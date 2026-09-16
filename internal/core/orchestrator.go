// Package core coordinates WS observations and bounded HTTP polling. Shared
// consensus transitions run under the state's write lock so every source uses
// the same stale-event guards and validator identity rules.
package core

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"strings"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"github.com/InjectiveLabs/cmt-top/internal/chain/cometrpc"
	"github.com/InjectiveLabs/cmt-top/internal/chain/cometws"
	"github.com/InjectiveLabs/cmt-top/internal/config"
	"github.com/InjectiveLabs/cmt-top/internal/cosmos"
	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/metrics"
	"github.com/InjectiveLabs/cmt-top/internal/obs"
	"github.com/InjectiveLabs/cmt-top/internal/state"
)

// Orchestrator wires the chain client, state, event bus, and divergence tracker.
type Orchestrator struct {
	cfg   config.Config
	bus   *events.Bus
	state *state.State
	log   *slog.Logger

	rpc *cometrpc.Pool
	ws  *cometws.Client
	q   *cosmos.Querier
	div *divergence.Tracker

	wsEvents         chan cometws.Event
	validatorRefresh chan struct{}
	comparisonPools  map[string]*cometrpc.Pool
}

// New builds an Orchestrator. It does not start any goroutines; call Run.
func New(cfg config.Config, bus *events.Bus, st *state.State) (*Orchestrator, error) {
	eps := make([]cometrpc.Endpoint, 0, len(cfg.Chain.RPCs))
	for _, r := range cfg.Chain.RPCs {
		eps = append(eps, cometrpc.Endpoint{URL: r.URL, Primary: r.Primary})
	}
	pool, err := cometrpc.NewPool(eps)
	if err != nil {
		return nil, err
	}

	wsCh := make(chan cometws.Event, 1024)
	logger := slog.Default().With("c", "orchestrator")

	urls := make([]string, 0, len(eps))
	for _, e := range eps {
		urls = append(urls, e.URL)
	}
	// Stay under the common max_subscriptions_per_client=5 limit. NewBlock
	// already carries the header so we drop NewBlockHeader; CompleteProposal
	// is informational and not currently displayed.
	queries := []string{
		"tm.event='NewBlock'",
		"tm.event='NewRound'",
		"tm.event='Vote'",
		"tm.event='ValidatorSetUpdates'",
	}
	wsClient, err := cometws.New(cometws.Options{
		Endpoints: urls,
		Queries:   queries,
		Logger:    logger,
		Handler: func(ev cometws.Event) {
			select {
			case wsCh <- ev:
			default:
				select {
				case <-wsCh:
					metrics.RecordIngestDrop(1)
				default:
				}
				select {
				case wsCh <- ev:
				default:
					metrics.RecordIngestDrop(1)
				}
			}
		},
		OnLifecycle: func(s cometws.State, ep string, err error) {
			st.Mutate(func(data *state.StateData) {
				data.WSConnected = s == cometws.StateLive
				data.Health.WSConnected = data.WSConnected
				data.Health.WSEndpoint = ep
				if err != nil {
					data.Health.LastError = err.Error()
				}
				if s == cometws.StateLive {
					data.Health.LastError = ""
				}
			})
			switch s {
			case cometws.StateLive:
				pool.SetActive(ep)
				bus.Publish(events.Event{Kind: events.KindConnectionRestored, Payload: events.ConnectionRestored{Endpoint: ep}})
			case cometws.StateReconnecting:
				bus.Publish(events.Event{Kind: events.KindConnectionLost, Payload: events.ConnectionLost{Endpoint: ep, Err: err}})
			}
		},
	})
	if err != nil {
		return nil, err
	}

	lcdURL := cfg.Chain.LCD
	if lcdURL == "" {
		lcdURL = lcdGuess(cfg)
	}
	q := cosmos.New(cosmos.Options{
		LCDURL:       lcdURL,
		Bech32Prefix: cfg.Chain.Bech32Prefix,
	})

	tracker := divergence.New(divergence.Config{
		ThresholdPct:    cfg.Divergence.ThresholdPct,
		HistorySize:     cfg.Divergence.HistorySize,
		IncludePrevotes: cfg.Divergence.IncludePrevotes,
	})
	comparisonPools := make(map[string]*cometrpc.Pool)
	if len(cfg.Chain.MonitoredRPCs) > 0 {
		for _, endpoint := range append([]string{cfg.PrimaryRPC()}, cfg.Chain.MonitoredRPCs...) {
			if _, exists := comparisonPools[endpoint]; exists {
				continue
			}
			p, err := cometrpc.NewPool([]cometrpc.Endpoint{{URL: endpoint, Primary: true}})
			if err != nil {
				return nil, fmt.Errorf("monitored RPC %s: %w", endpoint, err)
			}
			comparisonPools[endpoint] = p
		}
		st.Mutate(func(data *state.StateData) { data.RPCComparison.Status = "incomplete" })
	}

	return &Orchestrator{
		cfg:              cfg,
		bus:              bus,
		state:            st,
		log:              logger,
		rpc:              pool,
		ws:               wsClient,
		q:                q,
		div:              tracker,
		wsEvents:         wsCh,
		validatorRefresh: make(chan struct{}, 1),
		comparisonPools:  comparisonPools,
	}, nil
}

// Tracker returns the divergence tracker (for the web layer to query).
func (o *Orchestrator) Tracker() *divergence.Tracker { return o.div }

// RPC returns the RPC pool (for advanced web handlers).
func (o *Orchestrator) RPC() *cometrpc.Pool { return o.rpc }

// lcdGuess swaps an RPC URL host for a likely LCD host. Best-effort.
func lcdGuess(cfg config.Config) string {
	primary := cfg.PrimaryRPC()
	switch {
	case strings.Contains(primary, "tm.injective.network"),
		strings.Contains(primary, "rpc.injective"):
		return "https://lcd.injective.network"
	}
	return ""
}

// Run blocks until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) {
	defer obs.Recover("orchestrator")
	o.log.Info("starting", "primary_rpc", o.cfg.PrimaryRPC(), "ws_endpoints", len(o.cfg.Chain.RPCs))

	go o.pollLoop(ctx, "validators", o.cfg.Refresh.Validators.D(), func(c context.Context) {
		if err := o.refreshValidators(c); err != nil {
			o.log.Warn("validators refresh failed", "err", err)
		}
	})
	go o.pollLoop(ctx, "status", o.cfg.Refresh.Status.D(), func(c context.Context) {
		if err := o.refreshStatus(c); err != nil {
			o.log.Warn("status refresh failed", "err", err)
		}
	})
	go o.pollLoop(ctx, "upgrade", o.cfg.Refresh.UpgradePlan.D(), func(c context.Context) {
		if err := o.refreshUpgrade(c); err != nil {
			o.log.Debug("upgrade plan refresh failed", "err", err)
		}
	})
	go o.pollLoop(ctx, "block_time", o.cfg.Refresh.BlockTime.D(), func(c context.Context) {
		if err := o.refreshBlockTime(c); err != nil {
			o.log.Warn("block time refresh failed", "err", err)
		}
	})
	go o.pollLoop(ctx, "consensus", o.cfg.Refresh.Consensus.D(), func(c context.Context) {
		if o.ws.State() != cometws.StateLive || o.state.Snapshot().Health.Mode != "streaming" {
			if err := o.refreshConsensusViaHTTP(c); err != nil {
				o.log.Debug("consensus http fallback failed", "err", err)
			}
		}
	})
	if len(o.comparisonPools) > 0 {
		go o.pollLoop(ctx, "rpc_comparison", o.cfg.Refresh.Status.D(), o.refreshRPCComparison)
	}

	go func() {
		defer obs.Recover("ws.run")
		o.ws.Run(ctx)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-o.wsEvents:
			o.handleWSEvent(ev)
		}
	}
}

func (o *Orchestrator) pollLoop(ctx context.Context, name string, every time.Duration, fn func(context.Context)) {
	defer obs.Recover("poller." + name)
	if every <= 0 {
		every = 30 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	run := func() {
		pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		fn(pollCtx)
	}
	var wake <-chan struct{}
	if name == "validators" {
		wake = o.validatorRefresh
	}
	run() // run once immediately
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		case <-wake:
			run()
		}
	}
}

// ---------- WS event handling ----------

func (o *Orchestrator) handleWSEvent(ev cometws.Event) {
	now := time.Now()
	o.state.Mutate(func(s *state.StateData) {
		s.Health.LastEventAt = now
		s.Health.LastSuccessAt = now
		s.Health.LastError = ""
		s.ConsensusError = nil
	})
	switch d := ev.Data.(type) {
	case cometws.EventDataNewBlock:
		o.handleNewBlock(d)
	case cometws.EventDataNewRound:
		o.handleNewRound(d)
	case cometws.EventDataVote:
		o.handleVote(d.Vote)
	case cometws.EventDataValidatorSetUpdates:
		select {
		case o.validatorRefresh <- struct{}{}:
		default:
		}
	}
}

func (o *Orchestrator) handleNewBlock(d cometws.EventDataNewBlock) {
	h := int64(d.Block.Header.Height)
	if h <= 0 {
		return
	}
	blockIDHash := strings.ToLower(d.BlockID.Hash)
	appHash := strings.ToLower(d.Block.Header.AppHash)
	proposer := strings.ToUpper(d.Block.Header.ProposerAddress)
	accepted := false
	o.state.Mutate(func(s *state.StateData) {
		if h < s.LastCommittedHeight {
			return
		}
		for _, sample := range s.Blocks {
			if sample.Height == h {
				return
			}
		}
		accepted = true
		if h > s.LastCommittedHeight {
			s.LastCommittedHeight = h
		}
		appendBlock(s, state.BlockSample{Height: h, Time: d.Block.Header.Time,
			BlockIDHash: blockIDHash, AppHash: appHash, NumTxs: len(d.Block.Data.Txs)})
		// The next height has no observed proposer yet. Do not attribute the
		// committed block's proposer to its successor.
		advanceRound(s, h+1, 0, 1)
	})
	if !accepted {
		return
	}
	resolved := o.div.ResolveCommit(h, blockIDHash)
	o.bus.Publish(events.Event{Kind: events.KindNewBlock, Payload: events.NewBlock{
		Height: h, BlockIDHash: blockIDHash, AppHash: appHash,
		ProposerAddr: proposer, Time: d.Block.Header.Time, NumTxs: len(d.Block.Data.Txs),
	}})
	for _, r := range resolved {
		if r.IsDivergent {
			o.bus.Publish(events.Event{Kind: events.KindDivergenceResolved, Payload: r})
		}
	}
}

func (o *Orchestrator) handleNewRound(d cometws.EventDataNewRound) {
	height, round := int64(d.Height), int64(d.Round)
	step := stepFromString(d.Step)
	proposer := strings.ToUpper(d.Proposer.Address)
	accepted := false
	var started time.Time
	o.state.Mutate(func(s *state.StateData) {
		accepted = advanceRound(s, height, round, step)
		if !accepted {
			return
		}
		markProposer(s.LastRound, proposer)
		started = s.StartTime
	})
	if !accepted {
		return
	}
	o.div.ObserveRound(height, round, proposer)
	o.bus.Publish(events.Event{Kind: events.KindRoundChanged, Payload: events.RoundChanged{
		Height: height, Round: round, Step: step, StartTime: started, Proposer: proposer,
	}})
	if round > 0 {
		o.div.MarkRoundAdvanced(height, round-1)
	}
}

// clearRoundVotes wipes per-validator vote state and proposer flag in-place.
// Caller must hold the state's write lock (via Mutate).
func clearRoundVotes(rv *state.RoundView) {
	if rv == nil {
		return
	}
	for i := range rv.Validators {
		rv.Validators[i].RoundVote.Prevote = state.Vote{}
		rv.Validators[i].RoundVote.Precommit = state.Vote{}
		rv.Validators[i].RoundVote.IsProposer = false
	}
}

// markProposer sets IsProposer=true on the validator with the given address
// (uppercase hex) and clears the flag on every other validator.
func markProposer(rv *state.RoundView, addr string) {
	if rv == nil || addr == "" {
		return
	}
	for i := range rv.Validators {
		rv.Validators[i].RoundVote.IsProposer = rv.Validators[i].Validator.Address == addr
	}
}

func (o *Orchestrator) handleVote(v cometws.VoteData) {
	addr := strings.ToUpper(v.ValidatorAddress)
	hash := strings.ToLower(v.BlockID.Hash)
	var vt divergence.VoteType
	var pvt cmtproto.SignedMsgType
	var step int64
	switch v.Type {
	case cometws.VoteTypePrevote:
		vt, pvt, step = divergence.Prevote, cmtproto.PrevoteType, 4
	case cometws.VoteTypePrecommit:
		vt, pvt, step = divergence.Precommit, cmtproto.PrecommitType, 6
	default:
		return
	}
	// Preserve retained historical and conflicting observations independently of
	// the live dashboard's stale-event and first-vote guards.
	o.div.ObserveVote(divergence.VoteEvent{
		Height: int64(v.Height), Round: int64(v.Round), Type: vt,
		ValidatorAddr: addr, BlockIDHash: hash, Timestamp: v.Timestamp,
	})
	kind := state.VoteForBlock
	if hash == "" {
		kind = state.VoteNil
	}
	accepted := false
	o.state.Mutate(func(s *state.StateData) {
		accepted = advanceRound(s, int64(v.Height), int64(v.Round), step)
		if !accepted || s.LastRound == nil {
			return
		}
		for i := range s.LastRound.Validators {
			row := &s.LastRound.Validators[i]
			if row.Validator.Address != addr {
				continue
			}
			cell := state.Vote{Kind: kind, BlockIDHash: hash, Timestamp: v.Timestamp}
			// Keep the first observation, matching the tracker's deduplication.
			if vt == divergence.Prevote && row.RoundVote.Prevote.Kind != state.VoteAbsent || vt == divergence.Precommit && row.RoundVote.Precommit.Kind != state.VoteAbsent {
				accepted = false
				return
			}
			if vt == divergence.Prevote && row.RoundVote.Prevote.Kind == state.VoteAbsent {
				row.RoundVote.Prevote = cell
			}
			if vt == divergence.Precommit && row.RoundVote.Precommit.Kind == state.VoteAbsent {
				row.RoundVote.Precommit = cell
			}
			break
		}
	})
	// Stale events are not forwarded: every consumer sees the same acceptance
	// decision as the authoritative state.
	if !accepted {
		return
	}
	rep, trigger := o.div.IngestVote(divergence.VoteEvent{
		Height: int64(v.Height), Round: int64(v.Round), Type: vt,
		ValidatorAddr: addr, BlockIDHash: hash, Timestamp: v.Timestamp,
	})
	o.bus.Publish(events.Event{Kind: events.KindVoteReceived, Payload: events.VoteReceived{
		Height: int64(v.Height), Round: int64(v.Round), Type: pvt,
		ValidatorAddr: addr, BlockIDHash: hash, Timestamp: v.Timestamp,
	}})
	if trigger {
		o.bus.Publish(events.Event{Kind: events.KindDivergenceDetected, Payload: rep})
	}
}

// ---------- HTTP pollers ----------

func (o *Orchestrator) refreshStatus(ctx context.Context) error {
	st, endpoint, err := o.rpc.Status(ctx)
	if err != nil {
		o.state.Mutate(func(s *state.StateData) { s.StatusError = err; s.Health.LastError = err.Error() })
		return err
	}
	previous := o.state.Snapshot()
	if previous.NodeStatus != nil && previous.NodeStatus.Network != st.NodeInfo.Network {
		err := fmt.Errorf("RPC chain %q differs from established chain %q", st.NodeInfo.Network, previous.NodeStatus.Network)
		o.state.Mutate(func(s *state.StateData) { s.StatusError = err; s.Health.LastError = err.Error() })
		return err
	}
	ourAddr := strings.ToUpper(hex.EncodeToString(st.ValidatorInfo.Address))
	ns := &state.NodeStatus{
		Network:       st.NodeInfo.Network,
		CometVersion:  st.NodeInfo.Version,
		Moniker:       st.NodeInfo.Moniker,
		OurValidator:  ourAddr,
		CatchingUp:    st.SyncInfo.CatchingUp,
		LatestHeight:  st.SyncInfo.LatestBlockHeight,
		LatestBlockTs: st.SyncInfo.LatestBlockTime,
	}
	o.state.Mutate(func(s *state.StateData) {
		s.StatusError = nil
		s.NodeStatus = ns
		recordHTTPSuccess(s, endpoint)
		if ns.LatestHeight > s.LastCommittedHeight {
			s.LastCommittedHeight = ns.LatestHeight
			if s.Height > 0 && s.Height <= ns.LatestHeight {
				clearRoundVotes(s.LastRound)
				s.Height, s.Round, s.Step = 0, 0, 0
				s.StartTime = time.Time{}
				if s.LastRound != nil {
					s.LastRound.Round = 0
				}
			}
		}
	})
	o.bus.Publish(events.Event{Kind: events.KindStatusUpdated, Payload: events.StatusUpdated{
		Network:      ns.Network,
		CometVersion: ns.CometVersion,
		OurValidator: ns.OurValidator,
		CatchingUp:   ns.CatchingUp,
		LatestHeight: ns.LatestHeight,
	}})
	// Seed retained history on startup and keep it moving when only HTTP is
	// available. A status height alone never invents an active consensus round.
	if len(previous.Blocks) == 0 || !previous.Health.WSConnected {
		if err := o.refreshLatestBlock(ctx, ns.LatestHeight); err != nil {
			o.log.Debug("block history refresh failed", "err", err)
		}
	}
	return nil
}

func (o *Orchestrator) refreshValidators(ctx context.Context) error {
	vs, height, _, err := o.rpc.AllValidators(ctx, nil)
	if err != nil {
		o.state.Mutate(func(s *state.StateData) { s.ValidatorsError = err })
		return err
	}
	if len(vs) == 0 {
		err := fmt.Errorf("RPC returned an empty validator set")
		o.state.Mutate(func(s *state.StateData) { s.ValidatorsError = err })
		return err
	}
	dvs := make([]divergence.ValidatorPower, 0, len(vs))
	for _, v := range vs {
		addr := strings.ToUpper(hex.EncodeToString(v.Address))
		dvs = append(dvs, divergence.ValidatorPower{Address: addr, Power: v.VotingPower})
	}

	chainVals, err := o.q.Validators(ctx)
	if err != nil {
		o.log.Debug("LCD validators failed", "err", err)
		chainVals = o.state.Snapshot().ChainValidators
	}
	cvByCons := map[string]*state.ChainValidator{}
	for i := range chainVals {
		cv := &chainVals[i]
		cvByCons[cv.RawConsAddr] = cv
	}

	totalPow := big.NewInt(0)
	for _, v := range vs {
		totalPow.Add(totalPow, big.NewInt(v.VotingPower))
	}
	vwInfo := make([]state.ValidatorWithVote, 0, len(vs))
	for i, v := range vs {
		addr := strings.ToUpper(hex.EncodeToString(v.Address))
		vp := big.NewInt(v.VotingPower)
		pct := 0.0
		if totalPow.Sign() > 0 {
			f, _ := new(big.Float).Quo(new(big.Float).SetInt(vp), new(big.Float).SetInt(totalPow)).Float64()
			pct = f * 100
		}
		row := state.ValidatorWithVote{
			Validator: state.Validator{
				Address: addr, Index: i, VotingPower: vp, VotingPowerPercent: pct,
			},
		}
		if cv, ok := cvByCons[addr]; ok {
			c := *cv
			row.ChainValidator = &c
			dvs[i].Moniker = cv.Moniker
		}
		vwInfo = append(vwInfo, row)
	}

	cvSlice := append([]state.ChainValidator(nil), chainVals...)

	accepted := false
	o.state.Mutate(func(s *state.StateData) {
		accepted = mergeValidatorRound(s, vwInfo, totalPow, height)
		if !accepted {
			return
		}
		s.ValidatorsError = nil
		s.ChainValidators = cvSlice
	})
	if !accepted {
		return nil
	}
	o.div.SetValidators(dvs)
	o.bus.Publish(events.Event{Kind: events.KindValidatorSetUpdated, Payload: events.ValidatorSetUpdated{Height: height}})
	return nil
}

func (o *Orchestrator) refreshUpgrade(ctx context.Context) error {
	plan, err := o.q.UpgradePlan(ctx)
	if err != nil {
		o.state.Mutate(func(s *state.StateData) { s.UpgradeError = err })
		return err
	}
	o.state.Mutate(func(s *state.StateData) {
		s.UpgradeError = nil
		s.Upgrade = plan
	})
	update := events.UpgradePlanUpdated{}
	if plan != nil {
		update.Name, update.Height = plan.Name, plan.Height
	}
	o.bus.Publish(events.Event{Kind: events.KindUpgradePlanUpdated, Payload: update})
	return nil
}

func (o *Orchestrator) refreshBlockTime(ctx context.Context) error {
	statusRes, _, err := o.rpc.Status(ctx)
	if err != nil {
		return err
	}
	latest := statusRes.SyncInfo.LatestBlockHeight
	if latest <= 100 {
		return nil
	}
	latestB, _, err := o.rpc.Block(ctx, &latest)
	if err != nil {
		return err
	}
	earlier := latest - 100
	earlierB, _, err := o.rpc.Block(ctx, &earlier)
	if err != nil {
		return err
	}
	dt := latestB.Block.Time.Sub(earlierB.Block.Time) / 100
	o.state.Mutate(func(s *state.StateData) { s.BlockTime = dt })
	o.bus.Publish(events.Event{Kind: events.KindBlockTimeUpdated, Payload: events.BlockTimeUpdated{BlockTime: dt}})
	return nil
}

func (o *Orchestrator) refreshConsensusViaHTTP(ctx context.Context) (retErr error) {
	defer func() {
		if retErr != nil {
			o.state.Mutate(func(s *state.StateData) {
				if s.Health.ModeAt(time.Now()) == "streaming" {
					return
				}
				s.ConsensusError = retErr
				s.Health.LastError = retErr.Error()
			})
		}
	}()
	res, endpoint, err := o.rpc.ConsensusState(ctx)
	if err != nil {
		return err
	}
	var rs struct {
		HeightRoundStep string `json:"height/round/step"`
	}
	if err := jsonUnmarshal(res.RoundState, &rs); err != nil {
		return err
	}
	parts := strings.Split(rs.HeightRoundStep, "/")
	if len(parts) != 3 {
		return fmt.Errorf("invalid consensus height/round/step %q", rs.HeightRoundStep)
	}
	values := make([]int64, 3)
	for i, part := range parts {
		value, err := strconv.ParseInt(part, 10, 64)
		if err != nil || value < 0 {
			return fmt.Errorf("invalid consensus height/round/step %q", rs.HeightRoundStep)
		}
		values[i] = value
	}
	if values[0] == 0 {
		return fmt.Errorf("consensus height is zero")
	}
	accepted := false
	var started time.Time
	o.state.Mutate(func(s *state.StateData) {
		if s.Health.ModeAt(time.Now()) == "streaming" {
			// WS can recover while a fallback request is in flight. A delayed
			// response must not replace fresh consensus observations.
			return
		}
		s.ConsensusError = nil
		recordHTTPSuccess(s, endpoint)
		accepted = advanceRound(s, values[0], values[1], values[2])
		if accepted && s.Health.ModeAt(time.Now()) != "streaming" {
			// Compact HTTP consensus has no trustworthy per-validator vote
			// snapshot. Old WS observations must not look current in fallback.
			clearRoundVotes(s.LastRound)
		}
		started = s.StartTime
	})
	if accepted {
		o.div.ObserveRound(values[0], values[1], "")
		o.bus.Publish(events.Event{Kind: events.KindRoundChanged, Payload: events.RoundChanged{
			Height: values[0], Round: values[1], Step: values[2], StartTime: started,
		}})
	}
	return nil
}

func (o *Orchestrator) refreshLatestBlock(ctx context.Context, height int64) error {
	if height <= 0 {
		return nil
	}
	before := o.state.Snapshot()
	if len(before.Blocks) > 0 && before.Blocks[len(before.Blocks)-1].Height >= height {
		return nil
	}
	res, endpoint, err := o.rpc.Block(ctx, &height)
	if err != nil {
		return err
	}
	if res.Block == nil || res.Block.Height != height {
		return fmt.Errorf("RPC did not return block %d", height)
	}
	if before.NodeStatus != nil && res.Block.ChainID != before.NodeStatus.Network {
		return fmt.Errorf("block chain differs from status chain")
	}
	sample := state.BlockSample{Height: height, Time: res.Block.Time, BlockIDHash: hex.EncodeToString(res.BlockID.Hash),
		AppHash: hex.EncodeToString(res.Block.AppHash), NumTxs: len(res.Block.Txs)}
	o.state.Mutate(func(s *state.StateData) { appendBlock(s, sample); recordHTTPSuccess(s, endpoint) })
	for _, report := range o.div.ResolveCommit(height, sample.BlockIDHash) {
		if report.IsDivergent {
			o.bus.Publish(events.Event{Kind: events.KindDivergenceResolved, Payload: report})
		}
	}
	o.bus.Publish(events.Event{Kind: events.KindStatusUpdated, Payload: events.StatusUpdated{LatestHeight: height}})
	return nil
}

// stepFromString maps "RoundStepNewHeight"/"RoundStepPrevote" etc. into the
// numeric step Tendermint reports in /consensus_state.
func stepFromString(s string) int64 {
	switch s {
	case "RoundStepNewHeight":
		return 1
	case "RoundStepNewRound":
		return 2
	case "RoundStepPropose":
		return 3
	case "RoundStepPrevote":
		return 4
	case "RoundStepPrevoteWait":
		return 5
	case "RoundStepPrecommit":
		return 6
	case "RoundStepPrecommitWait":
		return 7
	case "RoundStepCommit":
		return 8
	}
	return 0
}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func parseI64(s string) int64 {
	var x int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return x
		}
		x = x*10 + int64(r-'0')
	}
	return x
}

// String for debug.
func (o *Orchestrator) String() string {
	return fmt.Sprintf("orchestrator{rpc=%s ws=%s}", o.rpc.Active(), o.ws.State())
}

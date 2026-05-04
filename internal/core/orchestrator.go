// Package core is the orchestrator: the single goroutine that mutates state
// and publishes events. WS events and HTTP poller results funnel into one
// dispatch loop, replacing tmtop's six-goroutine app.go.
package core

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"github.com/Ri-go/cmt-top/internal/chain/cometrpc"
	"github.com/Ri-go/cmt-top/internal/chain/cometws"
	"github.com/Ri-go/cmt-top/internal/config"
	"github.com/Ri-go/cmt-top/internal/cosmos"
	"github.com/Ri-go/cmt-top/internal/divergence"
	"github.com/Ri-go/cmt-top/internal/events"
	"github.com/Ri-go/cmt-top/internal/obs"
	"github.com/Ri-go/cmt-top/internal/state"
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

	wsEvents chan cometws.Event

	mu                sync.Mutex
	chainValByConsHex map[string]*state.ChainValidator
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
				default:
				}
				select {
				case wsCh <- ev:
				default:
				}
			}
		},
		OnLifecycle: func(s cometws.State, ep string, err error) {
			switch s {
			case cometws.StateLive:
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

	return &Orchestrator{
		cfg:               cfg,
		bus:               bus,
		state:             st,
		log:               logger,
		rpc:               pool,
		ws:                wsClient,
		q:                 q,
		div:               tracker,
		wsEvents:          wsCh,
		chainValByConsHex: map[string]*state.ChainValidator{},
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
		if o.ws.State() != cometws.StateLive {
			if err := o.refreshConsensusViaHTTP(c); err != nil {
				o.log.Debug("consensus http fallback failed", "err", err)
			}
		}
	})

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
	fn(ctx) // run once immediately
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

// ---------- WS event handling ----------

func (o *Orchestrator) handleWSEvent(ev cometws.Event) {
	switch d := ev.Data.(type) {
	case cometws.EventDataNewBlock:
		o.handleNewBlock(d)
	case cometws.EventDataNewRound:
		o.handleNewRound(d)
	case cometws.EventDataVote:
		o.handleVote(d.Vote)
	case cometws.EventDataValidatorSetUpdates:
		o.bus.Publish(events.Event{Kind: events.KindValidatorSetUpdated, Payload: events.ValidatorSetUpdated{}})
	}
}

func (o *Orchestrator) handleNewBlock(d cometws.EventDataNewBlock) {
	h := int64(d.Block.Header.Height)
	if h == 0 {
		return
	}
	blockIDHash := strings.ToLower(d.BlockID.Hash)
	appHash := strings.ToLower(d.Block.Header.AppHash)
	proposer := strings.ToUpper(d.Block.Header.ProposerAddress)

	o.state.Mutate(func(s *state.StateData) {
		s.Height = h
		s.Round = 0
		s.Step = 0
		s.StartTime = d.Block.Header.Time
		// Mark this height closed so late vote stragglers don't repaint the
		// just-cleared live row.
		if h > s.LastCommittedHeight {
			s.LastCommittedHeight = h
		}
		clearRoundVotes(s.LastRound)
		// NewRound fires sporadically on some chains (Injective skips most
		// events). NewBlock always fires reliably and carries the proposer of
		// the just-committed block, so use it as the proposer source. NewRound,
		// when it does fire, will override with the next round's proposer.
		markProposer(s.LastRound, proposer)
	})

	o.bus.Publish(events.Event{
		Kind: events.KindNewBlock,
		Payload: events.NewBlock{
			Height:       h,
			BlockIDHash:  blockIDHash,
			AppHash:      appHash,
			ProposerAddr: proposer,
			Time:         d.Block.Header.Time,
			NumTxs:       len(d.Block.Data.Txs),
		},
	})

	resolved := o.div.ResolveCommit(h, blockIDHash)
	for _, r := range resolved {
		if r.IsDivergent {
			o.bus.Publish(events.Event{Kind: events.KindDivergenceResolved, Payload: r})
		}
	}
	o.div.MarkRoundAdvanced(h-1, 0)
}

func (o *Orchestrator) handleNewRound(d cometws.EventDataNewRound) {
	step := stepFromString(d.Step)
	proposer := strings.ToUpper(d.Proposer.Address)
	o.state.Mutate(func(s *state.StateData) {
		s.Height = int64(d.Height)
		s.Round = int64(d.Round)
		s.Step = step
		// New round starts: clear last round's votes and mark the proposer.
		clearRoundVotes(s.LastRound)
		if s.LastRound != nil {
			s.LastRound.Round = int64(d.Round)
		}
		markProposer(s.LastRound, proposer)
	})
	o.bus.Publish(events.Event{
		Kind: events.KindRoundChanged,
		Payload: events.RoundChanged{
			Height:   int64(d.Height),
			Round:    int64(d.Round),
			Step:     step,
			Proposer: proposer,
		},
	})
	if d.Round > 0 {
		o.div.MarkRoundAdvanced(int64(d.Height), int64(d.Round)-1)
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
	switch v.Type {
	case cometws.VoteTypePrevote:
		vt = divergence.Prevote
		pvt = cmtproto.PrevoteType
	case cometws.VoteTypePrecommit:
		vt = divergence.Precommit
		pvt = cmtproto.PrecommitType
	default:
		return
	}
	rep, trigger := o.div.IngestVote(divergence.VoteEvent{
		Height:        int64(v.Height),
		Round:         int64(v.Round),
		Type:          vt,
		ValidatorAddr: addr,
		BlockIDHash:   hash,
		Timestamp:     v.Timestamp,
	})

	// Reflect the vote into per-validator state so both UIs render it. Skip
	// votes from older rounds — late arrivals shouldn't overwrite the live row.
	kind := state.VoteForBlock
	if hash == "" {
		kind = state.VoteNil
	}
	o.state.Mutate(func(s *state.StateData) {
		if s.LastRound == nil {
			return
		}
		// Reject stragglers from heights that have already committed.
		if int64(v.Height) <= s.LastCommittedHeight {
			return
		}
		// Stale-round guard within the same height.
		if int64(v.Height) == s.Height && int64(v.Round) < s.LastRound.Round {
			return
		}
		// Round advanced (we missed a NewRound) — clear stale votes for the
		// previous round, but DO NOT clear the proposer flag (NewBlock just
		// set it and there's no fresher source).
		if int64(v.Round) > s.LastRound.Round {
			for i := range s.LastRound.Validators {
				s.LastRound.Validators[i].RoundVote.Prevote = state.Vote{}
				s.LastRound.Validators[i].RoundVote.Precommit = state.Vote{}
			}
			s.LastRound.Round = int64(v.Round)
			s.Round = int64(v.Round)
		}
		for i := range s.LastRound.Validators {
			if s.LastRound.Validators[i].Validator.Address != addr {
				continue
			}
			cell := state.Vote{Kind: kind, BlockIDHash: hash, Timestamp: v.Timestamp}
			switch vt {
			case divergence.Prevote:
				s.LastRound.Validators[i].RoundVote.Prevote = cell
			case divergence.Precommit:
				s.LastRound.Validators[i].RoundVote.Precommit = cell
			}
			break
		}
	})

	o.bus.Publish(events.Event{
		Kind: events.KindVoteReceived,
		Payload: events.VoteReceived{
			Height:        int64(v.Height),
			Round:         int64(v.Round),
			Type:          pvt,
			ValidatorAddr: addr,
			BlockIDHash:   hash,
			Timestamp:     v.Timestamp,
		},
	})

	if trigger {
		o.bus.Publish(events.Event{Kind: events.KindDivergenceDetected, Payload: rep})
	}
}

// ---------- HTTP pollers ----------

func (o *Orchestrator) refreshStatus(ctx context.Context) error {
	st, _, err := o.rpc.Status(ctx)
	if err != nil {
		o.state.Mutate(func(s *state.StateData) { s.StatusError = err })
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
		s.ActiveRPC = o.rpc.Active()
	})
	o.bus.Publish(events.Event{Kind: events.KindStatusUpdated, Payload: events.StatusUpdated{
		Network:      ns.Network,
		CometVersion: ns.CometVersion,
		OurValidator: ns.OurValidator,
		CatchingUp:   ns.CatchingUp,
		LatestHeight: ns.LatestHeight,
	}})
	return nil
}

func (o *Orchestrator) refreshValidators(ctx context.Context) error {
	vs, height, _, err := o.rpc.AllValidators(ctx, nil)
	if err != nil {
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

	o.div.SetValidators(dvs)

	o.mu.Lock()
	o.chainValByConsHex = cvByCons
	o.mu.Unlock()

	cvSlice := append([]state.ChainValidator(nil), chainVals...)

	rv := &state.RoundView{
		Round:      0,
		Validators: vwInfo,
		TotalVP:    totalPow,
	}

	o.state.Mutate(func(s *state.StateData) {
		s.ValidatorsError = nil
		s.ChainValidators = cvSlice
		if s.LastRound == nil || len(s.LastRound.Validators) != len(vwInfo) {
			s.LastRound = rv
		} else {
			for i := range s.LastRound.Validators {
				s.LastRound.Validators[i].Validator = vwInfo[i].Validator
				s.LastRound.Validators[i].ChainValidator = vwInfo[i].ChainValidator
			}
		}
	})

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
	if plan != nil {
		o.bus.Publish(events.Event{Kind: events.KindUpgradePlanUpdated, Payload: events.UpgradePlanUpdated{
			Name: plan.Name, Height: plan.Height,
		}})
	}
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

func (o *Orchestrator) refreshConsensusViaHTTP(ctx context.Context) error {
	res, _, err := o.rpc.ConsensusState(ctx)
	if err != nil {
		o.state.Mutate(func(s *state.StateData) { s.ConsensusError = err })
		return err
	}
	// res.RoundState is json.RawMessage; parse just the height/round/step.
	var rs struct {
		HeightRoundStep string `json:"height/round/step"`
	}
	if err := jsonUnmarshal(res.RoundState, &rs); err != nil {
		return err
	}
	o.state.Mutate(func(s *state.StateData) {
		s.ConsensusError = nil
		parts := strings.Split(rs.HeightRoundStep, "/")
		if len(parts) == 3 {
			s.Height = parseI64(parts[0])
			s.Round = parseI64(parts[1])
			s.Step = parseI64(parts[2])
		}
	})
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

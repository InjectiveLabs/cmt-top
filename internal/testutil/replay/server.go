package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type peer struct {
	conn          *websocket.Conn
	mu            sync.Mutex
	subscriptions map[string]json.RawMessage
}
type Stats struct {
	Identity           string            `json:"identity"`
	Profile            string            `json:"profile"`
	Seed               int64             `json:"seed"`
	Validators         int               `json:"validators"`
	Height             int64             `json:"height"`
	Round              int64             `json:"round"`
	CommittedHeight    int64             `json:"committedHeight"`
	Events             uint64            `json:"events"`
	Connections        uint64            `json:"connections"`
	ActiveConnections  int               `json:"activeConnections"`
	Requests           map[string]uint64 `json:"requests"`
	UnexpectedRequests uint64            `json:"unexpectedRequests"`
	Paused             bool              `json:"paused"`
	Ready              bool              `json:"ready"`
}

type Server struct {
	opts       Options
	validators []Validator
	mu         sync.Mutex
	peers      map[*peer]bool
	stats      Stats
	paused     bool
	oracle     *Oracle
}

func New(opts Options) (*Server, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	vs := Validators(opts.Profile, opts.Seed)
	return &Server{opts: opts, validators: vs, peers: make(map[*peer]bool), oracle: NewOracle(vs), stats: Stats{Identity: Identity, Profile: opts.Profile, Seed: opts.Seed, Validators: len(vs), Height: InitialHeight - int64(opts.HistoryBlocks) + 1, CommittedHeight: InitialHeight - int64(opts.HistoryBlocks), Requests: make(map[string]uint64)}}, nil
}

func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.stats
	v.Requests = make(map[string]uint64, len(s.stats.Requests))
	for k, n := range s.stats.Requests {
		v.Requests[k] = n
	}
	v.ActiveConnections = len(s.peers)
	v.Paused = s.paused
	return v
}

func (s *Server) count(name string) { s.mu.Lock(); s.stats.Requests[name]++; s.mu.Unlock() }
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/_fixture/stats":
		writeJSON(w, s.Stats())
		return
	case "/_fixture/oracle":
		h, err := strconv.ParseInt(r.URL.Query().Get("height"), 10, 64)
		if err != nil {
			http.Error(w, "height required", 400)
			return
		}
		writeJSON(w, s.oracle.Height(h))
		return
	case "/_fixture/control":
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		var c struct {
			Paused     *bool `json:"paused"`
			Disconnect bool  `json:"disconnect"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&c) != nil {
			http.Error(w, "bad command", 400)
			return
		}
		s.mu.Lock()
		if c.Paused != nil {
			s.paused = *c.Paused
		}
		ps := s.peersCopyLocked()
		s.mu.Unlock()
		if c.Disconnect {
			for _, p := range ps {
				_ = p.conn.Close()
			}
		}
		writeJSON(w, s.Stats())
		return
	case "/websocket":
		s.serveWS(w, r)
		return
	case "/cosmos/staking/v1beta1/validators":
		s.count("lcd.validators")
		vals := make([]any, 0, len(s.validators))
		for i, v := range s.validators {
			vals = append(vals, map[string]any{"operator_address": fmt.Sprintf("fixturevaloper%040d", i), "consensus_pubkey": map[string]any{"@type": "/cosmos.crypto.ed25519.PubKey", "key": v.PublicKey}, "description": map[string]any{"moniker": v.Moniker}, "status": "BOND_STATUS_BONDED", "commission": map[string]any{"commission_rates": map[string]any{"rate": "0.05"}}})
		}
		writeJSON(w, map[string]any{"validators": vals, "pagination": map[string]any{"next_key": ""}})
		return
	case "/cosmos/upgrade/v1beta1/current_plan":
		s.count("lcd.upgrade")
		writeJSON(w, map[string]any{"plan": map[string]any{"name": "synthetic-capacity-upgrade", "height": fmt.Sprint(InitialHeight + 1)}})
		return
	case "/":
		if r.Method == "POST" {
			s.serveRPC(w, r)
			return
		}
	}
	s.mu.Lock()
	s.stats.UnexpectedRequests++
	s.mu.Unlock()
	http.Error(w, "unexpected fixture request", 400)
}

func (s *Server) serveRPC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage            `json:"id"`
		Method string                     `json:"method"`
		Params map[string]json.RawMessage `json:"params"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req) != nil {
		http.Error(w, "bad RPC", 400)
		return
	}
	s.count("rpc." + req.Method)
	st := s.Stats()
	var result any
	switch req.Method {
	case "status":
		result = map[string]any{"node_info": map[string]any{"network": "capacity-replay-1", "version": "0.38.21", "moniker": "Synthetic replay"}, "sync_info": map[string]any{"latest_block_height": fmt.Sprint(st.CommittedHeight), "latest_block_time": Timestamp(st.CommittedHeight, 0, 0)}, "validator_info": map[string]any{"address": s.validators[0].Address, "voting_power": fmt.Sprint(s.validators[0].Power)}}
	case "validators":
		h := st.CommittedHeight
		if v := strings.Trim(string(req.Params["height"]), "\""); v != "" && v != "null" {
			if n, e := strconv.ParseInt(v, 10, 64); e == nil {
				h = n
			}
		}
		vals := make([]any, 0, len(s.validators))
		for _, v := range s.validators {
			vals = append(vals, map[string]any{"address": v.Address, "voting_power": fmt.Sprint(v.Power), "proposer_priority": "0"})
		}
		result = map[string]any{"block_height": fmt.Sprint(h), "validators": vals, "count": fmt.Sprint(len(vals)), "total": fmt.Sprint(len(vals))}
	case "block":
		h := st.CommittedHeight
		if n, e := strconv.ParseInt(strings.Trim(string(req.Params["height"]), "\""), 10, 64); e == nil && n > 0 {
			h = n
		}
		result = Block(h, s.validators).Value
	case "consensus_state":
		result = map[string]any{"round_state": map[string]any{"height/round/step": fmt.Sprintf("%d/%d/3", st.Height, st.Round)}}
	default:
		s.mu.Lock()
		s.stats.UnexpectedRequests++
		s.mu.Unlock()
		http.Error(w, "unexpected RPC method", 400)
		return
	}
	writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	c, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	p := &peer{conn: c, subscriptions: make(map[string]json.RawMessage)}
	s.mu.Lock()
	s.peers[p] = true
	s.stats.Connections++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.peers, p); s.mu.Unlock(); _ = c.Close() }()
	c.SetReadLimit(1 << 20)
	for {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Query string `json:"query"`
			} `json:"params"`
		}
		if c.ReadJSON(&req) != nil {
			return
		}
		if req.Method != "subscribe" {
			s.mu.Lock()
			s.stats.UnexpectedRequests++
			s.mu.Unlock()
			return
		}
		s.count("ws.subscribe")
		p.mu.Lock()
		p.subscriptions[req.Params.Query] = req.ID
		err = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
		p.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (s *Server) peersCopyLocked() []*peer {
	ps := make([]*peer, 0, len(s.peers))
	for p := range s.peers {
		ps = append(ps, p)
	}
	return ps
}

// Run waits for all four real orchestrator subscriptions and an RPC/LCD roster
// read before emitting. That prevents fixture startup races from omitting votes.
func (s *Server) Run(ctx context.Context) {
	defer s.Close()
	for !s.canStart() {
		if !wait(ctx, 10*time.Millisecond) {
			return
		}
	}
	// Allow the HTTP response and roster merge to finish before first votes.
	if !wait(ctx, 100*time.Millisecond) {
		return
	}
	start := InitialHeight - int64(s.opts.HistoryBlocks) + 1
	for h := start; h <= InitialHeight; h++ {
		s.emit(Block(h, s.validators))
		if !wait(ctx, 2*time.Millisecond) {
			return
		}
	}
	h := InitialHeight + 1
	for {
		for round := 0; round < s.opts.Rounds; round++ {
			for _, ev := range Round(h, int64(round), s.validators) {
				if !s.waitUnpaused(ctx) {
					return
				}
				s.emit(ev)
				if !wait(ctx, s.opts.EventInterval) {
					return
				}
			}
		}
		s.mu.Lock()
		s.stats.Ready = true
		s.mu.Unlock()
		if s.opts.Stalled {
			// Fill the initially absent validator's prevote in one earlier round
			// per second. This exercises cache invalidation while150 investigators
			// watch a stalled height; after one pass, exact duplicates test304s.
			for lateRound := int64(0); ; lateRound++ {
				if !wait(ctx, time.Second) || !s.waitUnpaused(ctx) {
					return
				}
				round := lateRound % int64(s.opts.Rounds)
				s.emit(voteEvent(h, round, len(s.validators)-1, 1, Hash(h, round, "late"), s.validators))
			}
		}
		if !wait(ctx, s.opts.BlockInterval) || !s.waitUnpaused(ctx) {
			return
		}
		s.emit(Block(h, s.validators))
		h++
	}
}

func (s *Server) canStart() bool {
	s.mu.Lock()
	ready := s.stats.Requests["rpc.validators"] > 0 && s.stats.Requests["lcd.validators"] > 0
	ps := s.peersCopyLocked()
	s.mu.Unlock()
	if !ready {
		return false
	}
	for _, p := range ps {
		p.mu.Lock()
		n := len(p.subscriptions)
		p.mu.Unlock()
		if n == 4 {
			return true
		}
	}
	return false
}
func (s *Server) waitUnpaused(ctx context.Context) bool {
	for {
		s.mu.Lock()
		paused := s.paused
		s.mu.Unlock()
		if !paused {
			return ctx.Err() == nil
		}
		if !wait(ctx, 10*time.Millisecond) {
			return false
		}
	}
}
func wait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (s *Server) emit(ev Event) {
	s.oracle.Observe(ev)
	s.mu.Lock()
	s.stats.Events++
	if ev.Kind == "NewBlock" {
		if ev.Height > s.stats.CommittedHeight {
			s.stats.CommittedHeight = ev.Height
		}
		s.stats.Height = ev.Height + 1
		s.stats.Round = 0
	} else if ev.Kind == "NewRound" {
		s.stats.Height = ev.Height
		s.stats.Round = ev.Round
	}
	ps := s.peersCopyLocked()
	s.mu.Unlock()
	query := "tm.event='" + ev.Kind + "'"
	for _, p := range ps {
		p.mu.Lock()
		id, ok := p.subscriptions[query]
		if ok {
			_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if p.conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"query": query, "data": map[string]any{"type": "tendermint/event/" + ev.Kind, "value": ev.Value}}}) != nil {
				_ = p.conn.Close()
			}
		}
		p.mu.Unlock()
	}
}
func (s *Server) Close() {
	s.mu.Lock()
	ps := s.peersCopyLocked()
	s.mu.Unlock()
	for _, p := range ps {
		_ = p.conn.Close()
	}
}

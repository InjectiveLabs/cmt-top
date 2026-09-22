package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/InjectiveLabs/cmt-top/internal/metrics"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/events"
	"github.com/InjectiveLabs/cmt-top/internal/obs"
	"github.com/InjectiveLabs/cmt-top/internal/state"
)

// Options configure the web server.
type Options struct {
	MaxClients     int
	APIRateLimit   int
	TrustedProxies []string
	Capacity       *metrics.Capacity
	Listen         string
	Token          string
	CORSOrigin     string
	DisplayName    string
	ExplorerURL    string
	State          *state.State
	Bus            *events.Bus
	Tracker        *divergence.Tracker
	Logger         *slog.Logger
	Ring           *obs.Ring
	Version        string
	Ready          func() bool
}

// Server is the HTTP server.
type Server struct {
	opts   Options
	hub    *wsHub
	rounds *roundsCache
}

// New constructs a Server.
func New(opts Options) *Server {
	if opts.MaxClients <= 0 {
		opts.MaxClients = 256
	}
	if opts.APIRateLimit <= 0 {
		opts.APIRateLimit = 600
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	hub := newWSHub(opts.Bus, opts.State, opts.Tracker, opts.Logger.With("c", "wshub"), opts.CORSOrigin)
	hub.explorerURL = opts.ExplorerURL
	hub.displayName = opts.DisplayName
	hub.maxClients = opts.MaxClients
	hub.epoch = newServerEpoch()
	hub.capabilities = []string{"context-v1", "rounds-compact-v1", "rounds-capture-v1"}
	hub.observe = opts.Capacity.ObserveWS
	return &Server{opts: opts, hub: hub, rounds: newRoundsCache(opts.Tracker, opts.Capacity.ObserveReport)}
}

// Handler builds the HTTP surface, also used by local integration tests.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(bodySizeLimit(1 << 20)) // 1 MiB cap on any request body
	r.Use(s.observeHTTP)
	r.Use(s.middleware)

	r.Get("/healthz", s.handleHealthz)
	r.Get("/readyz", s.handleReadyz)
	// Note: /metrics is served on the dedicated metrics listener (default
	// 127.0.0.1:9091). Mounting it here too would bypass the bearer-token gate
	// for /api/*.

	r.Route("/api", func(r chi.Router) {
		r.Use(s.requestBudget())
		r.Get("/session", s.handleSession)
		r.Get("/state", s.handleState)
		r.Get("/validators", s.handleValidators)
		r.Get("/validators/{address}", s.handleValidator)
		r.Get("/divergence", s.handleDivergence)
		r.Get("/divergence/history", s.handleDivergenceHistory)
		r.Get("/blocks", s.handleBlocks)
		r.Get("/blocks/{height}/rounds", s.handleBlockRounds)
		r.Get("/chain", s.handleChain)
		r.Get("/version", s.handleVersion)
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "API route not found")
		})
	})

	r.With(s.handshakeBudget()).Get("/ws", s.hub.serveWS)

	// SPA at /
	spaHandler := s.spa()
	r.Handle("/*", spaHandler)

	return r
}

// Run serves HTTP and closes every websocket when the context ends.
func (s *Server) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go s.hub.run(ctx)
	srv := &http.Server{
		Addr:              s.opts.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Slow-client mitigation. Long-lived WS connections go through Hijack
		// which detaches from these timeouts, so the WS path is unaffected.
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if s.opts.Token == "" &&
		!strings.HasPrefix(s.opts.Listen, "127.") &&
		!strings.HasPrefix(s.opts.Listen, "localhost") {
		s.opts.Logger.Warn("web server bound to non-loopback address without a bearer token; dashboard is exposed without auth",
			"addr", s.opts.Listen)
	}
	s.opts.Logger.Info("web listening", "addr", s.opts.Listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		// CORS for /api/* and /ws.
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
			if s.opts.CORSOrigin != "" {
				w.Header().Set("Access-Control-Allow-Origin", s.opts.CORSOrigin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Expose-Headers", "ETag, Retry-After")
				if r.Method == "OPTIONS" {
					w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-None-Match")
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
		}

		// Auth for /api/* and /ws (when token is set).
		if s.opts.Token != "" && (strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws") {
			tok := bearerOrQuery(r)
			// Constant-time compare prevents timing side channels.
			expected := []byte(s.opts.Token)
			got := []byte(tok)
			if len(got) != len(expected) || subtle.ConstantTimeCompare(got, expected) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// Strip ?token= from the URL before downstream handlers (or any
			// future logging middleware) ever see it. Browser history and
			// reverse-proxy access logs may still capture the original; this
			// prevents in-process leakage at least.
			if q := r.URL.Query(); q.Has("token") {
				q.Del("token")
				r.URL.RawQuery = q.Encode()
			}
		}
		next.ServeHTTP(w, r)
	})
}

// bodySizeLimit caps every request body at n bytes; oversized requests get a
// 413 the first time a handler tries to read past the limit.
func bodySizeLimit(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

func bearerOrQuery(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

func (s *Server) spa() http.Handler {
	sub := SPA()
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// API/WS already handled by chi.
		// Prefer static file if it exists; otherwise serve index.html (or stub).
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback: try index.html; if not built, serve stub.html.
		if data, err := fs.ReadFile(sub, "index.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		if data, err := fs.ReadFile(sub, "stub.html"); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write(data)
			return
		}
		http.Error(w, "web UI not built (run `make web`)", http.StatusServiceUnavailable)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"code": http.StatusText(code), "message": msg}})
}

// ---- handlers ----

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Write([]byte("ok"))
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if s.opts.Ready != nil && !s.opts.Ready() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ready"))
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"version": s.opts.Version})
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	snap := s.opts.State.Snapshot()
	out := snapshotJSON(snap, s.opts.Tracker, s.opts.Bus)
	out["explorerURL"] = s.opts.ExplorerURL
	out["displayName"] = s.opts.DisplayName
	writeJSON(w, 200, out)
}

func (s *Server) handleChain(w http.ResponseWriter, _ *http.Request) {
	snap := s.opts.State.Snapshot()
	out := map[string]any{
		"height":          snap.Height,
		"round":           snap.Round,
		"step":            snap.Step,
		"blockTime":       snap.BlockTime.Milliseconds(),
		"activeRPC":       snap.ActiveRPC,
		"committedHeight": snap.LastCommittedHeight,
		"health":          snap.Health,
		"rpcComparison":   snap.RPCComparison,
		"upgrade":         nil,
	}
	if snap.NodeStatus != nil {
		out["network"] = snap.NodeStatus.Network
		out["cometVersion"] = snap.NodeStatus.CometVersion
		out["catchingUp"] = snap.NodeStatus.CatchingUp
		out["ourValidator"] = snap.NodeStatus.OurValidator
	}
	if snap.Upgrade != nil {
		out["upgrade"] = map[string]any{
			"name":   snap.Upgrade.Name,
			"height": snap.Upgrade.Height,
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleValidators(w http.ResponseWriter, r *http.Request) {
	snap := s.opts.State.Snapshot()
	if snap.LastRound == nil {
		writeJSON(w, 200, map[string]any{"total": 0, "items": []any{}})
		return
	}
	rows := snap.LastRound.Validators
	q := r.URL.Query()
	if search := strings.ToLower(q.Get("search")); search != "" {
		filtered := rows[:0]
		for _, v := range rows {
			mon := ""
			operator := ""
			if v.ChainValidator != nil {
				mon = strings.ToLower(v.ChainValidator.Moniker)
				operator = strings.ToLower(v.ChainValidator.OperatorAddress)
			}
			if strings.Contains(mon, search) || strings.Contains(strings.ToLower(v.Validator.Address), search) || strings.Contains(operator, search) {
				filtered = append(filtered, v)
			}
		}
		rows = filtered
	}
	out := make([]map[string]any, 0, len(rows))
	for _, v := range rows {
		out = append(out, validatorJSON(v))
	}
	writeJSON(w, 200, map[string]any{"total": len(out), "items": out})
}

func (s *Server) handleValidator(w http.ResponseWriter, r *http.Request) {
	addr := strings.ToUpper(chi.URLParam(r, "address"))
	snap := s.opts.State.Snapshot()
	if snap.LastRound == nil {
		writeError(w, 404, "not found")
		return
	}
	for _, v := range snap.LastRound.Validators {
		if v.Validator.Address == addr {
			writeJSON(w, 200, validatorJSON(v))
			return
		}
	}
	writeError(w, 404, "not found")
}

func (s *Server) handleDivergence(w http.ResponseWriter, r *http.Request) {
	rep := s.opts.Tracker.CurrentReport()
	q := r.URL.Query()
	if h := q.Get("height"); h != "" {
		filtered := rep.Live[:0]
		for _, rd := range rep.Live {
			if fmt.Sprintf("%d", rd.Height) == h {
				filtered = append(filtered, rd)
			}
		}
		rep.Live = filtered
	}
	writeJSON(w, 200, map[string]any{"live": rep.Live})
}

func (s *Server) handleDivergenceHistory(w http.ResponseWriter, _ *http.Request) {
	rep := s.opts.Tracker.CurrentReport()
	writeJSON(w, 200, map[string]any{"history": rep.History})
}

func (s *Server) handleBlocks(w http.ResponseWriter, _ *http.Request) {
	samples := s.opts.State.Snapshot().Blocks
	if samples == nil {
		samples = []state.BlockSample{}
	}
	writeJSON(w, 200, map[string]any{"items": samples})
}

// ---- shared JSON shapes ----

func validatorJSON(v state.ValidatorWithVote) map[string]any {
	out := map[string]any{
		"address":            v.Validator.Address,
		"index":              v.Validator.Index,
		"votingPower":        v.Validator.VotingPower.String(),
		"votingPowerPercent": v.Validator.VotingPowerPercent,
		"prevote":            voteJSON(v.RoundVote.Prevote),
		"precommit":          voteJSON(v.RoundVote.Precommit),
		"isProposer":         v.RoundVote.IsProposer,
	}
	if v.ChainValidator != nil {
		out["operatorAddress"] = v.ChainValidator.OperatorAddress
		out["moniker"] = v.ChainValidator.Moniker
		out["jailed"] = v.ChainValidator.Jailed
		out["active"] = v.ChainValidator.Active
		out["commissionRate"] = v.ChainValidator.CommissionRate
	}
	return out
}

func voteJSON(v state.Vote) map[string]any {
	return map[string]any{
		"kind":        v.Kind.String(),
		"blockIDHash": v.BlockIDHash,
	}
}

func snapshotJSON(snap state.StateData, tracker *divergence.Tracker, _ *events.Bus) map[string]any {
	validators := []map[string]any{}
	if snap.LastRound != nil {
		for _, v := range snap.LastRound.Validators {
			validators = append(validators, validatorJSON(v))
		}
	}
	out := map[string]any{
		"height":          snap.Height,
		"round":           snap.Round,
		"step":            snap.Step,
		"startTime":       snap.StartTime,
		"blockTime":       snap.BlockTime.Milliseconds(),
		"activeRPC":       snap.ActiveRPC,
		"validators":      validators,
		"committedHeight": snap.LastCommittedHeight,
		"health":          snap.Health,
		"blocks":          snap.Blocks,
		"rpcComparison":   snap.RPCComparison,
		"upgrade":         nil,
	}
	if snap.NodeStatus != nil {
		out["chain"] = map[string]any{
			"network":      snap.NodeStatus.Network,
			"cometVersion": snap.NodeStatus.CometVersion,
			"ourValidator": snap.NodeStatus.OurValidator,
			"catchingUp":   snap.NodeStatus.CatchingUp,
		}
	}
	if snap.Upgrade != nil {
		out["upgrade"] = map[string]any{
			"name":   snap.Upgrade.Name,
			"height": snap.Upgrade.Height,
		}
	}
	if tracker != nil {
		rep := tracker.CurrentReport()
		out["divergence"] = map[string]any{
			"live":    rep.Live,
			"history": rep.History,
		}
	}
	out["errors"] = errorMap(snap)
	return out
}

func errorMap(s state.StateData) map[string]string {
	out := map[string]string{}
	if s.ConsensusError != nil {
		out["consensus"] = s.ConsensusError.Error()
	}
	if s.ValidatorsError != nil {
		out["validators"] = s.ValidatorsError.Error()
	}
	if s.StatusError != nil {
		out["status"] = s.StatusError.Error()
	}
	if s.UpgradeError != nil {
		out["upgrade"] = s.UpgradeError.Error()
	}
	return out
}

// observeHTTP uses chi's capability-preserving writer, including Hijacker for WS.
func (s *Server) observeHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(wrapped, r)
		route := chi.RouteContext(r.Context()).RoutePattern()
		status := wrapped.Status()
		if status == 0 {
			if route == "/ws" {
				status = http.StatusSwitchingProtocols
			} else {
				status = http.StatusOK
			}
		}
		s.opts.Capacity.ObserveHTTP(route, status, time.Since(start))
	})
}

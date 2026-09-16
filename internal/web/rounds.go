package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/InjectiveLabs/cmt-top/internal/divergence"
	"github.com/InjectiveLabs/cmt-top/internal/state"
)

// Block investigation uses a dedicated read-only endpoint rather than sending
// the retained archive to every dashboard websocket client on every vote.
// Heights fit exactly in the browser's number representation.
const maxBrowserHeight int64 = 1<<53 - 1

type investigationContext struct {
	ChainID         string         `json:"chainId"`
	ActiveHeight    int64          `json:"activeHeight"`
	ActiveRound     int64          `json:"activeRound"`
	CommittedHeight int64          `json:"committedHeight"`
	Health          state.Health   `json:"health"`
	Upgrade         map[string]any `json:"upgrade"`
	GeneratedAt     time.Time      `json:"generatedAt"`
	Source          string         `json:"source"`
}

func (s *Server) handleBlockRounds(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "height")
	height, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || height <= 0 || height > maxBrowserHeight || strconv.FormatInt(height, 10) != raw {
		writeError(w, http.StatusBadRequest, "height must be a positive decimal integer no greater than 9007199254740991")
		return
	}
	if s.opts.Tracker == nil || s.opts.State == nil {
		writeError(w, http.StatusServiceUnavailable, "round observations are unavailable")
		return
	}
	snapshot := s.opts.State.Snapshot()
	context := investigationContext{
		ActiveHeight: snapshot.Height, ActiveRound: snapshot.Round,
		CommittedHeight: snapshot.LastCommittedHeight, Health: snapshot.Health,
		GeneratedAt: time.Now().UTC(),
		Source:      "Observed votes from the configured RPC; not a complete network record.",
	}
	if snapshot.NodeStatus != nil {
		context.ChainID = snapshot.NodeStatus.Network
	}
	if snapshot.Upgrade != nil {
		context.Upgrade = map[string]any{"name": snapshot.Upgrade.Name, "height": snapshot.Upgrade.Height}
	}
	writeJSON(w, http.StatusOK, struct {
		divergence.BlockInvestigation
		Context investigationContext `json:"context"`
	}{BlockInvestigation: s.opts.Tracker.BlockInvestigation(height), Context: context})
}

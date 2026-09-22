package web

import (
	"encoding/json"
	"fmt"
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

	q := r.URL.Query()
	view := q.Get("view")
	if view != "" && view != "full" && view != "compact" {
		writeError(w, 400, "view must be full or compact")
		return
	}
	selection, err := parseRoundSelection(q)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	capture := q.Get("capture") == "1"
	if q.Has("capture") && (!capture || view != "full") {
		writeError(w, 400, "capture requires view=full&capture=1")
		return
	}
	cache := s.rounds
	lane := cache.readers
	if capture {
		lane = cache.captures
	} else if view != "compact" {
		lane = cache.legacy
	}
	if !cache.admit(lane) {
		w.Header().Set("Retry-After", "1")
		writeError(w, 503, "round report capacity is busy; retry shortly")
		return
	}
	defer func() { <-lane }()
	var entry *roundsEntry
	var version divergence.ArchiveVersion
	started := time.Now()
	if capture {
		entry, version, err = cache.build(r.Context(), height)
		cache.record("capture", 1)
	} else {
		entry, version, err = cache.get(r.Context(), height, view == "compact")
	}
	if err != nil {
		if r.Context().Err() == nil {
			writeError(w, 503, "round observations are temporarily unavailable")
		}
		return
	}
	if view == "compact" {
		encoded, encodeErr := cache.compact(r.Context(), entry, version, s.hub.epoch, selection)
		if encodeErr != nil {
			if r.Context().Err() == nil {
				writeError(w, 503, "round observations are temporarily unavailable")
			}
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
		w.Header().Set("ETag", encoded.etag)
		if matchesETag(r.Header.Get("If-None-Match"), encoded.etag) {
			cache.record("not_modified", 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded.body)
		return
	}
	body := entry.full
	if !capture {
		encoded, encodeErr := cache.fullView(r.Context(), entry, version)
		if encodeErr != nil {
			if r.Context().Err() == nil {
				writeError(w, 503, "round observations are temporarily unavailable")
			}
			return
		}
		body = encoded.body
	}
	context := s.investigationContext()
	extra := map[string]any{"context": context}
	if capture {
		extra["schemaVersion"], extra["serverEpoch"] = 1, s.hub.epoch
		extra["revision"] = fmt.Sprintf("%d:%d", entry.evidence, version.Catalogue)
		extra["capturedAt"] = entry.capturedAt
	}
	// The large, shared evidence body is already valid JSON. Only the small
	// context is encoded per request; do not rescan the report via RawMessage.
	suffix, err := json.Marshal(extra)
	if err != nil {
		writeError(w, 500, "unable to encode observation context")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body[:len(body)-1])
	_, _ = w.Write([]byte(","))
	_, _ = w.Write(suffix[1:])
	if capture {
		cache.record("capture_seconds", time.Since(started).Seconds())
	}
}

func (s *Server) investigationContext() investigationContext {
	snapshot := s.opts.State.Snapshot()
	context := investigationContext{
		ActiveHeight: snapshot.Height, ActiveRound: snapshot.Round,
		CommittedHeight: snapshot.LastCommittedHeight, Health: snapshot.Health,
		GeneratedAt: time.Now().UTC(), Source: "Observed votes from the configured RPC; not a complete network record.",
	}
	if snapshot.NodeStatus != nil {
		context.ChainID = snapshot.NodeStatus.Network
	}
	if snapshot.Upgrade != nil {
		context.Upgrade = map[string]any{"name": snapshot.Upgrade.Name, "height": snapshot.Upgrade.Height}
	}
	return context
}

package web

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

func newServerEpoch() string {
	var buf [16]byte
	// Process identity must never be silently reused across restarts. The Go
	// cryptographic random source fails fatally if the OS cannot supply entropy.
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func (s *Server) handleSession(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"schemaVersion": 1, "serverEpoch": s.hub.epoch,
		"capabilities": s.hub.capabilities,
		"cadence":      map[string]int{"snapshotMs": 1000, "activePollMs": 1000, "settledPollMs": 5000},
	})
}

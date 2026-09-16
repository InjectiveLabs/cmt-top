//go:build !webui

package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultBuildExplainsMissingWebBundle(t *testing.T) {
	w := httptest.NewRecorder()
	(&Server{}).spa().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("default Go build served status %d, want 503", w.Code)
	}
	if !strings.Contains(strings.ToLower(w.Body.String()), "web") {
		t.Fatalf("missing-bundle response provides no useful explanation: %s", w.Body.String())
	}
}

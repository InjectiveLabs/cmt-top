package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func retryAdmission(code int, err error) bool {
	return err != nil && code == 0 || code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable
}

// Browsers retry a failed probe or handshake. Keep recovery bounded and visible
// in JSON, so simultaneous TCP backlog pressure is not mistaken for an app's
// steady-state client limit, nor silently omitted from admission measurements.
func (r *run) admit(ctx context.Context, id int) (*websocket.Conn, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var last error
	var retryAfter time.Duration
	for attempt := 0; attempt < 8; attempt++ {
		if attempt > 0 {
			r.mu.Lock()
			r.s.AdmissionRetries++
			r.mu.Unlock()
			delay := min(2*time.Second, 250*time.Millisecond*time.Duration(1<<uint(attempt-1))) + time.Duration(id%113)*time.Millisecond
			delay = max(delay, retryAfter)
			if !wait(ctx, delay) {
				break
			}
		}
		// Capable clients use the small session endpoint and receive the full
		// authoritative state only over WS. Old servers support state fallback.
		bootstrap := "/api/session"
		if r.legacyBootstrap {
			bootstrap = "/api/state"
		}
		code, body, headers, err := r.requestWithHeaders(ctx, bootstrap, "")
		if err == nil && code == http.StatusNotFound && !r.legacyBootstrap {
			code, body, headers, err = r.requestWithHeaders(ctx, "/api/state", "")
		}
		if err != nil || code != http.StatusOK {
			last = fmt.Errorf("bootstrap status=%d err=%v", code, err)
			if retryAdmission(code, err) {
				r.recordAdmissionTransient()
				retryAfter = parseRetryAfter(headers.Get("Retry-After"), time.Now())
				continue
			}
			return nil, 0, last
		}
		var baseline struct {
			Height int64 `json:"height"`
		}
		if err := json.Unmarshal(body, &baseline); err != nil {
			return nil, 0, fmt.Errorf("bootstrap decode: %w", err)
		}
		dialer := websocket.Dialer{HandshakeTimeout: 8 * time.Second, Proxy: nil}
		conn, resp, err := dialer.DialContext(ctx, "ws"+strings.TrimPrefix(r.target, "http")+"/ws", nil)
		if err == nil {
			return conn, baseline.Height, nil
		}
		status := 0
		retryAfter = 0
		if resp != nil {
			status = resp.StatusCode
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
			_ = resp.Body.Close()
		}
		last = fmt.Errorf("handshake status=%d err=%v", status, err)
		if !retryAdmission(status, err) {
			return nil, 0, last
		}
		r.recordAdmissionTransient()
	}
	if ctx.Err() != nil {
		return nil, 0, fmt.Errorf("admission deadline: %w (last failure: %v)", ctx.Err(), last)
	}
	return nil, 0, last
}

func (r *run) recordAdmissionTransient() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.s.AdmissionTransientErrors++
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}

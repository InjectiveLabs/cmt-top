package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-chi/httprate"
)

// clientIP trusts X-Forwarded-For only when the socket peer is explicitly
// trusted, walking from right to left until the first untrusted hop. Invalid
// forwarding input falls back to the socket peer, never a supplied identity.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	peer = peer.Unmap()
	isTrusted := func(ip netip.Addr) bool {
		for _, prefix := range trusted {
			if prefix.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !isTrusted(peer) {
		return quotaIP(peer)
	}
	raw := r.Header.Get("X-Forwarded-For")
	if raw == "" || len(raw) > 2048 {
		return quotaIP(peer)
	}
	hops := strings.Split(raw, ",")
	if len(hops) > 32 {
		return quotaIP(peer)
	}
	current := peer
	for i := len(hops) - 1; i >= 0; i-- {
		if !isTrusted(current) {
			break
		}
		next, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return quotaIP(peer)
		}
		current = next.Unmap()
	}
	return quotaIP(current)
}

func quotaIP(ip netip.Addr) string {
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return ip.String()
}

func (s *Server) requestBudget() func(http.Handler) http.Handler {
	var trusted []netip.Prefix
	for _, cidr := range s.opts.TrustedProxies {
		if prefix, err := netip.ParsePrefix(cidr); err == nil {
			trusted = append(trusted, prefix.Masked())
		}
	}
	key := func(r *http.Request) (string, error) { return clientIP(r, trusted), nil }
	limited := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "request budget exceeded; retry shortly")
	}
	// Separate cheap session admission probes from normal API reads so that a
	// report burst cannot prevent clients from diagnosing authentication/retry.
	normal := httprate.Limit(s.opts.APIRateLimit, time.Second, httprate.WithKeyFuncs(key), httprate.WithLimitHandler(limited))
	probe := httprate.Limit(s.opts.APIRateLimit, time.Second, httprate.WithKeyFuncs(key), httprate.WithLimitHandler(limited))
	globalNormal := httprate.Limit(s.opts.APIRateLimit, time.Second, httprate.WithLimitHandler(limited))
	globalProbe := httprate.Limit(s.opts.APIRateLimit, time.Second, httprate.WithLimitHandler(limited))
	return func(next http.Handler) http.Handler {
		normalHandler, probeHandler := globalNormal(normal(next)), globalProbe(probe(next))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/session" {
				probeHandler.ServeHTTP(w, r)
			} else {
				normalHandler.ServeHTTP(w, r)
			}
		})
	}
}

// Handshakes are also bounded before allocating a reserved connection slot.
// The separate global budget includes rejected/authenticated attempts but does
// not consume API probe or investigation read quotas.
func (s *Server) handshakeBudget() func(http.Handler) http.Handler {
	return httprate.Limit(s.opts.APIRateLimit, time.Second, httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "connection attempt budget exceeded; retry shortly")
	}))
}

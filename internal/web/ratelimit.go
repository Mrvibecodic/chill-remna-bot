package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// rateLimiter is a small per-key sliding-window limiter used to throttle the
// internet-facing cabinet auth endpoints (brute force + registration spam).
type rateLimiter struct {
	mu        sync.Mutex
	hits      map[string][]time.Time
	max       int
	window    time.Duration
	lastClean time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{hits: map[string][]time.Time{}, max: max, window: window, lastClean: time.Now()}
}

func (rl *rateLimiter) allow(key string) bool {
	now := time.Now()
	cut := now.Add(-rl.window)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if now.Sub(rl.lastClean) > rl.window {
		for k, ts := range rl.hits {
			if len(ts) == 0 || ts[len(ts)-1].Before(cut) {
				delete(rl.hits, k)
			}
		}
		rl.lastClean = now
	}
	ts := rl.hits[key]
	j := 0
	for _, t := range ts {
		if t.After(cut) {
			ts[j] = t
			j++
		}
	}
	ts = ts[:j]
	if len(ts) >= rl.max {
		rl.hits[key] = ts
		return false
	}
	rl.hits[key] = append(ts, now)
	return true
}

// clientIP returns the best-effort client IP, honoring a reverse proxy's
// forwarding headers (the bot is commonly behind nginx/Cloudflare). Forwarded
// values are only accepted if they parse to a real, globally-routable unicast
// address, so a spoofed or garbage header (e.g. a multicast 232.x.x.x) is
// ignored and we fall back to the real TCP peer.
func clientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}
	for _, h := range []string{"CF-Connecting-IP", "X-Real-IP", "X-Forwarded-For"} {
		v := r.Header.Get(h)
		if v == "" {
			continue
		}
		if i := strings.IndexByte(v, ','); i > 0 {
			v = v[:i]
		}
		if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() {
			return ip.String()
		}
	}
	return peer
}

// isSecure reports whether the request reached us over HTTPS, directly or via a
// TLS-terminating reverse proxy.
func isSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// setSecurityHeaders applies baseline hardening headers. frameDeny is used for
// the cabinet (clickjacking protection); the Mini App is intentionally framable
// by Telegram, so it is not set there. HSTS is only meaningful over TLS.
func (s *Server) setSecurityHeaders(w http.ResponseWriter, frameDeny bool) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if frameDeny {
		h.Set("X-Frame-Options", "DENY")
	}
	if s.domain != "" {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

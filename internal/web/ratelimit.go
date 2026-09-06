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
// peerAddr — адрес TCP-пира без порта.
func peerAddr(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// fromTrustedProxy — запрос пришёл от локального реверс-прокси, а не напрямую
// из интернета. Только такому пиру можно верить в служебных заголовках:
// прямой клиент подставит в них что угодно.
func fromTrustedProxy(r *http.Request) bool {
	p := net.ParseIP(peerAddr(r))
	return p != nil && (p.IsLoopback() || p.IsPrivate())
}

func clientIP(r *http.Request) string {
	peer := peerAddr(r)
	if !fromTrustedProxy(r) {
		return peer
	}
	// Цепочка пересылки читается СПРАВА НАЛЕВО. Рекомендованный нами конфиг
	// nginx использует $proxy_add_x_forwarded_for, который дописывает реальный
	// адрес в КОНЕЦ строки, присланной клиентом. Значит первый элемент пишет
	// сам клиент — и, беря его, лимитер получал ключ, полностью подконтрольный
	// атакующему: перебор паролей кабинета шёл без всякого ограничения.
	//
	// Идём с конца, пропуская адреса своих прокси (частные и loopback), и
	// берём первый недоверенный — это и есть клиент.
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		parts := strings.Split(v, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(parts[i]))
			if ip == nil {
				continue
			}
			if ip.IsLoopback() || ip.IsPrivate() {
				continue // свой прокси, идём левее
			}
			if ip.IsGlobalUnicast() {
				return ip.String()
			}
		}
	}
	// X-Real-IP и CF-Connecting-IP — одиночные значения, подделать цепочкой их
	// нельзя, но наш пример конфига их не выставляет и не вырезает. Поэтому они
	// читаются ПОСЛЕ цепочки, а не вперёд неё.
	for _, h := range []string{"X-Real-IP", "CF-Connecting-IP"} {
		v := strings.TrimSpace(r.Header.Get(h))
		if v == "" {
			continue
		}
		if ip := net.ParseIP(v); ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() {
			return ip.String()
		}
	}
	return peer
}

// isSecure reports whether the request reached us over HTTPS, directly or via a
// TLS-terminating reverse proxy.
//
// Заголовку верим только от локального прокси. Раньше он принимался от кого
// угодно, и одна строка в запросе снимала и редирект на HTTPS, и отказ 426:
// клиент добровольно отдавал пароль и ключ доступа в открытый канал, а сканер
// обходил единственное препятствие. Адрес клиента рядом определяется строго —
// эта асимметрия и была дырой.
func isSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !fromTrustedProxy(r) {
		return false
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// setSecurityHeaders applies baseline hardening headers. frameDeny is used for
// the cabinet (clickjacking protection); the Mini App is intentionally framable
// by Telegram, so it is not set there. HSTS is only meaningful over TLS.
func (s *Server) setSecurityHeaders(w http.ResponseWriter, frameDeny, secure bool) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow, noarchive, nosnippet")
	if frameDeny {
		h.Set("X-Frame-Options", "DENY")
	}
	if secure {
		// Раньше условием был режим autocert, поэтому за прокси HSTS не
		// выдавался никогда — даже когда соединение честно защищено.
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
}

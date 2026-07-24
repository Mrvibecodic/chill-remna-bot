package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cabinetJWTTTL is the lifetime of a web-cabinet session token. Longer than the
// Mini App's (the cabinet is a normal site, not re-opened from a chat).
const cabinetJWTTTL = 7 * 24 * time.Hour

// loginTTL bounds how old Telegram Login Widget data may be (anti-replay).
const loginTTL = 24 * time.Hour

// validateTelegramLogin verifies Telegram Login Widget data per the official
// algorithm: secret = SHA256(botToken); the data-check-string is every provided
// field except "hash", sorted by key as key=value joined by '\n'; the hex HMAC
// of that with secret must equal "hash". Returns the authenticated Telegram id.
func validateTelegramLogin(fields map[string]string, botToken string, ttl time.Duration) (int64, error) {
	hash := fields["hash"]
	if hash == "" || botToken == "" {
		return 0, errAuth
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if k == "hash" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(fields[k])
	}
	secret := sha256.Sum256([]byte(botToken))
	want := hex.EncodeToString(hmacSHA256(secret[:], []byte(b.String())))
	if !hmac.Equal([]byte(want), []byte(hash)) {
		return 0, errAuth
	}
	if ttl > 0 {
		ad, err := strconv.ParseInt(fields["auth_date"], 10, 64)
		if err != nil || ad <= 0 || time.Since(time.Unix(ad, 0)) > ttl {
			return 0, errAuth
		}
	}
	id, err := strconv.ParseInt(fields["id"], 10, 64)
	if err != nil || id == 0 {
		return 0, errAuth
	}
	return id, nil
}

// handleFlag serves a self-hosted country-flag SVG (see privacy F1: flags are
// fetched once at startup and served from this server, never from a third-party
// CDN at the visitor's browser).
func (s *Server) handleFlag(w http.ResponseWriter, r *http.Request) {
	if s.mini == nil {
		http.NotFound(w, r)
		return
	}
	code := strings.TrimSuffix(r.PathValue("code"), ".svg")
	b, ok := s.mini.CabinetFlag(code)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	_, _ = w.Write(b)
}

// handleRobots blocks search engines/crawlers across the whole bot domain
// (webhooks, mini-app, cabinet) — none of it should be indexable.
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

func (s *Server) cabinetOK() bool { return s.mini != nil && s.mini.CabinetEnabled() }

// requireHTTPS makes sure the cabinet is never served over plain HTTP. Static
// GETs are redirected to https; API calls are refused.
func (s *Server) requireHTTPS(w http.ResponseWriter, r *http.Request, api bool) bool {
	if isSecure(r) {
		return true
	}
	if api || r.Method != http.MethodGet {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "требуется HTTPS"})
		return false
	}
	http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusPermanentRedirect)
	return false
}

// authThrottled rate-limits the internet-facing auth endpoints per client IP.
func (s *Server) authThrottled(w http.ResponseWriter, r *http.Request) bool {
	if s.authLimiter != nil && !s.authLimiter.allow(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "слишком много попыток, попробуйте позже"})
		return true
	}
	return false
}

func (s *Server) issueCabinetToken(w http.ResponseWriter, tgID int64) {
	tok := issueJWT(tgID, true, jwtKey(s.mini.MiniBotToken()), cabinetJWTTTL)
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires_in": int(cabinetJWTTTL.Seconds())})
}

// handleCabinetConfig returns the public info the cabinet login page needs.
func (s *Server) handleCabinetConfig(w http.ResponseWriter, r *http.Request) {
	if !s.cabinetOK() {
		http.NotFound(w, r)
		return
	}
	if !s.requireHTTPS(w, r, true) {
		return
	}
	s.setSecurityHeaders(w, true)
	writeJSON(w, http.StatusOK, map[string]any{"bot_username": s.mini.CabinetBotUsername()})
}

// handleCabinetTelegramAuth exchanges Telegram Login Widget data for a token.
func (s *Server) handleCabinetTelegramAuth(w http.ResponseWriter, r *http.Request) {
	if !s.cabinetOK() {
		http.NotFound(w, r)
		return
	}
	if !s.requireHTTPS(w, r, true) {
		return
	}
	s.setSecurityHeaders(w, true)
	if s.authThrottled(w, r) {
		return
	}

	body, err := readAllLimited(r, 16*1024)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req struct {
		ID        int64  `json:"id"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Username  string `json:"username"`
		PhotoURL  string `json:"photo_url"`
		AuthDate  int64  `json:"auth_date"`
		Hash      string `json:"hash"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	fields := map[string]string{
		"id":        strconv.FormatInt(req.ID, 10),
		"auth_date": strconv.FormatInt(req.AuthDate, 10),
		"hash":      req.Hash,
	}
	for k, v := range map[string]string{"first_name": req.FirstName, "last_name": req.LastName, "username": req.Username, "photo_url": req.PhotoURL} {
		if v != "" {
			fields[k] = v
		}
	}
	tgID, err := validateTelegramLogin(fields, s.mini.MiniBotToken(), loginTTL)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "не удалось проверить вход через Telegram"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	s.mini.CabinetEnsureUser(ctx, tgID)
	if err := s.mini.CabinetGate(ctx, tgID, false); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	s.issueCabinetToken(w, tgID)
}

func (s *Server) cabinetEmail(w http.ResponseWriter, r *http.Request, register bool) {
	if !s.cabinetOK() {
		http.NotFound(w, r)
		return
	}
	if !s.requireHTTPS(w, r, true) {
		return
	}
	s.setSecurityHeaders(w, true)
	if s.authThrottled(w, r) {
		return
	}
	body, err := readAllLimited(r, 8*1024)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	var id int64
	if register {
		id, err = s.mini.CabinetEmailRegister(ctx, req.Email, req.Password)
	} else {
		id, err = s.mini.CabinetEmailLogin(ctx, req.Email, req.Password)
	}
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	s.issueCabinetToken(w, id)
}

func (s *Server) handleCabinetRegister(w http.ResponseWriter, r *http.Request) {
	s.cabinetEmail(w, r, true)
}
func (s *Server) handleCabinetLogin(w http.ResponseWriter, r *http.Request) {
	s.cabinetEmail(w, r, false)
}

// handleCabinetP2PScreenshot accepts a payment screenshot uploaded from the web
// cabinet (multipart) and forwards it to the admin for approval.
func (s *Server) handleCabinetP2PScreenshot(w http.ResponseWriter, r *http.Request) {
	id, web, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	s.setSecurityHeaders(w, true)
	if !web {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "только для веб-кабинета"})
		return
	}
	// Hard-cap the whole request body so an oversized upload can't spool large
	// temp files to disk (ParseMultipartForm's argument is only the in-memory
	// threshold, not a total limit).
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	reqID, _ := strconv.ParseInt(r.FormValue("req_id"), 10, 64)
	file, hdr, err := r.FormFile("photo")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "файл не загружен"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 8<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.mini.CabinetP2PScreenshot(ctx, id, reqID, hdr.Filename, data); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCabinetStatic serves the cabinet SPA at the live-configured path. It is
// registered as the catch-all so the path can change without a restart.
func (s *Server) handleCabinetStatic(w http.ResponseWriter, r *http.Request) {
	if !s.cabinetOK() {
		http.NotFound(w, r)
		return
	}
	if !s.requireHTTPS(w, r, false) {
		return
	}
	s.setSecurityHeaders(w, true)
	p := s.mini.CabinetPath()
	if !strings.HasPrefix(r.URL.Path, p) {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, p)
	if rel == "" || rel == "index.html" {
		s.serveCabinetHTML(w)
		return
	}
	fsys, err := s.staticFS()
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	http.StripPrefix(p, http.FileServer(http.FS(fsys))).ServeHTTP(w, r)
}

var fpAlphabet = []byte("abcdefghijklmnopqrstuvwxyz0123456789")

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Cosmetic (anti-fingerprint) use only; on the rare read error fall back
		// to a deterministic pad rather than failing the request.
		for i := range b {
			b[i] = fpAlphabet[i%len(fpAlphabet)]
		}
		return string(b)
	}
	for i := range b {
		b[i] = fpAlphabet[int(b[i])%len(fpAlphabet)]
	}
	return string(b)
}

// serveCabinetHTML serves the SPA with the configured title/description/favicon
// injected, and (in anti-fingerprint mode) randomized markers so the page is
// harder to identify as this bot's cabinet.
func (s *Server) serveCabinetHTML(w http.ResponseWriter) {
	data, err := s.readIndexHTML("cabinet.html")
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	out := string(data)
	antifp := s.mini.CabinetAntiFP()

	// Title drives BOTH the browser tab (<title>) and the visible page heading.
	// Description is ONLY a <meta> tag — it never alters a page element.
	title := s.mini.CabinetTitle()
	tabTitle := title // <title> text
	heading := title  // visible "Личный кабинет" heading text
	if title == "" {
		if antifp {
			r := randToken(6)
			tabTitle, heading = r, r
		} else {
			tabTitle = "Кабинет"
			heading = "Личный кабинет"
		}
	}
	out = strings.Replace(out, "<title>Кабинет</title>", "<title>"+html.EscapeString(tabTitle)+"</title>", 1)
	// Always reflect the configured title in the visible heading (login screen +
	// header), regardless of anti-fingerprint mode.
	out = strings.ReplaceAll(out, "Личный кабинет", html.EscapeString(heading))

	head := ""
	if d := s.mini.CabinetDescription(); d != "" {
		head += "<meta name=\"description\" content=\"" + html.EscapeString(d) + "\">\n"
	}
	if fav := s.mini.CabinetFavicon(); fav != "" {
		head += "<link rel=\"icon\" href=\"" + html.EscapeString(fav) + "\">\n"
	}
	if antifp {
		// vary the served bytes per request to avoid a static byte-for-byte fingerprint
		head += "<!-- " + randToken(16) + " -->\n"
	}
	if head != "" {
		out = strings.Replace(out, "<title>", head+"<title>", 1)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if antifp {
		w.Header().Set("Cache-Control", "no-store")
	}
	_, _ = w.Write([]byte(out))
}

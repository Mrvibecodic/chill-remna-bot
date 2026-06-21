package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
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
	sub, err := fs.Sub(miniStaticFS, "miniapp_static")
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	http.StripPrefix(p, http.FileServer(http.FS(sub))).ServeHTTP(w, r)
}

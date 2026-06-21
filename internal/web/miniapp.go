package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// initDataTTL bounds how old Telegram init data may be (anti-replay).
const initDataTTL = 24 * time.Hour

// jwtTTL is the lifetime of a Mini App session token.
const jwtTTL = 30 * time.Minute

// MiniProvider exposes the bot's existing data to the Mini App API as thin,
// read-mostly DTOs. The Mini App holds NO business logic of its own: every
// value here mirrors what the chat bot already computes/shows, so the two can
// never drift. Implemented by *app.App.
type MiniProvider interface {
	// MiniEnabled reports whether the Mini App feature flag is on.
	MiniEnabled() bool
	// MiniBotToken returns the Telegram bot token (for init-data validation).
	MiniBotToken() string

	MiniMe(ctx context.Context, tgID int64) MiniMeDTO
	MiniMenu(ctx context.Context, tgID int64) MiniMenuDTO
	MiniSubscription(ctx context.Context, tgID int64) MiniSubDTO
	MiniPlans(ctx context.Context, tgID int64) MiniPlansDTO

	// MiniTrial activates the free trial (mirrors the chat trial flow).
	MiniTrial(ctx context.Context, tgID int64) MiniActionDTO
	// MiniCheckout performs an in-app purchase for the given period+method.
	// Currently only the "balance" method completes in-app; others set
	// Redirect=true so the front-end points the user to the bot.
	MiniCheckout(ctx context.Context, tgID int64, months int, method string) MiniActionDTO
}

// MiniActionDTO is the result of an action (trial/checkout): on success it
// carries the fresh subscription link + expiry; otherwise an error message or
// a Redirect hint (use the bot for this payment method).
type MiniActionDTO struct {
	OK       bool   `json:"ok"`
	SubURL   string `json:"sub_url,omitempty"`
	ExpireAt string `json:"expire_at,omitempty"`
	Error    string `json:"error,omitempty"`
	Redirect bool   `json:"redirect,omitempty"`
}

type MiniMeDTO struct {
	TgID     int64  `json:"tg_id"`
	Lang     string `json:"lang"`
	BalanceK int64  `json:"balance_kopecks"`
}

// MiniMenuDTO mirrors navRow predicates so the front-end shows exactly the
// actions the chat bot would show this user.
type MiniMenuDTO struct {
	HasSub         bool     `json:"has_sub"`
	CanRenew       bool     `json:"can_renew"`
	TrialAvailable bool     `json:"trial_available"`
	ReferralOn     bool     `json:"referral_on"`
	PayMethods     []string `json:"pay_methods"`
	SupportURL     string   `json:"support_url"`
	GroupURL       string   `json:"group_url"`
}

type MiniSubDTO struct {
	Active      bool   `json:"active"`
	Status      string `json:"status"`
	ExpireAt    string `json:"expire_at"`
	SubURL      string `json:"sub_url"`
	DevicesUsed int    `json:"devices_used"`
	DeviceLimit int    `json:"device_limit"`
	HasLimit    bool   `json:"has_limit"`
	DevicesOK   bool   `json:"devices_ok"`
}

type MiniPlanDTO struct {
	Months    int    `json:"months"`
	Price     string `json:"price"`
	Currency  string `json:"currency"`
	TrafficGB int    `json:"traffic_gb"`
	Devices   int    `json:"devices"`
}

type MiniPlansDTO struct {
	Plans []MiniPlanDTO `json:"plans"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// requireAuth extracts and verifies the Bearer JWT, returning the Telegram id.
func (s *Server) miniAuth(r *http.Request) (int64, bool) {
	if s.mini == nil {
		return 0, false
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return 0, false
	}
	id, err := parseJWT(strings.TrimPrefix(h, "Bearer "), jwtKey(s.mini.MiniBotToken()))
	if err != nil {
		return 0, false
	}
	return id, true
}

// handleMiniAuth exchanges Telegram init data for a short-lived JWT.
func (s *Server) handleMiniAuth(w http.ResponseWriter, r *http.Request) {
	if s.mini == nil || !s.mini.MiniEnabled() {
		http.NotFound(w, r)
		return
	}
	body, err := readAllLimited(r, 16*1024)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req struct {
		InitData string `json:"init_data"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tgID, err := validateInitData(req.InitData, s.mini.MiniBotToken(), initDataTTL)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	tok := issueJWT(tgID, jwtKey(s.mini.MiniBotToken()), jwtTTL)
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires_in": int(jwtTTL.Seconds())})
}

func (s *Server) miniGuard(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if s.mini == nil || !s.mini.MiniEnabled() {
		http.NotFound(w, r)
		return 0, false
	}
	id, ok := s.miniAuth(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return 0, false
	}
	return id, true
}

func (s *Server) handleMiniMe(w http.ResponseWriter, r *http.Request) {
	id, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniMe(ctx, id))
}

func (s *Server) handleMiniMenu(w http.ResponseWriter, r *http.Request) {
	id, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniMenu(ctx, id))
}

func (s *Server) handleMiniSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniSubscription(ctx, id))
}

func (s *Server) handleMiniPlans(w http.ResponseWriter, r *http.Request) {
	id, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniPlans(ctx, id))
}

func (s *Server) handleMiniTrial(w http.ResponseWriter, r *http.Request) {
	id, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniTrial(ctx, id))
}

func (s *Server) handleMiniCheckout(w http.ResponseWriter, r *http.Request) {
	id, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	body, err := readAllLimited(r, 8*1024)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req struct {
		Months int    `json:"months"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniCheckout(ctx, id, req.Months, req.Method))
}

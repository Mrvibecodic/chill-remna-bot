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
	MiniMenu(ctx context.Context, tgID int64, web bool) MiniMenuDTO
	MiniSubscription(ctx context.Context, tgID int64) MiniSubDTO
	MiniPlans(ctx context.Context, tgID int64) MiniPlansDTO

	// MiniTrial activates the free trial (mirrors the chat trial flow).
	MiniTrial(ctx context.Context, tgID int64) MiniActionDTO
	// MiniCheckout performs an in-app purchase for the given period+method.
	MiniCheckout(ctx context.Context, tgID int64, months int, method string, web bool) MiniActionDTO

	// MiniReferral returns the user's referral info (mirrors showReferral).
	MiniReferral(ctx context.Context, tgID int64) MiniReferralDTO
	// MiniPromo applies a promo code (mirrors the chat promo flow).
	MiniPromo(ctx context.Context, tgID int64, code string) MiniPromoDTO
	// MiniTopUpOptions returns preset top-up amounts + enabled methods.
	MiniTopUpOptions(ctx context.Context, tgID int64) MiniTopUpOptionsDTO
	// MiniTopUp creates a balance top-up payment (yk/cb) and returns the URL.
	MiniTopUp(ctx context.Context, tgID int64, kopecks int64, method string) MiniActionDTO

	// MiniConnect returns install apps + deeplinks for the user's subscription,
	// sourced from their subscription page (iOS + Android only).
	MiniConnect(ctx context.Context, tgID int64) MiniConnectDTO

	// MiniResetDevices rotates the user's credentials and clears all their HWID
	// devices (mirrors the chat "reset devices" flow). Available in the cabinet too.
	MiniResetDevices(ctx context.Context, tgID int64) MiniActionDTO

	// --- web cabinet (browser site; reuses everything above except trial) ---
	CabinetEnabled() bool
	CabinetPath() string
	CabinetBotUsername() string
	CabinetTitle() string
	CabinetDescription() string
	CabinetFavicon() string
	CabinetAntiFP() bool
	CabinetEnsureUser(ctx context.Context, tgID int64)
	CabinetEmailRegister(ctx context.Context, email, password string) (int64, error)
	CabinetEmailLogin(ctx context.Context, email, password string) (int64, error)
	CabinetGate(ctx context.Context, tgID int64, isEmail bool) error
	CabinetP2PScreenshot(ctx context.Context, tgID, reqID int64, filename string, data []byte) error
	// MiniBlocked reports whether the user is blocked by an admin.
	MiniBlocked(ctx context.Context, tgID int64) bool
	// CabinetFlag returns a self-hosted country-flag SVG by ISO code.
	CabinetFlag(code string) ([]byte, bool)
}

type MiniReferralDTO struct {
	Enabled       bool   `json:"enabled"`
	Link          string `json:"link,omitempty"`
	Count         int    `json:"count"`
	BonusValue    int    `json:"bonus_value"`
	BonusKind     string `json:"bonus_kind"`
	OnFirstPay    bool   `json:"on_first_pay"`
	EarnedKopecks int64  `json:"earned_kopecks"`
	InviteeKind   string `json:"invitee_kind,omitempty"`
	InviteeValue  int    `json:"invitee_value"`
	Percent       int    `json:"percent"`
}

type MiniPromoDTO struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type MiniAmountDTO struct {
	Kopecks int64  `json:"kopecks"`
	Label   string `json:"label"`
}

type MiniTopUpOptionsDTO struct {
	Amounts []MiniAmountDTO `json:"amounts"`
	Methods []string        `json:"methods"`
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
	// Message is a human-readable note shown on a redirect (e.g. P2P -> bot).
	Message string `json:"message,omitempty"`
	// PayURL is set for external methods: a payment page/redirect URL, or a
	// Telegram invoice link when Invoice=true (front opens it via openInvoice).
	PayURL  string `json:"pay_url,omitempty"`
	Invoice bool   `json:"invoice,omitempty"`
	// P2P web flow: the card to transfer to + the request id the cabinet uploads
	// a payment screenshot against.
	P2PCard   string `json:"p2p_card,omitempty"`
	P2PAmount string `json:"p2p_amount,omitempty"`
	P2PReqID  int64  `json:"p2p_req_id,omitempty"`
}

type MiniMeDTO struct {
	TgID     int64  `json:"tg_id"`
	Lang     string `json:"lang"`
	BalanceK int64  `json:"balance_kopecks"`
	Name     string `json:"name,omitempty"`
	Email    string `json:"email,omitempty"`
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
	ExpireTS    int64  `json:"expire_ts,omitempty"`
	SubURL      string `json:"sub_url"`
	DevicesUsed int    `json:"devices_used"`
	DeviceLimit int    `json:"device_limit"`
	HasLimit    bool   `json:"has_limit"`
	DevicesOK   bool   `json:"devices_ok"`
}

type MiniPlanDTO struct {
	Months   int    `json:"months"`
	Price    string `json:"price"`
	Currency string `json:"currency"`
	// TrafficGB is the plan's traffic allowance in GB; 0 means unlimited.
	TrafficGB int `json:"traffic_gb"`
	// Devices is the plan's HWID device limit; 0 means no explicit limit
	// (panel default).
	Devices int `json:"devices"`
	// Countries are the distinct countries available to the plan's squad,
	// deduped. Each carries a flag emoji, ISO code (for image flags), and name.
	Countries []MiniCountryDTO `json:"countries,omitempty"`
	// Configs is the number of inbounds (configs) accessible to the plan.
	Configs int `json:"configs,omitempty"`
}

// MiniCountryDTO is one destination country available to a plan.
type MiniCountryDTO struct {
	Flag string `json:"flag,omitempty"`
	Code string `json:"code,omitempty"`
	Name string `json:"name,omitempty"`
}

type MiniPlansDTO struct {
	Plans []MiniPlanDTO `json:"plans"`
	// Strategy is the traffic reset strategy shared by all plans
	// (NO_RESET/DAY/WEEK/MONTH/MONTH_ROLLING).
	Strategy string `json:"strategy,omitempty"`
}

// MiniConnectButtonDTO is one install button (store link) for an app.
type MiniConnectButtonDTO struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// MiniConnectAppDTO is one VPN client app the user can install and import the
// subscription into via its deeplink.
type MiniConnectAppDTO struct {
	ID       string                 `json:"id"`
	Name     string                 `json:"name"`
	Featured bool                   `json:"featured,omitempty"`
	Deeplink string                 `json:"deeplink,omitempty"`
	AddDesc  string                 `json:"add_desc,omitempty"`
	Installs []MiniConnectButtonDTO `json:"installs,omitempty"`
}

// MiniConnectDTO carries the subscription URL plus the iOS/Android app lists.
type MiniConnectPlatformDTO struct {
	Key   string              `json:"key"`
	Label string              `json:"label"`
	Apps  []MiniConnectAppDTO `json:"apps"`
}

type MiniConnectDTO struct {
	SubURL    string                   `json:"sub_url,omitempty"`
	Username  string                   `json:"username,omitempty"`
	Platforms []MiniConnectPlatformDTO `json:"platforms,omitempty"`
	Android   []MiniConnectAppDTO      `json:"android,omitempty"`
	IOS       []MiniConnectAppDTO      `json:"ios,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// requireAuth extracts and verifies the Bearer JWT, returning the Telegram id.
func (s *Server) miniAuth(r *http.Request) (id int64, web bool, ok bool) {
	if s.mini == nil {
		return 0, false, false
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return 0, false, false
	}
	id, web, err := parseJWT(strings.TrimPrefix(h, "Bearer "), jwtKey(s.mini.MiniBotToken()))
	if err != nil {
		return 0, false, false
	}
	return id, web, true
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
	tok := issueJWT(tgID, false, jwtKey(s.mini.MiniBotToken()), jwtTTL)
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "expires_in": int(jwtTTL.Seconds())})
}

func (s *Server) miniGuard(w http.ResponseWriter, r *http.Request) (id int64, web bool, ok bool) {
	if s.mini == nil || (!s.mini.MiniEnabled() && !s.mini.CabinetEnabled()) {
		http.NotFound(w, r)
		return 0, false, false
	}
	id, web, ok = s.miniAuth(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return 0, false, false
	}
	if s.mini.MiniBlocked(r.Context(), id) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "доступ заблокирован"})
		return 0, false, false
	}
	return id, web, true
}

func (s *Server) handleMiniMe(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniMe(ctx, id))
}

func (s *Server) handleMiniMenu(w http.ResponseWriter, r *http.Request) {
	id, web, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniMenu(ctx, id, web))
}

func (s *Server) handleMiniSubscription(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniSubscription(ctx, id))
}

func (s *Server) handleMiniPlans(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniPlans(ctx, id))
}

func (s *Server) handleMiniTrial(w http.ResponseWriter, r *http.Request) {
	id, web, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	if web {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "триал недоступен в веб-кабинете"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniTrial(ctx, id))
}

func (s *Server) handleMiniCheckout(w http.ResponseWriter, r *http.Request) {
	id, web, ok := s.miniGuard(w, r)
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
	writeJSON(w, http.StatusOK, s.mini.MiniCheckout(ctx, id, req.Months, req.Method, web))
}

func (s *Server) handleMiniReferral(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniReferral(ctx, id))
}

func (s *Server) handleMiniPromo(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	body, err := readAllLimited(r, 4*1024)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniPromo(ctx, id, req.Code))
}

func (s *Server) handleMiniTopUpOptions(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniTopUpOptions(ctx, id))
}

func (s *Server) handleMiniTopUp(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	body, err := readAllLimited(r, 4*1024)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req struct {
		Kopecks int64  `json:"kopecks"`
		Method  string `json:"method"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniTopUp(ctx, id, req.Kopecks, req.Method))
}

func (s *Server) handleMiniConnect(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniConnect(ctx, id))
}

func (s *Server) handleMiniResetDevices(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.miniGuard(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.mini.MiniResetDevices(ctx, id))
}

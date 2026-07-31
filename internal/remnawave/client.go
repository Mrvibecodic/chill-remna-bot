package remnawave

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"remnabot/internal/model"
)

const LocalBaseURL = "http://remnawave:3000"

type APIEvent struct {
	Time       time.Time
	Method     string
	Path       string
	Status     int
	DurationMs int64
	Err        string
}

const apiLogCap = 200

type Client struct {
	base   string
	token  string
	cookie string
	apiKey string
	local  bool
	http   *http.Client

	// HWID delete-all retry tuning (0 = use defaults). Overridable in tests.
	hwidRetryBase time.Duration
	hwidRetryMax  time.Duration

	logMu sync.Mutex
	logs  []APIEvent
}

// hwidSyncAttempts is how many times ResetDevicesByTelegramID tries the HWID
// delete-all synchronously (with backoff) before giving up and letting the
// caller continue in the background. Kept small so the user isn't kept waiting.
const hwidSyncAttempts = 3

func New(cfg model.PanelConfig) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Mode == model.ModeLocal {
		base = LocalBaseURL
	}
	return &Client{
		base:   base,
		token:  cfg.APIToken,
		cookie: strings.TrimSpace(cfg.Cookie),
		apiKey: strings.TrimSpace(cfg.APIKey),
		local:  cfg.Mode == model.ModeLocal,
		http:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	if c.local {

		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		req.Header.Set("X-Forwarded-Proto", "https")
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	start := time.Now()
	resp, err := c.http.Do(req)
	ev := APIEvent{Time: start, Method: method, Path: path, DurationMs: time.Since(start).Milliseconds()}
	if err != nil {
		ev.Err = err.Error()
	} else {
		ev.Status = resp.StatusCode
	}
	c.appendLog(ev)
	return resp, err
}

func (c *Client) appendLog(ev APIEvent) {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	c.logs = append(c.logs, ev)
	if len(c.logs) > apiLogCap {
		c.logs = c.logs[len(c.logs)-apiLogCap:]
	}
}

func (c *Client) Logs() []APIEvent {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	out := make([]APIEvent, len(c.logs))
	copy(out, c.logs)
	return out
}

func (c *Client) ClearLogs() {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	c.logs = nil
}

func (c *Client) Health(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/api/system/health", nil)
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return classifyHTTP(resp)
	}
	return nil
}

func (c *Client) SystemStats(ctx context.Context) (int, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/system/stats", nil)
	if err != nil {
		return 0, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, classifyHTTP(resp)
	}
	var out struct {
		Response struct {
			Users struct {
				TotalUsers int `json:"totalUsers"`
			} `json:"users"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("разбор ответа панели: %w", err)
	}
	return out.Response.Users.TotalUsers, nil
}

type panelUser struct {
	Uuid            string `json:"uuid"`
	ExpireAt        string `json:"expireAt"`
	SubscriptionURL string `json:"subscriptionUrl"`
	Tag             string `json:"tag"`
	Username        string `json:"username"`
	TelegramID      int64  `json:"telegramId"`
	Status          string `json:"status"`

	TrafficLimitStrategy string `json:"trafficLimitStrategy"`
	HwidDeviceLimit      int    `json:"hwidDeviceLimit"`

	TrafficLimitBytes int64 `json:"trafficLimitBytes"`
	// Used traffic moved into the nested userTraffic object in the panel
	// contract; the flat field is still read as a fallback for older payloads.
	UsedTrafficBytes int64 `json:"usedTrafficBytes"`
	UserTraffic      struct {
		UsedTrafficBytes int64 `json:"usedTrafficBytes"`
	} `json:"userTraffic"`
}

// usedBytes returns the user's used traffic regardless of where the panel put
// it in the payload (nested userTraffic, or the flat legacy field).
func (u *panelUser) usedBytes() int64 {
	if u == nil {
		return 0
	}
	if u.UserTraffic.UsedTrafficBytes > 0 {
		return u.UserTraffic.UsedTrafficBytes
	}
	return u.UsedTrafficBytes
}

type PanelUser struct {
	UUID            string
	Username        string
	TelegramID      int64
	ExpireAt        string
	SubscriptionURL string
	Tag             string
	Strategy        string
	DeviceLimit     int
	Status          string
	TrafficLimit    int64
	TrafficUsed     int64
}

func toPanelUser(u *panelUser) *PanelUser {
	if u == nil || u.Uuid == "" {
		return nil
	}
	return &PanelUser{
		UUID:            u.Uuid,
		Username:        u.Username,
		TelegramID:      u.TelegramID,
		ExpireAt:        u.ExpireAt,
		SubscriptionURL: u.SubscriptionURL,
		Tag:             u.Tag,
		Strategy:        u.TrafficLimitStrategy,
		DeviceLimit:     u.HwidDeviceLimit,
		Status:          u.Status,
		TrafficLimit:    u.TrafficLimitBytes,
		TrafficUsed:     u.usedBytes(),
	}
}

const BotTag = "CHILLBOT"

func ownedByBot(u *panelUser, telegramID int64) bool {
	if u == nil || telegramID == 0 {
		return false
	}
	return u.TelegramID == telegramID || u.Username == fmt.Sprintf("tg_%d", telegramID)
}

const BotTagAdd = "CHILLBOT_ADD"

const DefaultAddSubSuffix = "_addsub"

// addSubSuffixRe keeps the configured suffix inside what the panel accepts in a
// username; anything else would make every derived name invalid, silently
// disabling auto-discovery for everyone, so it falls back to the default.
var addSubSuffixRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,20}$`)

func normalizeAddSubSuffix(suffix string) string {
	if !addSubSuffixRe.MatchString(suffix) {
		return DefaultAddSubSuffix
	}
	return suffix
}

// addSubUsername builds B's panel username from A's ACTUAL username, which is
// exactly what the subscription middleware's auto-discovery looks up ("имя B =
// полное имя A + суффикс"). For bot-created accounts A is tg_<id>, so the name
// is unchanged; for accounts adopted from the panel (linked by telegramId, any
// username) this is what makes the merge discoverable at all.
func addSubUsername(mainUsername, suffix string) string {
	return mainUsername + normalizeAddSubSuffix(suffix)
}

// legacyAddSubUsername is the name older bot builds always used, regardless of
// A's real username. Still looked up, so an existing add-on user is recognised
// as the bot's own instead of being treated as someone else's account.
func legacyAddSubUsername(telegramID int64, suffix string) string {
	return fmt.Sprintf("tg_%d%s", telegramID, normalizeAddSubSuffix(suffix))
}

// panelUsernameRe mirrors the panel's own rule for usernames (3-36 chars of
// letters, digits, underscore and dash).
var panelUsernameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,36}$`)

// addSubNames returns the name B should live under, followed by the legacy name
// to fall back on (deduped). mainUsername may be empty when A is already gone —
// then only the legacy name is known. A derived name the panel would refuse
// (A's username is long enough that the suffix pushes it over 36 chars) falls
// back to the short legacy name, so syncing keeps working instead of failing on
// every call — the merge for that user then needs a manual binding.
func addSubNames(mainUsername string, telegramID int64, suffix string) []string {
	legacy := legacyAddSubUsername(telegramID, suffix)
	if mainUsername == "" {
		return []string{legacy}
	}
	want := addSubUsername(mainUsername, suffix)
	if want == legacy || !panelUsernameRe.MatchString(want) {
		return []string{legacy}
	}
	return []string{want, legacy}
}

// findAddSub returns the bot-owned add-on user B (nil when there is none) and
// the username B should live under. A user sitting on the wanted name that is
// NOT the bot's is an error; on the legacy name it is simply ignored, since
// that name is only consulted for migration.
func (c *Client) findAddSub(ctx context.Context, mainUsername string, telegramID int64, suffix string) (*PanelUser, string, error) {
	names := addSubNames(mainUsername, telegramID, suffix)
	want := names[0]
	for i, name := range names {
		u, err := c.FindByUsername(ctx, name)
		if err != nil {
			return nil, want, err
		}
		if u == nil || u.UUID == "" {
			continue
		}
		if u.Tag != BotTagAdd {
			if i == 0 {
				return nil, want, fmt.Errorf("addsub: пользователь %s принадлежит не боту", name)
			}
			continue
		}
		return u, want, nil
	}
	return nil, want, nil
}

// expired reports whether an RFC3339 expiry is in the past. Unparsable values
// are treated as not expired, so a panel quirk never silently skips a user.
func expired(expireAt string) bool {
	t, err := time.Parse(time.RFC3339, expireAt)
	if err != nil {
		return false
	}
	return !t.After(time.Now().UTC())
}

// AddSubOptions carries everything the bot decides about the add-on user B.
type AddSubOptions struct {
	// Suffix appended to A's username to build B's ("" = "_addsub").
	Suffix string
	// TrafficBytes is B's own traffic allowance; 0 = unlimited.
	TrafficBytes int64
	// InternalSquads are B's squads (B's servers are what gets merged in).
	InternalSquads []string
	// ResetTraffic zeroes B's counters. Must be set exactly when A's traffic
	// was reset too (paid renewal), so both subscriptions stay in step.
	ResetTraffic bool
	// MigrateLegacyName recreates an add-on that still lives under the old
	// tg_<id>+suffix name under the discoverable one. The panel has no rename,
	// so this DELETES the old user — which would break a manual binding wired
	// to its subscription URL in the middleware. Therefore it never runs on the
	// automatic paths: only from the explicit admin "sync everyone" action.
	MigrateLegacyName bool
}

// AddSubUpsert reports what an upsert actually did.
type AddSubUpsert struct {
	// Done is true when B was created or updated (false = the user was skipped:
	// expired, no expiry, or an add-on itself — not an error).
	Done bool
	// Legacy carries the username of an add-on found under the old naming
	// scheme. Outside migration it keeps being managed exactly as before and is
	// only reported, so an admin can decide when to move it.
	Legacy string
	// Migrated is true when that legacy user was replaced by a correctly named
	// one during this call.
	Migrated bool
}

// UpsertAddSub creates/updates the add-on user B for telegramID. B inherits
// expireAt, traffic-reset strategy and device limit from the main user A; only
// squads and traffic are overridden. B carries NO telegramId and tag
// CHILLBOT_ADD, so it never appears in by-telegram-id lookups.
func (c *Client) UpsertAddSub(ctx context.Context, telegramID int64, opt AddSubOptions) (AddSubUpsert, error) {
	a, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return AddSubUpsert{}, err
	}
	main := toPanelUser(a)
	if main == nil {
		return AddSubUpsert{}, nil
	}
	if main.TelegramID == 0 {
		main.TelegramID = telegramID
	}
	return c.UpsertAddSubForUser(ctx, *main, opt)
}

// UpsertAddSubForUser is UpsertAddSub for an already-fetched main user, so the
// admin backfill can walk the panel's user list without re-reading each user.
func (c *Client) UpsertAddSubForUser(ctx context.Context, main PanelUser, opt AddSubOptions) (res AddSubUpsert, err error) {
	if main.UUID == "" || main.ExpireAt == "" || expired(main.ExpireAt) {
		return res, nil
	}
	// Never build an add-on of an add-on.
	if main.Tag == BotTagAdd || strings.HasSuffix(main.Username, normalizeAddSubSuffix(opt.Suffix)) {
		return res, nil
	}
	limits := UserLimits{
		TrafficBytes:   opt.TrafficBytes,
		DeviceLimit:    main.DeviceLimit,
		Strategy:       main.Strategy,
		InternalSquads: opt.InternalSquads,
	}
	existing, want, err := c.findAddSub(ctx, main.Username, main.TelegramID, opt.Suffix)
	if err != nil {
		return res, err
	}
	mainDisabled := strings.EqualFold(main.Status, StatusDisabled)

	// An add-on still living under the legacy name can't be auto-discovered by
	// the middleware. It is NOT touched by default: an admin may have wired its
	// subscription URL into the middleware as a manual binding, and both
	// deleting it and letting it go stale would break a merge that works today.
	// So it keeps being managed exactly as before, and the move to the
	// discoverable name happens only on the explicit admin action.
	if existing != nil && existing.Username != want {
		res.Legacy = existing.Username
		if opt.MigrateLegacyName {
			// New user first, old one only after it exists — a failure in
			// between must never leave the subscriber without an add-on.
			if err := c.createAddSub(ctx, want, main, limits, opt.TrafficBytes, mainDisabled); err != nil {
				return res, err
			}
			res.Done = true
			if err := c.deleteUser(ctx, existing.UUID); err != nil {
				return res, err
			}
			res.Migrated = true
			return res, nil
		}
	}
	// A previous migration may have created the new B and then failed to delete
	// the old one. Once the new name resolves first, that leftover would never
	// be looked at again, so the migrating pass probes the legacy name too.
	if existing != nil && existing.Username == want && opt.MigrateLegacyName {
		if legacy := legacyAddSubUsername(main.TelegramID, opt.Suffix); legacy != want {
			if old, lerr := c.FindByUsername(ctx, legacy); lerr == nil && old != nil && old.Tag == BotTagAdd {
				res.Legacy = old.Username
				if err := c.deleteUser(ctx, old.UUID); err != nil {
					return res, err
				}
				res.Migrated = true
			}
		}
	}

	if existing != nil {
		patch := map[string]any{"uuid": existing.UUID, "expireAt": main.ExpireAt}
		// Status is only touched when the two are out of step: mirroring A's
		// block, or lifting a leftover block on B. Writing ACTIVE otherwise
		// would un-limit a B whose traffic the panel had just capped.
		switch {
		case mainDisabled:
			patch["status"] = StatusDisabled
		case strings.EqualFold(existing.Status, StatusDisabled):
			patch["status"] = "ACTIVE"
		}
		applyLimits(patch, limits)
		// Unlike A, B's traffic allowance is fully bot-owned, so "unlimited"
		// (0) must be written explicitly instead of being left as-is.
		patch["trafficLimitBytes"] = opt.TrafficBytes
		if _, _, err := c.upsertCall(ctx, http.MethodPatch, "/api/users", patch); err != nil {
			return res, err
		}
		res.Done = true
		if opt.ResetTraffic {
			return res, c.ResetTraffic(ctx, existing.UUID)
		}
		return res, nil
	}

	if err := c.createAddSub(ctx, want, main, limits, opt.TrafficBytes, mainDisabled); err != nil {
		return res, err
	}
	res.Done = true
	return res, nil
}

func (c *Client) createAddSub(ctx context.Context, username string, main PanelUser, limits UserLimits, trafficBytes int64, disabled bool) error {
	body := map[string]any{
		"username": username,
		"expireAt": main.ExpireAt,
		"tag":      BotTagAdd,
	}
	if disabled {
		body["status"] = StatusDisabled
	}
	applyLimits(body, limits)
	body["trafficLimitBytes"] = trafficBytes
	_, _, err := c.upsertCall(ctx, http.MethodPost, "/api/users", body)
	return err
}

// mainUsernameFor returns A's panel username, or "" when A is genuinely gone
// (deleted). A lookup FAILURE is returned as an error and never degraded to "":
// that would narrow the search to the legacy name and quietly skip a B living
// under the derived one — leaving a blocked user served or an orphan behind.
func (c *Client) mainUsernameFor(ctx context.Context, telegramID int64) (string, error) {
	a, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return "", err
	}
	if a == nil {
		return "", nil
	}
	return a.Username, nil
}

// findAddSubFor resolves B for a telegram id, going through A's username.
func (c *Client) findAddSubFor(ctx context.Context, telegramID int64, suffix string) (*PanelUser, error) {
	main, err := c.mainUsernameFor(ctx, telegramID)
	if err != nil {
		return nil, err
	}
	u, _, err := c.findAddSub(ctx, main, telegramID, suffix)
	return u, err
}

func (c *Client) deleteUser(ctx context.Context, uuid string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/api/users/"+url.PathEscape(uuid), nil)
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusCreated {
		return classifyHTTP(resp)
	}
	return nil
}

// DeleteAddSub removes the add-on user B. Call it BEFORE deleting A, so B can
// still be resolved from A's username.
func (c *Client) DeleteAddSub(ctx context.Context, telegramID int64, suffix string) error {
	u, err := c.findAddSubFor(ctx, telegramID, suffix)
	if err != nil || u == nil || u.UUID == "" {
		return err
	}
	return c.deleteUser(ctx, u.UUID)
}

func (c *Client) SetAddSubEnabled(ctx context.Context, telegramID int64, suffix string, enable bool) error {
	u, err := c.findAddSubFor(ctx, telegramID, suffix)
	if err != nil || u == nil || u.UUID == "" {
		return err
	}
	status := StatusDisabled
	if enable {
		status = "ACTIVE"
	}
	resp, err := c.do(ctx, http.MethodPatch, "/api/users", map[string]any{"uuid": u.UUID, "status": status})
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return classifyHTTP(resp)
	}
	return nil
}

// AddSubInfo is a read-only snapshot of the add-on subscription B, for the
// user-facing screens. Limit 0 means unlimited.
type AddSubInfo struct {
	UUID      string
	Username  string
	Status    string
	Limit     int64
	Used      int64
	Exhausted bool
}

// AddSubStatus returns B's traffic/status snapshot. ok=false when the user has
// no add-on subscription (or the panel can't be read) — callers then show
// nothing, so the screen degrades gracefully.
func (c *Client) AddSubStatus(ctx context.Context, telegramID int64, suffix string) (AddSubInfo, bool) {
	u, err := c.findAddSubFor(ctx, telegramID, suffix)
	if err != nil || u == nil || u.UUID == "" {
		return AddSubInfo{}, false
	}
	info := AddSubInfo{
		UUID:     u.UUID,
		Username: u.Username,
		Status:   u.Status,
		Limit:    u.TrafficLimit,
		Used:     u.TrafficUsed,
	}
	info.Exhausted = info.Limit > 0 && info.Used >= info.Limit
	return info, true
}

// ResetAddSubDevices mirrors ResetDevicesByTelegramID onto the add-on user B:
// the middleware forwards the client's HWID headers to B as well, so B's device
// slots fill up with the same devices and must be freed by the same reset.
// found=false when the user has no add-on subscription.
func (c *Client) ResetAddSubDevices(ctx context.Context, telegramID int64, suffix string) (res DeviceResetResult, found bool, err error) {
	u, err := c.findAddSubFor(ctx, telegramID, suffix)
	if err != nil {
		return DeviceResetResult{}, false, err
	}
	if u == nil || u.UUID == "" {
		return DeviceResetResult{}, false, nil
	}
	res.UUID = u.UUID
	pre := c.hwidCount(ctx, u.UUID)
	if err := c.revokeUser(ctx, u.UUID); err != nil {
		return res, true, err
	}
	res.KeysRotated = true
	if derr := c.deleteAllHwidRetry(ctx, u.UUID, hwidSyncAttempts); derr != nil {
		res.HwidErr = derr
	} else {
		res.HwidCleared = true
		if pre > 0 {
			res.Removed = pre
		}
	}
	return res, true, nil
}

type UserLimits struct {
	TrafficBytes   int64
	DeviceLimit    int
	InternalSquads []string
	ExternalSquad  string
	Strategy       string
}

func (c *Client) CreateOrUpdateUser(ctx context.Context, telegramID int64, months int, limits UserLimits) (string, string, error) {
	existing, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return "", "", err
	}
	expire := nextExpire(existing, months)

	if existing != nil && existing.Uuid != "" {
		if !ownedByBot(existing, telegramID) {
			return "", "", fmt.Errorf("аккаунт этого пользователя создан НЕ через бота — изменять его запрещено")
		}
		patch := map[string]any{
			"uuid":     existing.Uuid,
			"expireAt": expire,
		}
		applyLimits(patch, limits)
		link, expireAt, err := c.upsertCall(ctx, http.MethodPatch, "/api/users", patch)
		if err == nil {
			_ = c.ResetTraffic(ctx, existing.Uuid)
		}
		return link, expireAt, err
	}

	body := map[string]any{
		"username":   fmt.Sprintf("tg_%d", telegramID),
		"telegramId": telegramID,
		"expireAt":   expire,
		"tag":        BotTag,
	}
	applyLimits(body, limits)
	return c.upsertCall(ctx, http.MethodPost, "/api/users", body)
}

func (c *Client) CreateOrUpdateUserDays(ctx context.Context, telegramID int64, days int, limits UserLimits) (string, string, error) {
	existing, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return "", "", err
	}
	base := time.Now().UTC()
	if existing != nil && existing.ExpireAt != "" {
		if t, err := time.Parse(time.RFC3339, existing.ExpireAt); err == nil && t.After(base) {
			base = t
		}
	}
	expire := base.AddDate(0, 0, days).Format(time.RFC3339)

	if existing != nil && existing.Uuid != "" {
		if !ownedByBot(existing, telegramID) {
			return "", "", fmt.Errorf("аккаунт этого пользователя создан НЕ через бота — изменять его запрещено")
		}
		patch := map[string]any{"uuid": existing.Uuid, "expireAt": expire}
		applyLimits(patch, limits)
		return c.upsertCall(ctx, http.MethodPatch, "/api/users", patch)
	}
	body := map[string]any{
		"username":   fmt.Sprintf("tg_%d", telegramID),
		"telegramId": telegramID,
		"expireAt":   expire,
		"tag":        BotTag,
	}
	applyLimits(body, limits)
	return c.upsertCall(ctx, http.MethodPost, "/api/users", body)
}

func applyLimits(body map[string]any, l UserLimits) {
	if l.TrafficBytes > 0 {
		body["trafficLimitBytes"] = l.TrafficBytes
	}
	if l.Strategy != "" {
		body["trafficLimitStrategy"] = l.Strategy
	}
	if l.DeviceLimit > 0 {
		body["hwidDeviceLimit"] = l.DeviceLimit
	}
	if len(l.InternalSquads) > 0 {
		body["activeInternalSquads"] = l.InternalSquads
	}
	if l.ExternalSquad != "" {
		body["externalSquadUuid"] = l.ExternalSquad
	}
}

type Squad struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

func (c *Client) ListSquads(ctx context.Context) ([]Squad, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/internal-squads", nil)
	if err != nil {
		return nil, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTP(resp)
	}
	var env struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("разбор ответа панели: %w", err)
	}

	var obj struct {
		InternalSquads []Squad `json:"internalSquads"`
	}
	if json.Unmarshal(env.Response, &obj) == nil && len(obj.InternalSquads) > 0 {
		return obj.InternalSquads, nil
	}

	var arr []Squad
	if json.Unmarshal(env.Response, &arr) == nil {
		return arr, nil
	}
	return nil, nil
}

type ExternalSquad struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

func (c *Client) ListExternalSquads(ctx context.Context) ([]ExternalSquad, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/external-squads", nil)
	if err != nil {
		return nil, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTP(resp)
	}
	var env struct {
		Response struct {
			ExternalSquads []ExternalSquad `json:"externalSquads"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("разбор ответа панели: %w", err)
	}
	return env.Response.ExternalSquads, nil
}

// SquadFull is an internal squad enriched with its inbound membership, used to
// map a plan's squad to the hosts (and thus countries) available to it.
type SquadFull struct {
	UUID          string
	Name          string
	InboundsCount int
	InboundUUIDs  []string
}

// ListSquadsFull returns internal squads with their inbound UUIDs and inbound
// count (GET /api/internal-squads).
func (c *Client) ListSquadsFull(ctx context.Context) ([]SquadFull, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/internal-squads", nil)
	if err != nil {
		return nil, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTP(resp)
	}
	var env struct {
		Response struct {
			InternalSquads []struct {
				UUID string `json:"uuid"`
				Name string `json:"name"`
				Info struct {
					InboundsCount int `json:"inboundsCount"`
				} `json:"info"`
				Inbounds []struct {
					UUID string `json:"uuid"`
				} `json:"inbounds"`
			} `json:"internalSquads"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("разбор ответа панели: %w", err)
	}
	out := make([]SquadFull, 0, len(env.Response.InternalSquads))
	for _, sq := range env.Response.InternalSquads {
		sf := SquadFull{UUID: sq.UUID, Name: sq.Name, InboundsCount: sq.Info.InboundsCount}
		for _, ib := range sq.Inbounds {
			if ib.UUID != "" {
				sf.InboundUUIDs = append(sf.InboundUUIDs, ib.UUID)
			}
		}
		out = append(out, sf)
	}
	return out, nil
}

// Host is the subset of a panel host needed to derive available countries: its
// human-readable remark (often "🇩🇪 Germany"), the inbound it exposes, and the
// internal squads explicitly excluded from it.
type Host struct {
	Remark         string
	InboundUUID    string
	ExcludedSquads []string
	Disabled       bool
	Hidden         bool
}

// ListHosts returns all panel hosts (GET /api/hosts).
func (c *Client) ListHosts(ctx context.Context) ([]Host, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/hosts", nil)
	if err != nil {
		return nil, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTP(resp)
	}
	var env struct {
		Response []struct {
			Remark  string `json:"remark"`
			Inbound struct {
				ConfigProfileInboundUUID string `json:"configProfileInboundUuid"`
			} `json:"inbound"`
			ExcludedInternalSquads []string `json:"excludedInternalSquads"`
			IsDisabled             bool     `json:"isDisabled"`
			IsHidden               bool     `json:"isHidden"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("разбор ответа панели: %w", err)
	}
	out := make([]Host, 0, len(env.Response))
	for _, h := range env.Response {
		out = append(out, Host{
			Remark:         h.Remark,
			InboundUUID:    h.Inbound.ConfigProfileInboundUUID,
			ExcludedSquads: h.ExcludedInternalSquads,
			Disabled:       h.IsDisabled,
			Hidden:         h.IsHidden,
		})
	}
	return out, nil
}

func (c *Client) ResetTraffic(ctx context.Context, uuid string) error {
	resp, err := c.do(ctx, http.MethodPost, "/api/users/"+url.PathEscape(uuid)+"/actions/reset-traffic", nil)
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return classifyHTTP(resp)
	}
	return nil
}

func (c *Client) DeleteByTelegramID(ctx context.Context, telegramID int64) (bool, error) {
	u, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return false, err
	}
	if u == nil || u.Uuid == "" {
		return false, nil
	}
	if !ownedByBot(u, telegramID) {
		return false, fmt.Errorf("аккаунт <code>%d</code> создан НЕ через бота — удалять его запрещено", telegramID)
	}
	resp, err := c.do(ctx, http.MethodDelete, "/api/users/"+u.Uuid, nil)
	if err != nil {
		return false, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusCreated {
		return false, classifyHTTP(resp)
	}
	return true, nil
}

func (c *Client) setSubEnabled(ctx context.Context, telegramID int64, enable bool) (bool, error) {
	u, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return false, err
	}
	if u == nil || u.Uuid == "" {
		return false, nil
	}
	if !ownedByBot(u, telegramID) {
		return false, fmt.Errorf("аккаунт <code>%d</code> создан НЕ через бота — управлять им запрещено", telegramID)
	}
	status := "DISABLED"
	if enable {
		status = "ACTIVE"
	}
	body := map[string]any{"uuid": u.Uuid, "status": status}
	resp, err := c.do(ctx, http.MethodPatch, "/api/users", body)
	if err != nil {
		return false, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return false, classifyHTTP(resp)
	}
	return true, nil
}

func (c *Client) DisableByTelegramID(ctx context.Context, telegramID int64) (bool, error) {
	return c.setSubEnabled(ctx, telegramID, false)
}

func (c *Client) EnableByTelegramID(ctx context.Context, telegramID int64) (bool, error) {
	return c.setSubEnabled(ctx, telegramID, true)
}

// DeviceResetResult reports what ResetDevicesByTelegramID actually did on the
// panel, so callers can warn on a partial result.
type DeviceResetResult struct {
	UUID        string // panel user uuid (set once found); lets the caller keep retrying delete-all
	KeysRotated bool   // proxy credentials rotated (all connected devices dropped)
	HwidCleared bool   // all HWID device registrations deleted (slots freed)
	Removed     int    // HWID devices removed (best-effort, from the pre-count)
	HwidErr     error  // delete-all still failing after the synchronous retries (keys were still rotated)
}

// ResetDevicesByTelegramID fully resets a user's devices: it rotates the proxy
// credentials — dropping every currently connected client while keeping the same
// subscription URL — AND deletes all of the user's HWID device registrations,
// freeing the per-user device slots. Both endpoints exist on every supported
// panel (minimum 2.7.4). The credential rotation hard-fails the reset; the HWID
// delete-all is retried a few times synchronously and, if it still fails, is
// reported via HwidErr so the caller can keep retrying it in the background
// (res.UUID carries the panel uuid for that). The reset itself is not failed by
// a delete-all miss, since the rotation has already applied.
// found=false when the user is unknown to the panel.
func (c *Client) ResetDevicesByTelegramID(ctx context.Context, telegramID int64) (res DeviceResetResult, found bool, err error) {
	u, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return DeviceResetResult{}, false, err
	}
	if u == nil || u.Uuid == "" {
		return DeviceResetResult{}, false, nil
	}
	if !ownedByBot(u, telegramID) {
		return DeviceResetResult{}, false, fmt.Errorf("аккаунт <code>%d</code> создан НЕ через бота — управлять им запрещено", telegramID)
	}
	res.UUID = u.Uuid

	// Count devices first so we can report how many slots were freed (best-effort).
	pre := c.hwidCount(ctx, u.Uuid)

	// 1) Rotate credentials — drops every connected device. Hard-fails the reset.
	if err := c.revokeUser(ctx, u.Uuid); err != nil {
		return res, true, err
	}
	res.KeysRotated = true

	// 2) Delete all HWID registrations so the device-limit slots are freed.
	//    Retried synchronously a few times; a persistent failure is handed back
	//    via HwidErr for the caller to finish in the background (until success).
	if derr := c.deleteAllHwidRetry(ctx, u.Uuid, hwidSyncAttempts); derr != nil {
		res.HwidErr = derr
	} else {
		res.HwidCleared = true
		if pre > 0 {
			res.Removed = pre
		}
	}
	return res, true, nil
}

// deleteAllHwidRetry calls deleteAllHwid until it succeeds, ctx is done, or (when
// maxAttempts > 0) maxAttempts have been made, backing off exponentially between
// tries. maxAttempts <= 0 means "keep going until ctx is done". Returns the last
// error seen (nil on success).
func (c *Client) deleteAllHwidRetry(ctx context.Context, uuid string, maxAttempts int) error {
	base := c.hwidRetryBase
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	maxb := c.hwidRetryMax
	if maxb <= 0 {
		maxb = 30 * time.Second
	}
	backoff := base
	var last error
	for attempt := 1; ; attempt++ {
		if last = c.deleteAllHwid(ctx, uuid); last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return last
		}
		if maxAttempts > 0 && attempt >= maxAttempts {
			return last
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return last
		case <-t.C:
		}
		if backoff < maxb {
			if backoff *= 2; backoff > maxb {
				backoff = maxb
			}
		}
	}
}

// DeleteAllHwidUntil keeps retrying the HWID delete-all (with backoff) until it
// succeeds or ctx is done. Used for the best-effort background cleanup after a
// device reset whose synchronous delete-all attempts didn't get through.
func (c *Client) DeleteAllHwidUntil(ctx context.Context, uuid string) error {
	return c.deleteAllHwidRetry(ctx, uuid, 0)
}

// revokeUser rotates the user's proxy credentials
// (POST /api/users/{uuid}/actions/revoke with revokeOnlyPasswords=true), keeping
// the same subscription URL so clients only need to refresh to reconnect.
func (c *Client) revokeUser(ctx context.Context, uuid string) error {
	body := map[string]any{"revokeOnlyPasswords": true}
	resp, err := c.do(ctx, http.MethodPost, "/api/users/"+url.PathEscape(uuid)+"/actions/revoke", body)
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return classifyHTTP(resp)
	}
	return nil
}

// deleteAllHwid removes every HWID device registered to the user
// (POST /api/hwid/devices/delete-all with {userUuid}).
func (c *Client) deleteAllHwid(ctx context.Context, uuid string) error {
	body := map[string]any{"userUuid": uuid}
	resp, err := c.do(ctx, http.MethodPost, "/api/hwid/devices/delete-all", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return classifyHTTP(resp)
	}
	return nil
}

// hwidCount returns the number of HWID devices currently registered to the user,
// or -1 when it can't be determined. Best-effort; never fails the caller.
func (c *Client) hwidCount(ctx context.Context, uuid string) int {
	resp, err := c.do(ctx, http.MethodGet, "/api/hwid/devices/"+url.PathEscape(uuid), nil)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return -1
	}
	var env struct {
		Response struct {
			Total   int               `json:"total"`
			Devices []json.RawMessage `json:"devices"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return -1
	}
	if env.Response.Total > 0 {
		return env.Response.Total
	}
	return len(env.Response.Devices)
}

func (c *Client) Subscription(ctx context.Context, telegramID int64) (string, string, bool) {
	u, err := c.findByTelegram(ctx, telegramID)
	if err != nil || u == nil || u.SubscriptionURL == "" {
		return "", "", false
	}
	return u.SubscriptionURL, u.ExpireAt, true
}

const StatusDisabled = "DISABLED"

func (c *Client) SubscriptionFull(ctx context.Context, telegramID int64) (url, expireAt, status string, ok bool) {
	u, err := c.findByTelegram(ctx, telegramID)
	if err != nil || u == nil || u.SubscriptionURL == "" {
		return "", "", "", false
	}
	return u.SubscriptionURL, u.ExpireAt, u.Status, true
}

// DeviceInfo is a read-only snapshot of a user's HWID devices.
// Used is the number of devices currently registered on the subscription;
// Limit is the per-user device limit. HasLimit is false when no explicit
// per-user limit is set (0) — the panel-wide HWID_FALLBACK_DEVICE_LIMIT then
// applies and is unknown to the bot, so callers show only the connected count.
type DeviceInfo struct {
	Used     int
	Limit    int
	HasLimit bool
}

// DevicesByTelegramID returns the connected/allowed device counts for a user.
// Read-only: it never registers or deletes devices. ok=false when the user
// is unknown to the panel or HWID data is unavailable.
func (c *Client) DevicesByTelegramID(ctx context.Context, telegramID int64) (DeviceInfo, bool) {
	u, err := c.findByTelegram(ctx, telegramID)
	if err != nil || u == nil || u.Uuid == "" {
		return DeviceInfo{}, false
	}
	info := DeviceInfo{Limit: u.HwidDeviceLimit, HasLimit: u.HwidDeviceLimit > 0}

	resp, err := c.do(ctx, http.MethodGet, "/api/hwid/devices/"+url.PathEscape(u.Uuid), nil)
	if err != nil {
		return DeviceInfo{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DeviceInfo{}, false
	}
	var env struct {
		Response struct {
			Total   int               `json:"total"`
			Devices []json.RawMessage `json:"devices"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return DeviceInfo{}, false
	}
	info.Used = env.Response.Total
	if info.Used == 0 && len(env.Response.Devices) > 0 {
		info.Used = len(env.Response.Devices)
	}
	return info, true
}

func (c *Client) findByTelegram(ctx context.Context, telegramID int64) (*panelUser, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/users/by-telegram-id/"+strconv.FormatInt(telegramID, 10), nil)
	if err != nil {
		return nil, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTP(resp)
	}
	var env struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, err
	}
	var arr []panelUser
	if json.Unmarshal(env.Response, &arr) == nil && len(arr) > 0 {
		return &arr[0], nil
	}
	var one panelUser
	if json.Unmarshal(env.Response, &one) == nil && one.Uuid != "" {
		return &one, nil
	}
	return nil, nil
}

func (c *Client) FindByTelegramID(ctx context.Context, telegramID int64) (*PanelUser, error) {
	u, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return nil, err
	}
	return toPanelUser(u), nil
}

func (c *Client) FindByUsername(ctx context.Context, username string) (*PanelUser, error) {
	return c.fetchOne(ctx, "/api/users/by-username/"+url.PathEscape(username))
}

func (c *Client) FindByUUID(ctx context.Context, uuid string) (*PanelUser, error) {
	return c.fetchOne(ctx, "/api/users/"+url.PathEscape(uuid))
}

func (c *Client) fetchOne(ctx context.Context, path string) (*PanelUser, error) {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTP(resp)
	}
	var env struct {
		Response panelUser `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("разбор ответа панели: %w", err)
	}
	return toPanelUser(&env.Response), nil
}

func (c *Client) ListUsersPage(ctx context.Context, start, size int) ([]PanelUser, int, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/users?start="+strconv.Itoa(start)+"&size="+strconv.Itoa(size), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, classifyHTTP(resp)
	}
	var env struct {
		Response struct {
			Users []panelUser `json:"users"`
			Total int         `json:"total"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, 0, fmt.Errorf("разбор ответа панели: %w", err)
	}
	out := make([]PanelUser, 0, len(env.Response.Users))
	for i := range env.Response.Users {
		if pu := toPanelUser(&env.Response.Users[i]); pu != nil {
			out = append(out, *pu)
		}
	}
	return out, env.Response.Total, nil
}

func (c *Client) LinkTelegramID(ctx context.Context, uuid string, telegramID int64, setTag bool) error {
	body := map[string]any{"uuid": uuid, "telegramId": telegramID}
	if setTag {
		body["tag"] = BotTag
	}
	resp, err := c.do(ctx, http.MethodPatch, "/api/users", body)
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return classifyHTTP(resp)
	}
	return nil
}

func (c *Client) upsertCall(ctx context.Context, method, path string, body any) (string, string, error) {
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		return "", "", fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", "", classifyHTTP(resp)
	}
	var env struct {
		Response panelUser `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return "", "", err
	}
	return env.Response.SubscriptionURL, env.Response.ExpireAt, nil
}

func nextExpire(existing *panelUser, months int) string {
	base := time.Now().UTC()
	if existing != nil && existing.ExpireAt != "" {
		if t, err := time.Parse(time.RFC3339, existing.ExpireAt); err == nil && t.After(base) {
			base = t
		}
	}
	return base.AddDate(0, months, 0).Format(time.RFC3339)
}

func classifyHTTP(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	snippet := strings.TrimSpace(string(body))
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("панель отклонила доступ (HTTP %d): проверьте API-token. %s", resp.StatusCode, snippet)
	case http.StatusNotFound:
		return fmt.Errorf("эндпоинт не найден (HTTP 404): проверьте URL панели")
	default:
		return fmt.Errorf("панель вернула HTTP %d: %s", resp.StatusCode, snippet)
	}
}

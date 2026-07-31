package remnawave

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	// gen caches which API dialect this panel speaks (see panelGen).
	gen       atomic.Int32
	probeMu   sync.Mutex
	lastProbe time.Time

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

// Remnawave 3.0.0 removed the user uuid from the API entirely (users are keyed
// by a numeric id) and dropped GET /api/users/by-telegram-id in favour of
// GET /api/users/stream?telegramId=. This client speaks both dialects and works
// out which one the panel in front of it uses, so one build keeps running
// against 2.7.4+ and 3.x alike.
type panelGen int32

const (
	genUnknown panelGen = iota
	genLegacy           // before 3.0.0: user addressed by uuid
	genV3               // 3.0.0+: user addressed by numeric id
)

// genRecheckEvery bounds how often an empty lookup may re-probe the panel, so a
// panel upgraded under a running bot is picked up without a restart while an
// unknown user doesn't double every request.
const genRecheckEvery = 10 * time.Minute

// UserRef identifies a panel user in whichever dialect the panel speaks. It is
// opaque to callers: they carry it around (a background retry, a log line) and
// hand it back to the client.
type UserRef struct {
	uuid string
	id   int64
}

func (r UserRef) Empty() bool { return r.uuid == "" && r.id <= 0 }

// Key is a stable string for dedup maps and log lines.
func (r UserRef) Key() string {
	if r.uuid != "" {
		return r.uuid
	}
	if r.id > 0 {
		return strconv.FormatInt(r.id, 10)
	}
	return ""
}

// path is the identifier as it goes into a URL path segment.
func (r UserRef) path() string { return url.PathEscape(r.Key()) }

// apply points a request body at this user the way the panel expects it.
func (r UserRef) apply(body map[string]any) map[string]any {
	if r.uuid != "" {
		body["uuid"] = r.uuid
	} else {
		body["id"] = r.id
	}
	return body
}

// hwidBody is the /api/hwid/devices body naming this user.
func (r UserRef) hwidBody() map[string]any {
	if r.uuid != "" {
		return map[string]any{"userUuid": r.uuid}
	}
	return map[string]any{"userId": r.id}
}

func (c *Client) generation() panelGen { return panelGen(c.gen.Load()) }

func (c *Client) noteGen(g panelGen) {
	if g != genUnknown {
		c.gen.Store(int32(g))
	}
}

// noteUser learns the dialect for free from any user payload that came back.
func (c *Client) noteUser(u *panelUser) {
	switch {
	case u == nil:
	case u.Uuid != "":
		c.noteGen(genLegacy)
	case u.ID > 0:
		c.noteGen(genV3)
	}
}

// probeGen asks the panel which dialect it speaks: /api/users/stream exists only
// from 3.0.0 on. Deliberately careful — a wrong answer here sends every lookup
// to a route the panel doesn't have:
//   - 200 counts only when the body really is a stream envelope (a proxy or a
//     catch-all answering 200 to anything proves nothing);
//   - any other client-side refusal means a pre-3.0.0 panel. Not just 404: on
//     2.x the request matches GET /api/users/:uuid, whose uuid validation
//     answers 400;
//   - auth errors, rate limiting, 5xx and transport failures decide nothing —
//     the caller must treat that as "don't know", never as "no such user".
//
// Attempts are rate-limited by genRecheckEvery (including the fruitless ones),
// so a flaky panel can't turn every lookup into two requests, and the HTTP call
// is made outside the lock so concurrent lookups don't queue up behind it.
func (c *Client) probeGen(ctx context.Context) panelGen {
	c.probeMu.Lock()
	if g := c.generation(); !c.lastProbe.IsZero() && time.Since(c.lastProbe) < genRecheckEvery {
		c.probeMu.Unlock()
		return g
	}
	c.lastProbe = time.Now()
	c.probeMu.Unlock()

	resp, err := c.do(ctx, http.MethodGet, "/api/users/stream?size=1", nil)
	if err != nil {
		return c.generation()
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return c.generation()
		}
		var env struct {
			Response *struct {
				Users *[]json.RawMessage `json:"users"`
			} `json:"response"`
		}
		if json.Unmarshal(body, &env) == nil && env.Response != nil && env.Response.Users != nil {
			c.noteGen(genV3)
		}
	case undecidedStatus(resp.StatusCode):
		// auth / rate limit / server trouble — proves nothing
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		c.noteGen(genLegacy)
	}
	return c.generation()
}

// undecidedStatus marks answers that say nothing about the panel's API version.
func undecidedStatus(code int) bool {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusProxyAuthRequired,
		http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return code >= 500
}

// resetGen forgets the cached dialect, so the next lookup probes again. Used
// when a route that must exist in that dialect answers "no such route" — e.g.
// the panel was rolled back from 3.x while the bot kept running.
func (c *Client) resetGen() {
	c.gen.Store(int32(genUnknown))
	c.probeMu.Lock()
	c.lastProbe = time.Time{}
	c.probeMu.Unlock()
}

// routeGone recognises the framework's "no such route here" 404 — the shape a
// panel answers with once an endpoint has been removed (3.0.0 dropped
// by-telegram-id): a message like "Cannot GET /api/...". The panel's own
// "no such user" 404 looks different (typed error, its own message), and a
// body that says neither is treated as the ordinary not-found so a panel that
// answers tersely keeps behaving exactly as before.
func routeGone(body []byte) bool {
	var env struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &env) != nil {
		return false
	}
	return strings.HasPrefix(env.Message, "Cannot ")
}

type panelUser struct {
	Uuid            string `json:"uuid"`
	ID              int64  `json:"id"`
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

// ref is how this user is addressed by this panel: uuid before 3.0.0, numeric
// id from 3.0.0 on.
func (u *panelUser) ref() UserRef {
	if u == nil {
		return UserRef{}
	}
	return UserRef{uuid: u.Uuid, id: u.ID}
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
	// ID is the numeric identifier (panel 3.0.0+); UUID is the pre-3.0.0 one.
	// Ref is whichever of them this panel actually understands.
	ID  int64
	Ref UserRef
}

func toPanelUser(u *panelUser) *PanelUser {
	if u == nil || u.ref().Empty() {
		return nil
	}
	return &PanelUser{
		UUID:            u.Uuid,
		ID:              u.ID,
		Ref:             u.ref(),
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
		if u == nil || u.Ref.Empty() {
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
	if main.Ref.Empty() || main.ExpireAt == "" || expired(main.ExpireAt) {
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
			if err := c.deleteUser(ctx, existing.Ref); err != nil {
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
				if err := c.deleteUser(ctx, old.Ref); err != nil {
					return res, err
				}
				res.Migrated = true
			}
		}
	}

	if existing != nil {
		patch := existing.Ref.apply(map[string]any{"expireAt": main.ExpireAt})
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
			return res, c.ResetTraffic(ctx, existing.Ref)
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

func (c *Client) deleteUser(ctx context.Context, ref UserRef) error {
	resp, err := c.do(ctx, http.MethodDelete, "/api/users/"+ref.path(), nil)
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if !writeOK(resp.StatusCode) {
		return classifyHTTP(resp)
	}
	return nil
}

// DeleteAddSub removes the add-on user B. Call it BEFORE deleting A, so B can
// still be resolved from A's username.
func (c *Client) DeleteAddSub(ctx context.Context, telegramID int64, suffix string) error {
	u, err := c.findAddSubFor(ctx, telegramID, suffix)
	if err != nil || u == nil || u.Ref.Empty() {
		return err
	}
	return c.deleteUser(ctx, u.Ref)
}

func (c *Client) SetAddSubEnabled(ctx context.Context, telegramID int64, suffix string, enable bool) error {
	u, err := c.findAddSubFor(ctx, telegramID, suffix)
	if err != nil || u == nil || u.Ref.Empty() {
		return err
	}
	status := StatusDisabled
	if enable {
		status = "ACTIVE"
	}
	resp, err := c.do(ctx, http.MethodPatch, "/api/users", u.Ref.apply(map[string]any{"status": status}))
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if !writeOK(resp.StatusCode) {
		return classifyHTTP(resp)
	}
	return nil
}

// AddSubInfo is a read-only snapshot of the add-on subscription B, for the
// user-facing screens. Limit 0 means unlimited.
type AddSubInfo struct {
	Ref       UserRef
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
	if err != nil || u == nil || u.Ref.Empty() {
		return AddSubInfo{}, false
	}
	info := AddSubInfo{
		Ref:      u.Ref,
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
	if u == nil || u.Ref.Empty() {
		return DeviceResetResult{}, false, nil
	}
	res.Ref = u.Ref
	pre := c.hwidCount(ctx, u.Ref)
	if err := c.revokeUser(ctx, u.Ref); err != nil {
		return res, true, err
	}
	res.KeysRotated = true
	if derr := c.deleteAllHwidRetry(ctx, u.Ref, hwidSyncAttempts); derr != nil {
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

	if existing != nil && !existing.ref().Empty() {
		if !ownedByBot(existing, telegramID) {
			return "", "", fmt.Errorf("аккаунт этого пользователя создан НЕ через бота — изменять его запрещено")
		}
		patch := existing.ref().apply(map[string]any{"expireAt": expire})
		applyLimits(patch, limits)
		link, expireAt, err := c.upsertCall(ctx, http.MethodPatch, "/api/users", patch)
		if err == nil {
			_ = c.ResetTraffic(ctx, existing.ref())
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

	if existing != nil && !existing.ref().Empty() {
		if !ownedByBot(existing, telegramID) {
			return "", "", fmt.Errorf("аккаунт этого пользователя создан НЕ через бота — изменять его запрещено")
		}
		patch := existing.ref().apply(map[string]any{"expireAt": expire})
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

func (c *Client) ResetTraffic(ctx context.Context, ref UserRef) error {
	resp, err := c.do(ctx, http.MethodPost, "/api/users/"+ref.path()+"/actions/reset-traffic", nil)
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if !writeOK(resp.StatusCode) {
		return classifyHTTP(resp)
	}
	return nil
}

func (c *Client) DeleteByTelegramID(ctx context.Context, telegramID int64) (bool, error) {
	u, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return false, err
	}
	if u == nil || u.ref().Empty() {
		return false, nil
	}
	if !ownedByBot(u, telegramID) {
		return false, fmt.Errorf("аккаунт <code>%d</code> создан НЕ через бота — удалять его запрещено", telegramID)
	}
	if err := c.deleteUser(ctx, u.ref()); err != nil {
		return false, err
	}
	return true, nil
}

func (c *Client) setSubEnabled(ctx context.Context, telegramID int64, enable bool) (bool, error) {
	u, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return false, err
	}
	if u == nil || u.ref().Empty() {
		return false, nil
	}
	if !ownedByBot(u, telegramID) {
		return false, fmt.Errorf("аккаунт <code>%d</code> создан НЕ через бота — управлять им запрещено", telegramID)
	}
	status := "DISABLED"
	if enable {
		status = "ACTIVE"
	}
	body := u.ref().apply(map[string]any{"status": status})
	resp, err := c.do(ctx, http.MethodPatch, "/api/users", body)
	if err != nil {
		return false, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if !writeOK(resp.StatusCode) {
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
	Ref         UserRef // panel user reference (set once found); lets the caller keep retrying delete-all
	KeysRotated bool    // proxy credentials rotated (all connected devices dropped)
	HwidCleared bool    // all HWID device registrations deleted (slots freed)
	Removed     int     // HWID devices removed (best-effort, from the pre-count)
	HwidErr     error   // delete-all still failing after the synchronous retries (keys were still rotated)
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
	if u == nil || u.ref().Empty() {
		return DeviceResetResult{}, false, nil
	}
	if !ownedByBot(u, telegramID) {
		return DeviceResetResult{}, false, fmt.Errorf("аккаунт <code>%d</code> создан НЕ через бота — управлять им запрещено", telegramID)
	}
	res.Ref = u.ref()

	// Count devices first so we can report how many slots were freed (best-effort).
	pre := c.hwidCount(ctx, u.ref())

	// 1) Rotate credentials — drops every connected device. Hard-fails the reset.
	if err := c.revokeUser(ctx, u.ref()); err != nil {
		return res, true, err
	}
	res.KeysRotated = true

	// 2) Delete all HWID registrations so the device-limit slots are freed.
	//    Retried synchronously a few times; a persistent failure is handed back
	//    via HwidErr for the caller to finish in the background (until success).
	if derr := c.deleteAllHwidRetry(ctx, u.ref(), hwidSyncAttempts); derr != nil {
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
func (c *Client) deleteAllHwidRetry(ctx context.Context, ref UserRef, maxAttempts int) error {
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
		if last = c.deleteAllHwid(ctx, ref); last == nil {
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
func (c *Client) DeleteAllHwidUntil(ctx context.Context, ref UserRef) error {
	return c.deleteAllHwidRetry(ctx, ref, 0)
}

// revokeUser rotates the user's proxy credentials
// (POST /api/users/{uuid}/actions/revoke with revokeOnlyPasswords=true), keeping
// the same subscription URL so clients only need to refresh to reconnect.
func (c *Client) revokeUser(ctx context.Context, ref UserRef) error {
	body := map[string]any{"revokeOnlyPasswords": true}
	resp, err := c.do(ctx, http.MethodPost, "/api/users/"+ref.path()+"/actions/revoke", body)
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if !writeOK(resp.StatusCode) {
		return classifyHTTP(resp)
	}
	return nil
}

// deleteAllHwid removes every HWID device registered to the user
// (POST /api/hwid/devices/delete-all with {userUuid}).
func (c *Client) deleteAllHwid(ctx context.Context, ref UserRef) error {
	resp, err := c.do(ctx, http.MethodPost, "/api/hwid/devices/delete-all", ref.hwidBody())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if !writeOK(resp.StatusCode) {
		return classifyHTTP(resp)
	}
	return nil
}

// hwidCount returns the number of HWID devices currently registered to the user,
// or -1 when it can't be determined. Best-effort; never fails the caller.
func (c *Client) hwidCount(ctx context.Context, ref UserRef) int {
	resp, err := c.do(ctx, http.MethodGet, "/api/hwid/devices/"+ref.path(), nil)
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
	if err != nil || u == nil || u.ref().Empty() {
		return DeviceInfo{}, false
	}
	info := DeviceInfo{Limit: u.HwidDeviceLimit, HasLimit: u.HwidDeviceLimit > 0}

	resp, err := c.do(ctx, http.MethodGet, "/api/hwid/devices/"+u.ref().path(), nil)
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

// errPanelDialect is returned when the panel can't be asked which API version
// it speaks. Callers must surface it: silently reporting "no such user" would
// turn a temporary panel outage into "your subscription is gone", and a renewal
// into creating a second account.
var errPanelDialect = errors.New("не удалось определить версию API панели")

// findByTelegram resolves a user by telegram id in whichever dialect the panel
// speaks (see panelGen).
func (c *Client) findByTelegram(ctx context.Context, telegramID int64) (*panelUser, error) {
	if c.generation() == genV3 {
		u, gone, err := c.findByTelegramStream(ctx, telegramID)
		if err != nil {
			return nil, err
		}
		if !gone {
			return u, nil
		}
		// The route that must exist on 3.x is gone: the panel was rolled back.
		c.resetGen()
	}
	// Legacy route first while the dialect is unknown: on a pre-3.0.0 panel a
	// found user answers the question by itself, at no extra cost.
	u, gone, err := c.findByTelegramLegacy(ctx, telegramID)
	if err != nil {
		return nil, err
	}
	if u != nil {
		return u, nil
	}
	if !gone && c.generation() == genLegacy {
		// Known pre-3.0.0 panel answering its own "no such user".
		return nil, nil
	}
	// Either the route is gone (3.x), or the dialect isn't settled yet: ask
	// once (the answer is cached) instead of guessing.
	switch c.probeGen(ctx) {
	case genV3:
		u2, gone2, err2 := c.findByTelegramStream(ctx, telegramID)
		if err2 != nil {
			return nil, err2
		}
		if gone2 {
			return nil, errPanelDialect
		}
		return u2, nil
	case genLegacy:
		return nil, nil
	}
	if gone {
		// The old route is definitely gone and we could not find out what
		// replaced it — reporting "no such user" here would look like a lost
		// subscription and make a renewal create a second account.
		return nil, errPanelDialect
	}
	return nil, nil
}

// findByTelegramStream is the 3.0.0+ lookup: GET /api/users/stream?telegramId=.
// gone=true means the route itself is not there (wrong base URL, or a panel
// older than 3.0.0).
func (c *Client) findByTelegramStream(ctx context.Context, telegramID int64) (u *panelUser, gone bool, err error) {
	path := "/api/users/stream?size=1&telegramId=" + strconv.FormatInt(telegramID, 10)
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, false, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, classifyHTTP(resp)
	}
	var env struct {
		Response struct {
			Users []panelUser `json:"users"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, false, err
	}
	// Never trust the filter blindly: a panel (or anything in front of it) that
	// ignores telegramId would otherwise hand out another subscriber's account.
	for i := range env.Response.Users {
		if env.Response.Users[i].TelegramID != telegramID {
			continue
		}
		c.noteUser(&env.Response.Users[i])
		return &env.Response.Users[i], false, nil
	}
	return nil, false, nil
}

// findByTelegramLegacy is the pre-3.0.0 lookup, removed by the panel in 3.0.0.
// gone=true means the route itself no longer exists, as opposed to the panel
// answering that it has no such user.
func (c *Client) findByTelegramLegacy(ctx context.Context, telegramID int64) (u *panelUser, gone bool, err error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/users/by-telegram-id/"+strconv.FormatInt(telegramID, 10), nil)
	if err != nil {
		return nil, false, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, routeGone(body), nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, classifyHTTP(resp)
	}
	var env struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, false, err
	}
	var arr []panelUser
	if json.Unmarshal(env.Response, &arr) == nil && len(arr) > 0 {
		c.noteUser(&arr[0])
		return &arr[0], false, nil
	}
	var one panelUser
	if json.Unmarshal(env.Response, &one) == nil && !one.ref().Empty() {
		c.noteUser(&one)
		return &one, false, nil
	}
	return nil, false, nil
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

// FindByRef looks a user up by whatever identifier this panel uses in paths:
// the uuid before 3.0.0, the numeric id from 3.0.0 on.
func (c *Client) FindByRef(ctx context.Context, ref string) (*PanelUser, error) {
	return c.fetchOne(ctx, "/api/users/"+url.PathEscape(ref))
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
	c.noteUser(&env.Response)
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
		c.noteUser(&env.Response.Users[i])
		if pu := toPanelUser(&env.Response.Users[i]); pu != nil {
			out = append(out, *pu)
		}
	}
	return out, env.Response.Total, nil
}

func (c *Client) LinkTelegramID(ctx context.Context, ref UserRef, telegramID int64, setTag bool) error {
	body := ref.apply(map[string]any{"telegramId": telegramID})
	if setTag {
		body["tag"] = BotTag
	}
	resp, err := c.do(ctx, http.MethodPatch, "/api/users", body)
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if !writeOK(resp.StatusCode) {
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
	c.noteUser(&env.Response)
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

// writeOK covers every success code a write may answer with across panel
// generations: 3.0.0 turned several 200s into 201 (create) and 204/202
// (delete and background work), so a fixed 200 check would break on it.
func writeOK(code int) bool {
	switch code {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
		return true
	}
	return false
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

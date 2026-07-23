package remnawave

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

	logMu sync.Mutex
	logs  []APIEvent
}

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

func addSubUsername(telegramID int64, suffix string) string {
	if suffix == "" {
		suffix = "_addsub"
	}
	return fmt.Sprintf("tg_%d%s", telegramID, suffix)
}

// UpsertAddSub creates/updates the add-on user B for telegramID. B inherits
// expireAt, traffic-reset strategy and device limit from the main user A; only
// squads and traffic are overridden. B carries NO telegramId and tag
// CHILLBOT_ADD, so it never appears in by-telegram-id lookups.
func (c *Client) UpsertAddSub(ctx context.Context, telegramID int64, suffix string, trafficBytes int64, internalSquads []string) error {
	a, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return err
	}
	if a == nil || a.ExpireAt == "" {
		return nil
	}
	limits := UserLimits{
		TrafficBytes:   trafficBytes,
		DeviceLimit:    a.HwidDeviceLimit,
		Strategy:       a.TrafficLimitStrategy,
		InternalSquads: internalSquads,
	}
	uname := addSubUsername(telegramID, suffix)
	existing, err := c.FindByUsername(ctx, uname)
	if err != nil {
		return err
	}
	if existing != nil && existing.UUID != "" {
		if existing.Tag != BotTagAdd {
			return fmt.Errorf("addsub: пользователь %s принадлежит не боту", uname)
		}
		patch := map[string]any{"uuid": existing.UUID, "expireAt": a.ExpireAt}
		applyLimits(patch, limits)
		_, _, err = c.upsertCall(ctx, http.MethodPatch, "/api/users", patch)
		return err
	}
	body := map[string]any{
		"username": uname,
		"expireAt": a.ExpireAt,
		"tag":      BotTagAdd,
	}
	applyLimits(body, limits)
	_, _, err = c.upsertCall(ctx, http.MethodPost, "/api/users", body)
	return err
}

func (c *Client) DeleteAddSub(ctx context.Context, telegramID int64, suffix string) error {
	u, err := c.FindByUsername(ctx, addSubUsername(telegramID, suffix))
	if err != nil || u == nil || u.UUID == "" {
		return err
	}
	if u.Tag != BotTagAdd {
		return nil
	}
	resp, err := c.do(ctx, http.MethodDelete, "/api/users/"+u.UUID, nil)
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusCreated {
		return classifyHTTP(resp)
	}
	return nil
}

func (c *Client) SetAddSubEnabled(ctx context.Context, telegramID int64, suffix string, enable bool) error {
	u, err := c.FindByUsername(ctx, addSubUsername(telegramID, suffix))
	if err != nil || u == nil || u.UUID == "" {
		return err
	}
	if u.Tag != BotTagAdd {
		return nil
	}
	status := "DISABLED"
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
	KeysRotated bool  // proxy credentials rotated (all connected devices dropped)
	HwidCleared bool  // all HWID device registrations deleted (slots freed)
	Removed     int   // HWID devices removed (best-effort, from the pre-count)
	HwidErr     error // delete-all failed (non-fatal: keys were still rotated)
}

// ResetDevicesByTelegramID fully resets a user's devices: it rotates the proxy
// credentials — dropping every currently connected client while keeping the same
// subscription URL — AND deletes all of the user's HWID device registrations,
// freeing the per-user device slots. Both endpoints exist on every supported
// panel (minimum 2.7.4). A failed delete-all is reported via HwidErr but does not
// fail the reset, since the credential rotation has already applied.
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

	// Count devices first so we can report how many slots were freed (best-effort).
	pre := c.hwidCount(ctx, u.Uuid)

	// 1) Rotate credentials — drops every connected device. Hard-fails the reset.
	if err := c.revokeUser(ctx, u.Uuid); err != nil {
		return DeviceResetResult{}, true, err
	}
	res.KeysRotated = true

	// 2) Delete all HWID registrations so the device-limit slots are freed.
	if derr := c.deleteAllHwid(ctx, u.Uuid); derr != nil {
		res.HwidErr = derr
	} else {
		res.HwidCleared = true
		if pre > 0 {
			res.Removed = pre
		}
	}
	return res, true, nil
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

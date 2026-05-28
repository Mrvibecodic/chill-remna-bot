// Package remnawave — клиент REST API панели Remnawave.
package remnawave

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"remnabot/internal/model"
)

// LocalBaseURL — адрес панели внутри общей docker-сети (минуя reverse-proxy).
const LocalBaseURL = "http://remnawave:3000"

// APIEvent — одна запись лога исходящих запросов к панели (для админ-просмотра).
type APIEvent struct {
	Time       time.Time
	Method     string
	Path       string
	Status     int // 0, если ошибка транспорта (Err непустой)
	DurationMs int64
	Err        string // сообщение об ошибке (короткое)
}

// apiLogCap — кольцевой буфер: чем больше, тем дольше «помним» прошлые запросы.
// 200 — компромисс между видимостью истории и потреблением памяти.
const apiLogCap = 200

type Client struct {
	base   string
	token  string
	cookie string // "name=value" для eGames(nginx), иначе ""
	apiKey string // X-API-Key для защищённого Caddy, иначе ""
	local  bool
	http   *http.Client

	logMu sync.Mutex
	logs  []APIEvent // ring buffer длиной apiLogCap
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
		// ProxyCheckGuard панели рвёт сокет без этих заголовков при прямом :3000.
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		req.Header.Set("X-Forwarded-Proto", "https")
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie) // eGames(nginx): иначе 444
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

// appendLog добавляет запись в ring buffer (под мьютексом). Старые записи
// вытесняются, чтобы не разрастаться по памяти.
func (c *Client) appendLog(ev APIEvent) {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	c.logs = append(c.logs, ev)
	if len(c.logs) > apiLogCap {
		c.logs = c.logs[len(c.logs)-apiLogCap:]
	}
}

// Logs возвращает копию текущего лога (новые записи в конце).
func (c *Client) Logs() []APIEvent {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	out := make([]APIEvent, len(c.logs))
	copy(out, c.logs)
	return out
}

// ClearLogs очищает кольцевой буфер (используется кнопкой «🧹 Очистить»).
func (c *Client) ClearLogs() {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	c.logs = nil
}

// Health проверяет доступность панели.
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

// SystemStats возвращает число пользователей панели.
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
}

// BotTag помечает аккаунты, созданные этим ботом.
// Жёсткие правила безопасности: бот продлевает ТОЛЬКО свои аккаунты, и НИКОГДА
// не удаляет и не отключает (DISABLED) пользователей панели (таких вызовов нет).
const BotTag = "CHILLBOT"

func ownedByBot(u *panelUser, telegramID int64) bool {
	return u.Tag == BotTag || u.Username == fmt.Sprintf("tg_%d", telegramID)
}

// CreateOrUpdateUser создаёт юзера в панели или продлевает существующего
// (поиск по telegramId) на months месяцев и возвращает ссылку на подписку.
//
// ВНИМАНИЕ: точные формы запросов/ответов панели нужно проверить на живой
// инсталляции; при расхождении — поправить разбор.
func (c *Client) CreateOrUpdateUser(ctx context.Context, telegramID int64, months int, squadUUID string) (string, error) {
	existing, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return "", err
	}
	expire := nextExpire(existing, months)

	if existing != nil && existing.Uuid != "" {
		if !ownedByBot(existing, telegramID) {
			return "", fmt.Errorf("аккаунт этого пользователя создан НЕ через бота — изменять его запрещено")
		}
		return c.upsertCall(ctx, http.MethodPatch, "/api/users", map[string]any{
			"uuid":     existing.Uuid,
			"expireAt": expire,
		})
	}

	body := map[string]any{
		"username":   fmt.Sprintf("tg_%d", telegramID),
		"telegramId": telegramID,
		"expireAt":   expire,
		"tag":        BotTag,
	}
	if squadUUID != "" {
		body["activeInternalSquads"] = []string{squadUUID}
	}
	return c.upsertCall(ctx, http.MethodPost, "/api/users", body)
}

// Squad — внутренний сквад панели (для выбора при создании пользователей).
type Squad struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// ListSquads возвращает внутренние сквады панели. Разбор защитный: ответ
// панели может прийти как {response:{internalSquads:[...]}} или {response:[...]}.
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
	// 1) объект с полем internalSquads
	var obj struct {
		InternalSquads []Squad `json:"internalSquads"`
	}
	if json.Unmarshal(env.Response, &obj) == nil && len(obj.InternalSquads) > 0 {
		return obj.InternalSquads, nil
	}
	// 2) сразу массив
	var arr []Squad
	if json.Unmarshal(env.Response, &arr) == nil {
		return arr, nil
	}
	return nil, nil
}

// DisableByTelegramID отключает аккаунт пользователя в панели (POST /api/users/{uuid}/actions/disable).
// Жёсткое правило безопасности: трогаем ТОЛЬКО аккаунты, созданные этим ботом
// (Tag == BotTag или username == tg_<id>); чужие аккаунты не трогаем.
//
// Возвращает (true, nil), если аккаунт нашёлся и был отключён или уже отключён.
// Возвращает (false, nil), если в панели юзера нет — это не ошибка.
func (c *Client) DisableByTelegramID(ctx context.Context, telegramID int64) (bool, error) {
	u, err := c.findByTelegram(ctx, telegramID)
	if err != nil {
		return false, err
	}
	if u == nil || u.Uuid == "" {
		return false, nil
	}
	if !ownedByBot(u, telegramID) {
		return false, fmt.Errorf("аккаунт <code>%d</code> создан НЕ через бота — отключать его запрещено", telegramID)
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/users/"+u.Uuid+"/actions/disable", nil)
	if err != nil {
		return false, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return false, classifyHTTP(resp)
	}
	return true, nil
}

// Subscription возвращает ссылку на подписку пользователя (по telegramId), если он есть в панели.
func (c *Client) Subscription(ctx context.Context, telegramID int64) (string, bool) {
	u, err := c.findByTelegram(ctx, telegramID)
	if err != nil || u == nil || u.SubscriptionURL == "" {
		return "", false
	}
	return u.SubscriptionURL, true
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

func (c *Client) upsertCall(ctx context.Context, method, path string, body any) (string, error) {
	resp, err := c.do(ctx, method, path, body)
	if err != nil {
		return "", fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", classifyHTTP(resp)
	}
	var env struct {
		Response panelUser `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return "", err
	}
	return env.Response.SubscriptionURL, nil
}

// nextExpire — новая дата окончания: продлеваем от max(now, текущая) на months.
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

package remnawave

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
)

// SubpageConfig — конфигурация страницы подписки, как её хранит панель 3.x.
// Config — сырой JSON: там лежат platforms → apps → blocks → buttons, то же
// самое, что страница подписки отдаёт браузеру. Забрать его у панели надёжнее,
// чем ходить на саму страницу: обычный токен вместо cookie-сессии и защит.
type SubpageConfig struct {
	UUID         string          `json:"uuid"`
	Name         string          `json:"name"`
	ViewPosition int             `json:"viewPosition"`
	Config       json.RawMessage `json:"config"`
}

// errNoSubpageAPI сообщает, что панель не умеет отдавать конфиги страницы
// подписки (до 3.0.0). Не ошибка: вызывающий просто идёт старым путём.
var errNoSubpageAPI = fmt.Errorf("панель не отдаёт конфиги страницы подписки")

// ErrNoSubpageAPI reports whether the panel simply has no subpage-config API.
func ErrNoSubpageAPI(err error) bool { return err == errNoSubpageAPI }

// SubpageConfigs returns every subscription-page config the panel holds
// (GET /api/subscription-page-configs), ordered as the panel shows them.
func (c *Client) SubpageConfigs(ctx context.Context) ([]SubpageConfig, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/subscription-page-configs", nil)
	if err != nil {
		return nil, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNoSubpageAPI
	}
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTP(resp)
	}
	var env struct {
		Response struct {
			Total   int             `json:"total"`
			Configs []SubpageConfig `json:"configs"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("разбор ответа панели: %w", err)
	}
	out := env.Response.Configs
	sort.SliceStable(out, func(i, j int) bool { return out[i].ViewPosition < out[j].ViewPosition })
	return out, nil
}

// SubpageConfigUUIDFor reports which config is assigned to the subscription
// identified by shortUUID (GET /api/subscriptions/subpage-config/{shortUuid}).
// An empty uuid means the panel did not name one — use the default config.
func (c *Client) SubpageConfigUUIDFor(ctx context.Context, shortUUID string) (string, error) {
	if shortUUID == "" {
		return "", nil
	}
	path := "/api/subscriptions/subpage-config/" + url.PathEscape(shortUUID)
	// Панель ждёт заголовки исходного запроса, чтобы применить свои правила
	// ответа. У бота исходного запроса нет — шлём пустой набор.
	resp, err := c.do(ctx, http.MethodGet, path, map[string]any{"requestHeaders": map[string]string{}})
	if err != nil {
		return "", fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", errNoSubpageAPI
	}
	if resp.StatusCode != http.StatusOK {
		return "", classifyHTTP(resp)
	}
	var env struct {
		Response struct {
			SubpageConfigUUID *string `json:"subpageConfigUuid"`
			WebpageAllowed    bool    `json:"webpageAllowed"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return "", fmt.Errorf("разбор ответа панели: %w", err)
	}
	if env.Response.SubpageConfigUUID == nil {
		return "", nil
	}
	return *env.Response.SubpageConfigUUID, nil
}

// SubpageConfigFor returns the config that applies to the given subscription:
// the one the panel assigned to it, or the first one otherwise. A nil result
// with a nil error means the panel has no usable config.
func (c *Client) SubpageConfigFor(ctx context.Context, shortUUID string) (*SubpageConfig, error) {
	list, err := c.SubpageConfigs(ctx)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	// Персональный конфиг — не обязателен: если панель на этот вопрос не
	// отвечает, берём первый, он же дефолтный.
	if uuid, err := c.SubpageConfigUUIDFor(ctx, shortUUID); err == nil && uuid != "" {
		for i := range list {
			if list[i].UUID == uuid {
				if len(list[i].Config) == 0 {
					return nil, nil
				}
				return &list[i], nil
			}
		}
	}
	for i := range list {
		if len(list[i].Config) > 0 {
			return &list[i], nil
		}
	}
	return nil, nil
}

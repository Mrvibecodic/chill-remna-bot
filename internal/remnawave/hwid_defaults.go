package remnawave

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// HwidRule — как панель ограничивает устройства у пользователя без своего
// лимита (hwidDeviceLimit = null). Enabled=false — контроль устройств
// выключен, подключать можно сколько угодно.
type HwidRule struct {
	Enabled  bool
	Fallback int
}

// HwidDefaults — лимит устройств по умолчанию: общий из настроек подписки и
// свои у внешних сквадов. Правило сквада, если оно задано, целиком заменяет
// общее (так панель делает при выдаче подписки). ExtKnown=false — список
// внешних сквадов прочитать не удалось.
type HwidDefaults struct {
	Global   HwidRule
	Ext      map[string]HwidRule
	ExtKnown bool
}

// For — правило для подписки с внешним сквадом ext (пусто — без сквада).
// ok=false — правило неизвестно, и показывать нечего.
func (d HwidDefaults) For(ext string) (HwidRule, bool) {
	if ext != "" {
		if !d.ExtKnown {
			return HwidRule{}, false
		}
		if r, ok := d.Ext[ext]; ok {
			return r, true
		}
	}
	return d.Global, true
}

type panelHwidSettings struct {
	Enabled  *bool `json:"enabled"`
	Fallback *int  `json:"fallbackDeviceLimit"`
}

// parseHwidSettings — hwidSettings из ответа панели. set=false — настроек
// нет (null или пустой объект: для сквада это «как в общих настройках»).
func parseHwidSettings(raw json.RawMessage) (r HwidRule, set bool, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || bytes.Equal(raw, []byte("{}")) {
		return HwidRule{}, false, nil
	}
	var s panelHwidSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return HwidRule{}, false, err
	}
	if s.Enabled == nil || s.Fallback == nil {
		return HwidRule{}, false, errors.New("hwidSettings без enabled или fallbackDeviceLimit")
	}
	return HwidRule{Enabled: *s.Enabled, Fallback: *s.Fallback}, true, nil
}

// HwidDefaults читает лимит устройств по умолчанию: GET /api/subscription-settings
// (скоуп subscription-settings:get) и правила внешних сквадов из
// GET /api/external-squads. Ошибка — общее правило неизвестно; сбой списка
// сквадов ошибкой не считается, только снимает ExtKnown.
func (c *Client) HwidDefaults(ctx context.Context) (HwidDefaults, error) {
	var d HwidDefaults
	resp, err := c.do(ctx, http.MethodGet, "/api/subscription-settings", nil)
	if err != nil {
		return d, fmt.Errorf("нет связи с панелью: %w", err)
	}
	var env struct {
		Response struct {
			Hwid json.RawMessage `json:"hwidSettings"`
		} `json:"response"`
	}
	if resp.StatusCode != http.StatusOK {
		err = classifyHTTP(resp)
		resp.Body.Close()
		return d, err
	}
	err = json.NewDecoder(resp.Body).Decode(&env)
	resp.Body.Close()
	if err != nil {
		return d, fmt.Errorf("разбор ответа панели: %w", err)
	}
	g, set, err := parseHwidSettings(env.Response.Hwid)
	if err != nil {
		return d, fmt.Errorf("разбор hwidSettings: %w", err)
	}
	if !set {
		return d, errors.New("панель не отдала hwidSettings")
	}
	d.Global = g
	d.Ext, d.ExtKnown = c.extHwidRules(ctx)
	return d, nil
}

func (c *Client) extHwidRules(ctx context.Context) (map[string]HwidRule, bool) {
	resp, err := c.do(ctx, http.MethodGet, "/api/external-squads", nil)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var env struct {
		Response struct {
			Total *int `json:"total"`
			List  *[]struct {
				UUID string          `json:"uuid"`
				Hwid json.RawMessage `json:"hwidSettings"`
			} `json:"externalSquads"`
		} `json:"response"`
	}
	if json.NewDecoder(resp.Body).Decode(&env) != nil || env.Response.List == nil {
		return nil, false
	}
	list := *env.Response.List
	if env.Response.Total != nil && *env.Response.Total != len(list) {
		return nil, false
	}
	out := map[string]HwidRule{}
	for _, s := range list {
		if s.UUID == "" {
			return nil, false
		}
		r, set, err := parseHwidSettings(s.Hwid)
		if err != nil {
			return nil, false
		}
		if set {
			out[s.UUID] = r
		}
	}
	return out, true
}

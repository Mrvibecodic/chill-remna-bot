package app

import (
	"context"
	"errors"
	"fmt"
)

// Healthy реализует web.Handlers.Healthy: бот считается живым, когда есть
// открытое хранилище и валидный botCfg.Installed. Опрос панели опционален —
// если она временно недоступна, бот всё равно отвечает /healthz=200, чтобы
// прокси не считал нас мёртвыми.
func (a *App) Healthy(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store == nil {
		return errors.New("storage not initialised")
	}
	if a.botCfg == nil || !a.botCfg.Installed {
		return errors.New("bot not installed")
	}
	return nil
}

// HandleCryptoBotWebhook — обработчик POST /webhook/cryptobot (Phase 4).
func (a *App) HandleCryptoBotWebhook(_ context.Context, sig string, body []byte) (bool, error) {
	a.log.Info("cryptobot webhook received (phase 1 stub)", "sig_len", len(sig), "body_len", len(body))
	return false, nil
}

// webhookConfig возвращает копию конфига вебхука или пустую структуру.
func (a *App) WebhookConfig() (addr string, enabled bool, publicURL string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return ":8080", false, ""
	}
	addr = a.botCfg.Webhook.ListenAddr
	if addr == "" {
		addr = ":8080"
	}
	return addr, a.botCfg.Webhook.Enabled, a.botCfg.Webhook.PublicBaseURL
}

// PublicWebhookURL — публичный URL конкретного эндпоинта (для показа в админке).
func (a *App) PublicWebhookURL(path string) string {
	a.mu.Lock()
	base := ""
	if a.botCfg != nil {
		base = a.botCfg.Webhook.PublicBaseURL
	}
	a.mu.Unlock()
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s%s", base, path)
}

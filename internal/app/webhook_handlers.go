package app

import (
	"context"
	"errors"
	"path/filepath"
)

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

func (a *App) WebhookServer() (addr, domain, cacheDir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	addr = ":8080"
	if a.botCfg != nil {
		// Второй рубеж: в поле мог попасть мусор до того, как появилась
		// проверка при вводе. Отдать его в http.Server значит не поднять
		// веб-сервер вовсе — вебхуки всех платёжек, мини-апп и кабинет
		// мертвы, а причина видна только в логе.
		if norm, ok := normalizeListenAddr(a.botCfg.Webhook.ListenAddr); ok {
			addr = norm
		}
		if a.botCfg.Webhook.TLS && a.botCfg.Webhook.Domain != "" {
			domain = a.botCfg.Webhook.Domain
		}
	}
	cacheDir = filepath.Join(a.cfg.DataDir, "autocert")
	return
}

func (a *App) WebhookConfig() (addr string, enabled bool, publicURL string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return ":8080", false, ""
	}
	addr = ":8080"
	if norm, ok := normalizeListenAddr(a.botCfg.Webhook.ListenAddr); ok {
		addr = norm
	}
	if addr == "" {
		addr = ":8080"
	}
	return addr, a.botCfg.Webhook.Enabled, a.botCfg.Webhook.PublicBaseURL
}

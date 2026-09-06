package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// healthPingBudget — сколько ждём ответа базы. Заведомо меньше 3 секунд,
// отведённых обработчику: остаток нужен на запись ответа.
const healthPingBudget = 2 * time.Second

func (a *App) Healthy(ctx context.Context) error {
	a.mu.Lock()
	st := a.store
	installed := a.botCfg != nil && a.botCfg.Installed
	a.mu.Unlock()
	if st == nil {
		return errors.New("storage not initialised")
	}
	if !installed {
		return errors.New("bot not installed")
	}
	// Проверка «бот жив» без базы отвечала «всё хорошо» при отвалившемся
	// хранилище — оркестратор ничего не перезапускал. Пинг делается ВНЕ замка:
	// a.mu — главный замок приложения, и держать его на время недоступной
	// базы значило бы заодно подвесить весь бот.
	//
	// Панель здесь намеренно НЕ проверяется: её недоступность рестартом бота
	// не лечится, а рестарт посреди обработки вебхука прямо вредит.
	if ctx == nil {
		ctx = context.Background()
	}
	pctx, cancel := context.WithTimeout(ctx, healthPingBudget)
	defer cancel()
	if err := st.Ping(pctx); err != nil {
		return fmt.Errorf("база не отвечает: %w", err)
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

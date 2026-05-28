package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

// rwWebhookEvent — общий формат входящего вебхука панели Remnawave.
// Реальная панель шлёт `event` (snake_case) и `data` (объект с полями
// пользователя). Имена полей внутри `data` могут варьироваться в зависимости
// от версии панели — здесь принимаем наиболее распространённые алиасы
// (uuid/userId, telegramId, expireAt/expireTime). На все события смотрим
// одним типом, а специфичная обработка — в switch ниже.
type rwWebhookEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// rwUserPayload — облако возможных полей юзера панели.
type rwUserPayload struct {
	UUID       string `json:"uuid"`
	UserID     string `json:"userId"` // алиас uuid в некоторых билдах
	Username   string `json:"username"`
	TelegramID int64  `json:"telegramId"`
	ExpireAt   string `json:"expireAt"`
	ExpireTime string `json:"expireTime"` // legacy-имя
	Status     string `json:"status"`
}

// verifyRemnawaveSignature сравнивает X-Remnawave-Signature (hex) c HMAC-SHA256
// тела по WEBHOOK_SECRET_HEADER. Сравнение constant-time, чтобы не было
// тайминг-сайдчанелла. Если секрет не задан — валидация пропускается
// (для локальной отладки), но в продакшене обязательно установить его в
// «Управление → 🔗 Вебхуки → 🔑 Секрет панели».
func verifyRemnawaveSignature(signatureHex, secret string, body []byte) error {
	if secret == "" {
		return nil
	}
	if signatureHex == "" {
		return errors.New("remnawave webhook: signature header missing")
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signatureHex, "sha256="))
	if err != nil {
		return fmt.Errorf("remnawave webhook: bad signature hex: %w", err)
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	if !hmac.Equal(got, m.Sum(nil)) {
		return errors.New("remnawave webhook: signature mismatch")
	}
	return nil
}

// HandleRemnawaveWebhook — обработчик POST /webhook/remnawave (Phase 3).
//
// События панели, которые мы пушим юзеру:
//   - user.expired           → подписка истекла, кнопка «Продлить»
//   - user.expires_in_*h/d   → скоро истекает, превентивное напоминание
//   - user.limited           → трафик исчерпан, кнопка «Купить ещё»
//
// Все остальные события (user.created, user.disabled, …) логируются с
// handled=true, чтобы панель не ретраила бесконечно — но юзеру не пушим,
// иначе спам на любом действии админа.
func (a *App) HandleRemnawaveWebhook(ctx context.Context, signature string, body []byte) (bool, error) {
	a.mu.Lock()
	secret := ""
	if a.botCfg != nil {
		secret = a.botCfg.Webhook.RemnawaveSecret
	}
	a.mu.Unlock()
	if err := verifyRemnawaveSignature(signature, secret, body); err != nil {
		return false, err
	}

	var ev rwWebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return false, fmt.Errorf("remnawave webhook: bad json: %w", err)
	}
	if ev.Event == "" {
		return false, errors.New("remnawave webhook: missing event field")
	}

	var u rwUserPayload
	_ = json.Unmarshal(ev.Data, &u) // безопасно: пустые поля если data другой формы

	switch {
	case strings.HasPrefix(ev.Event, "user.expires_in"):
		// Префикс ловит user.expires_in_24h, user.expires_in_72h, в разных
		// версиях панели окно настраивается.
		a.pushExpiryWarning(ctx, u, ev.Event)
		return true, nil
	case ev.Event == "user.expired":
		a.pushExpired(ctx, u)
		return true, nil
	case ev.Event == "user.limited" || ev.Event == "user.traffic_used":
		a.pushTrafficLimited(ctx, u)
		return true, nil
	default:
		a.log.Info("remnawave webhook: event ignored", "event", ev.Event, "tg_id", u.TelegramID)
		return true, nil
	}
}

// pushExpiryWarning — мягкое уведомление «истекает через …, продлите».
func (a *App) pushExpiryWarning(ctx context.Context, u rwUserPayload, event string) {
	if u.TelegramID == 0 {
		return
	}
	lang := a.lang(u.TelegramID)
	a.notifyKB(ctx, u.TelegramID, i18n.T(lang, "rw.warn_expiring", event), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
	})
	a.log.Info("remnawave webhook: warn sent", "event", event, "tg_id", u.TelegramID)
}

// pushExpired — подписка истекла.
func (a *App) pushExpired(ctx context.Context, u rwUserPayload) {
	if u.TelegramID == 0 {
		return
	}
	a.invalidateSubCache(u.TelegramID)
	lang := a.lang(u.TelegramID)
	a.notifyKB(ctx, u.TelegramID, i18n.T(lang, "rw.expired"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
	})
	a.log.Info("remnawave webhook: expired notified", "tg_id", u.TelegramID)
}

// pushTrafficLimited — у юзера кончился трафик / лимит устройств.
func (a *App) pushTrafficLimited(ctx context.Context, u rwUserPayload) {
	if u.TelegramID == 0 {
		return
	}
	lang := a.lang(u.TelegramID)
	a.notifyKB(ctx, u.TelegramID, i18n.T(lang, "rw.limited"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
	})
	a.log.Info("remnawave webhook: limit notified", "tg_id", u.TelegramID)
}

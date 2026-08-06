package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

type rwWebhookEvent struct {
	Scope string          `json:"scope"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
	// Meta держим сырым: типизированный разбор ронял бы весь конверт (а с ним
	// и все прочие события) на любом неожиданном типе поля.
	Meta json.RawMessage `json:"meta"`
}

// meta приходит рядом с data, а не внутри неё (см. RemnawaveWebhookUserEvents).
// Expiration — интервал в ЧАСАХ со знаком: отрицательный = столько часов до
// истечения, положительный = столько часов после. Задаётся на панели в
// EXPIRATION_NOTIFICATIONS (диапазон -744..744).
type rwWebhookMeta struct {
	Expiration             *int `json:"expiration"`
	NotConnectedAfterHours *int `json:"notConnectedAfterHours"`
}

// rwUserPayload is the subset of the panel's user object the bot needs. Panel
// 3.0.0 dropped uuid and made the id numeric, so both identifier fields are
// read as raw JSON: they are only informational here — everything the bot acts
// on comes from telegramId/username, which both generations still send.
type rwUserPayload struct {
	UUID       json.RawMessage `json:"uuid"`
	UserID     json.RawMessage `json:"id"`
	Username   string          `json:"username"`
	TelegramID int64           `json:"telegramId"`
	ExpireAt   string          `json:"expireAt"`
	ExpireTime string          `json:"expireTime"`
	Status     string          `json:"status"`
}

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

func (a *App) HandleRemnawaveWebhook(ctx context.Context, signature string, body []byte) (bool, error) {
	a.mu.Lock()
	secret := ""
	if a.botCfg != nil {
		secret = strings.TrimSpace(a.botCfg.Webhook.RemnawaveSecret)
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
	_ = json.Unmarshal(ev.Data, &u)

	switch {
	case ev.Event == "user.expiration":
		// Панель с 2.8.20: одно событие вместо user.expires_in_*/user.expired_*_ago,
		// конкретный интервал лежит в meta.expiration.
		hours := 0
		var meta rwWebhookMeta
		if len(ev.Meta) > 0 {
			if err := json.Unmarshal(ev.Meta, &meta); err != nil {
				// Интервал не прочитан — событие всё равно обрабатываем, но
				// текст будет без числа: молчать хуже.
				a.log.Warn("remnawave webhook: meta не разобрана", "event", ev.Event, "err", err)
			} else if meta.Expiration != nil {
				hours = *meta.Expiration
			}
		}
		if hours > 0 {
			// Подписка истекла hours часов назад — напоминание продлить.
			a.pushExpired(ctx, u)
			return true, nil
		}
		a.pushExpiryWarning(ctx, u, ev.Event, -hours)
		return true, nil
	case strings.HasPrefix(ev.Event, "user.expires_in"):
		// Панели 2.7.0–2.8.19.
		a.pushExpiryWarning(ctx, u, ev.Event, expiresInHours(ev.Event))
		return true, nil
	case ev.Event == "user.expired":
		a.pushExpired(ctx, u)
		return true, nil
	case ev.Event == "user.limited" || ev.Event == "user.bandwidth_usage_threshold_reached":
		a.pushTrafficLimited(ctx, u)
		return true, nil
	case ev.Event == "torrent_blocker.report":
		a.pushTorrentReport(ctx, ev.Data)
		return true, nil
	default:
		a.log.Info("remnawave webhook: event ignored", "scope", ev.Scope, "event", ev.Event, "tg_id", u.TelegramID)
		return true, nil
	}
}

func (a *App) pushExpiryWarning(ctx context.Context, u rwUserPayload, event string, hours int) {
	if u.TelegramID == 0 {
		return
	}
	lang := a.lang(u.TelegramID)
	text := i18n.T(lang, "rw.warn_expiring")
	switch {
	case hours >= 48 && hours%24 == 0:
		text = i18n.T(lang, "rw.warn_expiring_days", hours/24)
	case hours > 0:
		text = i18n.T(lang, "rw.warn_expiring_hours", hours)
	}
	a.notifyKB(ctx, u.TelegramID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
	})
	a.log.Info("remnawave webhook: warn sent", "event", event, "tg_id", u.TelegramID)
}

func expiresInHours(event string) int {
	s := strings.TrimSuffix(strings.TrimPrefix(event, "user.expires_in_"), "_hours")
	n, _ := strconv.Atoi(s)
	return n
}

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

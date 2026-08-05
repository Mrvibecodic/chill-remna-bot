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
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
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
	_ = json.Unmarshal(ev.Data, &u)

	switch {
	case strings.HasPrefix(ev.Event, "user.expires_in"):

		a.pushExpiryWarning(ctx, u, ev.Event)
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
		a.log.Info("remnawave webhook: event ignored", "event", ev.Event, "tg_id", u.TelegramID)
		return true, nil
	}
}

func (a *App) pushExpiryWarning(ctx context.Context, u rwUserPayload, event string) {
	if u.TelegramID == 0 {
		return
	}
	lang := a.lang(u.TelegramID)
	text := i18n.T(lang, "rw.warn_expiring")
	if h := expiresInHours(event); h > 0 {
		text = i18n.T(lang, "rw.warn_expiring_hours", h)
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

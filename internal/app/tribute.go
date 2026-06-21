package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func (a *App) tributeCfg() model.TributeConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.TributeConfig{}
	}
	return a.botCfg.Tribute
}

func (a *App) startTribute(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.tributeCfg()
	if !cfg.Enabled || cfg.PayURL == "" {
		a.sendHome(ctx, chatID, i18n.T(lang, "trb.not_configured"))
		return
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "trb.pay_prompt"), [][]models.InlineKeyboardButton{
		{{Text: i18n.T(lang, "trb.btn_pay"), URL: cfg.PayURL}},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func tributePeriodToMonths(period string) int {
	p := strings.ToLower(period)
	switch {
	case strings.Contains(p, "year") || strings.Contains(p, "annual") || strings.Contains(p, "12"):
		return 12
	case strings.Contains(p, "6") || strings.Contains(p, "half"):
		return 6
	case strings.Contains(p, "3") || strings.Contains(p, "quart"):
		return 3
	default:
		return 1
	}
}

type tributeWebhook struct {
	Name    string `json:"name"`
	Payload struct {
		SubscriptionID int       `json:"subscription_id"`
		Period         string    `json:"period"`
		Amount         int       `json:"amount"`
		Currency       string    `json:"currency"`
		TelegramUserID int64     `json:"telegram_user_id"`
		ExpiresAt      time.Time `json:"expires_at"`
	} `json:"payload"`
}

func (a *App) HandleTributeWebhook(ctx context.Context, signatureHex string, body []byte) (bool, error) {
	cfg := a.tributeCfg()
	if !cfg.Enabled || cfg.APIKey == "" {
		a.log.Warn("tribute webhook: ignored — tribute disabled or api key not set")
		return true, nil
	}
	mac := hmac.New(sha256.New, []byte(cfg.APIKey))
	mac.Write(body)
	got, err := hex.DecodeString(strings.TrimSpace(signatureHex))
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		a.log.Warn("tribute webhook: bad signature")
		return false, fmt.Errorf("tribute webhook: invalid signature")
	}
	var wh tributeWebhook
	if err := json.Unmarshal(body, &wh); err != nil {
		return false, fmt.Errorf("tribute webhook: bad json: %w", err)
	}
	if wh.Name != "new_subscription" && wh.Name != "renewed_subscription" {
		a.log.Info("tribute webhook: ignored", "event", wh.Name)
		return true, nil
	}
	chatID := wh.Payload.TelegramUserID
	if chatID == 0 {
		a.log.Warn("tribute webhook: no telegram_user_id")
		return true, nil
	}
	months := tributePeriodToMonths(wh.Payload.Period)
	extID := fmt.Sprintf("trb_%d_%d", wh.Payload.SubscriptionID, wh.Payload.ExpiresAt.Unix())
	a.payLog(ctx, model.PayMethodTribute, extID, chatID, "webhook", "%s period=%s amount=%d %s", wh.Name, wh.Payload.Period, wh.Payload.Amount, wh.Payload.Currency)
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, extID); done {
			a.payLog(ctx, model.PayMethodTribute, extID, chatID, "duplicate", "уже финализирован, вебхук пропущен")
			return true, nil
		}
	}
	amount := fmt.Sprintf("%d %s", wh.Payload.Amount, wh.Payload.Currency)
	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodTribute, amount, extID)
	if err != nil {
		return false, fmt.Errorf("tribute finalize %s: %w", extID, err)
	}
	a.sendSubActive(ctx, chatID, link, expireAt)
	a.log.Info("tribute webhook: finalized", "chat_id", chatID, "months", months)
	return true, nil
}

func (a *App) showTributeAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.tributeCfg()
	status := i18n.T(lang, "admin.off")
	if cfg.Enabled {
		status = i18n.T(lang, "admin.on")
	}
	key := i18n.T(lang, "admin.no")
	if cfg.APIKey != "" {
		key = i18n.T(lang, "admin.yes")
	}
	url := cfg.PayURL
	if url == "" {
		url = i18n.T(lang, "admin.none")
	}
	text := i18n.T(lang, "trb.title", status, key, url)
	a.sendPayKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{toggleBtn(lang, cfg.Enabled, "trb:toggle")},
		{btn(i18n.T(lang, "trb.btn_key"), "trb:key"), btn(i18n.T(lang, "trb.btn_url"), "trb:url")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onTributeAdmin(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	switch val {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Tribute.Enabled = !a.botCfg.Tribute.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showTributeAdmin(ctx, chatID)
	case "key":
		a.getUI(chatID).adminInput = "trb_key"
		a.askInput(ctx, chatID, i18n.T(lang, "trb.ask_key"), "menu:tribute")
	case "url":
		a.getUI(chatID).adminInput = "trb_url"
		a.askInput(ctx, chatID, i18n.T(lang, "trb.ask_url"), "menu:tribute")
	}
}

func (a *App) setTributeField(ctx context.Context, chatID int64, field, text string) {
	text = strings.TrimSpace(text)
	a.mu.Lock()
	if a.botCfg != nil {
		switch field {
		case "trb_key":
			a.botCfg.Tribute.APIKey = text
		case "trb_url":
			a.botCfg.Tribute.PayURL = text
		}
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showTributeAdmin(ctx, chatID)
}

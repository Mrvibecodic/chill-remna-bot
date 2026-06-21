package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/platega"
)

func (a *App) plConfig() model.PlategaConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.PlategaConfig{}
	}
	return a.botCfg.Platega
}

func (a *App) plClient() *platega.Client {
	cfg := a.plConfig()
	if cfg.MerchantID == "" || cfg.Secret == "" {
		return nil
	}
	return platega.New(cfg.MerchantID, cfg.Secret)
}

func (a *App) plMethod() int {
	m := a.plConfig().Method
	if m == platega.MethodCards {
		return platega.MethodCards
	}
	return platega.MethodSBP
}

func plPayload(chatID int64, months int) string {
	return fmt.Sprintf("telegram_id=%d&months=%d", chatID, months)
}

func parsePlPayload(payload string) (telegramID int64, months int) {
	for _, part := range strings.Split(payload, "&") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch k {
		case "telegram_id":
			telegramID, _ = strconv.ParseInt(v, 10, 64)
		case "months":
			months, _ = strconv.Atoi(v)
		}
	}
	return
}

func (a *App) startPlatega(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	months := a.getUI(chatID).buyMonths
	if months == 0 {
		months = model.PlanMonths[0]
	}
	cfg := a.plConfig()
	pr := a.pricing()
	value := pr.Fiat(model.PayMethodPlatega, months)
	if !cfg.Enabled || value == "" {
		a.sendHome(ctx, chatID, i18n.T(lang, "pl.no_price"))
		return
	}
	client := a.plClient()
	if client == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "pl.not_configured"))
		return
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	returnURL := cfg.ReturnURL
	if returnURL == "" {
		returnURL = "https://t.me"
	}
	amount := parseAmountRub(value)
	desc := i18n.T(lang, "pl.invoice_desc", months)
	redirect, txID, err := a.plCreateTransaction(ctx, chatID, months, amount, desc, returnURL)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "pl.fail", err.Error()))
		return
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "pl.pay_prompt", months, value+curSuffix(curRUB)), [][]models.InlineKeyboardButton{
		{{Text: i18n.T(lang, "pl.btn_pay"), URL: redirect}},
		{btn(i18n.T(lang, "pl.btn_check"), "plc:"+txID)},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onPLCheck(ctx context.Context, chatID int64, txID string) {
	lang := a.lang(chatID)
	client := a.plClient()
	if client == nil || txID == "" {
		return
	}
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, txID); done {
			a.showMySubs(ctx, chatID)
			return
		}
	}
	tx, err := client.GetTransaction(ctx, txID)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "pl.fail", err.Error()))
		return
	}
	a.payLog(ctx, model.PayMethodPlatega, txID, chatID, "manual_check", "status=%s", tx.Status)
	if !strings.EqualFold(tx.Status, "CONFIRMED") {
		a.sendKB(ctx, chatID, i18n.T(lang, "pl.pending"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "pl.btn_check"), "plc:"+txID)},
			{btn(i18n.T(lang, "btn.home"), "menu:home")},
		})
		return
	}
	a.finalizePlatega(ctx, txID, tx)
}

func (a *App) finalizePlatega(ctx context.Context, txID string, tx *platega.Transaction) {
	if a.store == nil {
		return
	}
	amount := fmt.Sprintf("%.2f %s", tx.Amount, tx.Currency)
	if p, _ := a.store.PendingByExtID(ctx, txID); p != nil && p.Purpose == "topup" {
		_ = a.finalizeTopUp(ctx, p.TelegramID, p.Kopecks, model.PayMethodPlatega, amount, txID)
		_ = a.store.ResolvePending(ctx, p.ID)
		return
	}
	chatID, months := parsePlPayload(tx.Payload)
	if chatID == 0 {
		if p, _ := a.store.PendingByExtID(ctx, txID); p != nil {
			chatID, months = p.TelegramID, p.Months
		}
	}
	if months == 0 {
		months = model.PlanMonths[0]
	}
	if chatID == 0 {
		a.payLog(ctx, model.PayMethodPlatega, txID, 0, "error", "оплата подтверждена, но получатель неизвестен: нет payload и pending-счёта")
		return
	}
	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodPlatega, amount, txID)
	if err != nil {
		a.log.Error("platega finalize", "err", err, "tx", txID)
		return
	}
	a.sendSubActive(ctx, chatID, link, expireAt)
}

func (a *App) showPlategaAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.plConfig()
	status := i18n.T(lang, "admin.off")
	if cfg.Enabled {
		status = i18n.T(lang, "admin.on")
	}
	merchant := cfg.MerchantID
	if merchant == "" {
		merchant = i18n.T(lang, "admin.none")
	}
	secret := i18n.T(lang, "admin.no")
	if cfg.Secret != "" {
		secret = i18n.T(lang, "admin.yes")
	}
	ret := cfg.ReturnURL
	if ret == "" {
		ret = i18n.T(lang, "admin.none")
	}
	method := i18n.T(lang, "pl.method_sbp")
	if cfg.Method == platega.MethodCards {
		method = i18n.T(lang, "pl.method_cards")
	}
	text := i18n.T(lang, "pl.title", status, merchant, secret, ret, method)
	a.sendPayKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{toggleBtn(lang, cfg.Enabled, "pl:toggle"), btn(i18n.T(lang, "pl.btn_method"), "pl:method")},
		{btn(i18n.T(lang, "pl.btn_merchant"), "pl:merchant"), btn(i18n.T(lang, "pl.btn_secret"), "pl:secret")},
		{btn(i18n.T(lang, "pl.btn_return"), "pl:return")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onPlategaAdmin(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	switch val {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Platega.Enabled = !a.botCfg.Platega.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showPlategaAdmin(ctx, chatID)
	case "method":
		a.mu.Lock()
		if a.botCfg != nil {
			if a.botCfg.Platega.Method == platega.MethodCards {
				a.botCfg.Platega.Method = platega.MethodSBP
			} else {
				a.botCfg.Platega.Method = platega.MethodCards
			}
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showPlategaAdmin(ctx, chatID)
	case "merchant":
		a.getUI(chatID).adminInput = "pl_merchant"
		a.askInput(ctx, chatID, i18n.T(lang, "pl.ask_merchant"), "menu:platega")
	case "secret":
		a.getUI(chatID).adminInput = "pl_secret"
		a.askInput(ctx, chatID, i18n.T(lang, "pl.ask_secret"), "menu:platega")
	case "return":
		a.getUI(chatID).adminInput = "pl_return"
		a.askInput(ctx, chatID, i18n.T(lang, "pl.ask_return"), "menu:platega")
	}
}

func (a *App) setPlategaField(ctx context.Context, chatID int64, field, text string) {
	text = strings.TrimSpace(text)
	a.mu.Lock()
	if a.botCfg != nil {
		switch field {
		case "pl_merchant":
			a.botCfg.Platega.MerchantID = text
		case "pl_secret":
			a.botCfg.Platega.Secret = text
		case "pl_return":
			a.botCfg.Platega.ReturnURL = text
		}
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showPlategaAdmin(ctx, chatID)
}

// plCreateTransaction creates a Platega transaction + pending record and
// returns the redirect URL. Shared by chat flow and Mini App.
func (a *App) plCreateTransaction(ctx context.Context, chatID int64, months int, amount float64, desc, returnURL string) (redirect, txID string, err error) {
	client := a.plClient()
	if client == nil {
		return "", "", fmt.Errorf("platega не настроена")
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	tx, err := client.CreateTransaction(ctx, a.plMethod(), amount, "RUB", desc, returnURL, plPayload(chatID, months))
	if err != nil {
		a.payLog(ctx, model.PayMethodPlatega, "", chatID, "invoice_error", "purchase months=%d: %v", months, err)
		return "", "", err
	}
	a.payLog(ctx, model.PayMethodPlatega, tx.ID, chatID, "invoice_created", "purchase months=%d amount=%.2f RUB method=%d", months, amount, a.plMethod())
	if a.store != nil {
		_ = a.store.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodPlatega, ExtID: tx.ID, TelegramID: chatID, Months: months})
	}
	return tx.Redirect, tx.ID, nil
}

package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/yookassa"
)

func (a *App) ykConfig() model.YooKassaConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.YooKassaConfig{}
	}
	return a.botCfg.YooKassa
}

func (a *App) ykClient() *yookassa.Client {
	cfg := a.ykConfig()
	if cfg.ShopID == "" || cfg.SecretKey == "" {
		return nil
	}
	return yookassa.New(cfg.ShopID, cfg.SecretKey)
}

// --- пользователь: оплата картой через ЮKassa ---

func (a *App) startYooKassa(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	months := a.getUI(chatID).buyMonths
	if months == 0 {
		months = model.PlanMonths[0]
	}
	cfg := a.ykConfig()
	pr := a.pricing()
	value := pr.Fiat(model.PayMethodYooKassa, months)
	if !cfg.Enabled || value == "" {
		a.send(ctx, chatID, i18n.T(lang, "yk.no_price"))
		return
	}
	client := a.ykClient()
	if client == nil {
		a.send(ctx, chatID, i18n.T(lang, "yk.not_configured"))
		return
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	returnURL := cfg.ReturnURL
	if returnURL == "" {
		returnURL = "https://t.me"
	}
	currency := pr.Currency
	if currency == "" || len(currency) != 3 {
		currency = "RUB"
	}
	desc := i18n.T(lang, "yk.invoice_desc", months)
	pay, err := client.CreatePayment(ctx, value, currency, desc, returnURL, chatID, months)
	if err != nil {
		a.send(ctx, chatID, i18n.T(lang, "yk.fail", err.Error()))
		return
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "yk.pay_prompt", months, value+curSuffix(pr.Currency)), [][]models.InlineKeyboardButton{
		{{Text: i18n.T(lang, "yk.btn_pay"), URL: pay.Confirmation.ConfirmationURL}},
		{btn(i18n.T(lang, "yk.btn_check"), "ykc:"+pay.ID)},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// onYKCheck опрашивает статус платежа и при успехе активирует подписку.
func (a *App) onYKCheck(ctx context.Context, chatID int64, payID string) {
	lang := a.lang(chatID)
	client := a.ykClient()
	if client == nil || payID == "" {
		return
	}
	// идемпотентность: если платёж уже проведён — просто отдадим ссылку.
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, payID); done {
			a.showMySubs(ctx, chatID)
			return
		}
	}
	pay, err := client.GetPayment(ctx, payID)
	if err != nil {
		a.send(ctx, chatID, i18n.T(lang, "yk.fail", err.Error()))
		return
	}
	if pay.Status != "succeeded" {
		a.sendKB(ctx, chatID, i18n.T(lang, "yk.pending"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "yk.btn_check"), "ykc:"+payID)},
			{btn(i18n.T(lang, "btn.home"), "menu:home")},
		})
		return
	}
	months, _ := strconv.Atoi(pay.Metadata["months"])
	if months == 0 {
		months = model.PlanMonths[0]
	}
	amount := pay.Amount.Value + " " + pay.Amount.Currency
	link, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodYooKassa, amount, payID)
	if err != nil {
		a.send(ctx, chatID, i18n.T(lang, "yk.fail", err.Error()))
		return
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "yk.paid_ok", link), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// --- админ: настройки ЮKassa ---

func (a *App) showYooKassaAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.ykConfig()
	status := i18n.T(lang, "admin.off")
	if cfg.Enabled {
		status = i18n.T(lang, "admin.on")
	}
	shop := cfg.ShopID
	if shop == "" {
		shop = i18n.T(lang, "admin.none")
	}
	secret := i18n.T(lang, "admin.no")
	if cfg.SecretKey != "" {
		secret = i18n.T(lang, "admin.yes")
	}
	ret := cfg.ReturnURL
	if ret == "" {
		ret = i18n.T(lang, "admin.none")
	}
	text := i18n.T(lang, "admin.yk_title", status, shop, secret, ret, a.formatFiatPrices(model.PayMethodYooKassa))
	a.sendKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "admin.btn_toggle"), "yk:toggle"), btn(i18n.T(lang, "admin.btn_prices"), "yk:prices")},
		{btn(i18n.T(lang, "admin.yk_btn_shop"), "yk:shop"), btn(i18n.T(lang, "admin.yk_btn_secret"), "yk:secret")},
		{btn(i18n.T(lang, "admin.yk_btn_return"), "yk:return")},
		homeRow(lang),
	})
}

func (a *App) onYKAdmin(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	lang := a.lang(chatID)
	switch action {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.YooKassa.Enabled = !a.botCfg.YooKassa.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooKassaAdmin(ctx, chatID)
	case "shop":
		a.getUI(chatID).adminInput = "yk_shop"
		a.askInput(ctx, chatID, i18n.T(lang, "admin.yk_ask_shop"), "menu:yookassa")
	case "secret":
		a.getUI(chatID).adminInput = "yk_secret"
		a.askInput(ctx, chatID, i18n.T(lang, "admin.yk_ask_secret"), "menu:yookassa")
	case "return":
		a.getUI(chatID).adminInput = "yk_return"
		a.askInput(ctx, chatID, i18n.T(lang, "admin.yk_ask_return"), "menu:yookassa")
	case "prices":
		a.askPriceMonth(ctx, chatID, "yk")
	case "price":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "ykprice"
		ui.priceMonths = mo
		a.askInput(ctx, chatID, i18n.T(lang, "admin.yk_ask_price", mo), "menu:yookassa")
	}
}

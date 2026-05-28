// Пользовательский и админский флоу оплаты через CryptoBot (@CryptoBot,
// pay.crypt.bot). Создание инвойса, кнопка mini-app оплаты, fallback-кнопка
// «Проверить оплату» на случай недоставки вебхука. Финальная активация
// подписки — в HandleCryptoBotWebhook (см. webhook_cryptobot.go).
package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/cryptobot"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/storage"
)

func (a *App) cbConfig() model.CryptoBotConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.CryptoBotConfig{}
	}
	return a.botCfg.CryptoBot
}

func (a *App) cbClient() *cryptobot.Client {
	cfg := a.cbConfig()
	if !cfg.Enabled || cfg.Token == "" {
		return nil
	}
	return cryptobot.New(cfg.Token)
}

// --- пользователь: создание инвойса ---

func (a *App) startCryptoBot(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	months := a.getUI(chatID).buyMonths
	if months == 0 {
		months = model.PlanMonths[0]
	}
	cfg := a.cbConfig()
	price := cfg.Prices[months]
	if !cfg.Enabled || price == "" {
		a.send(ctx, chatID, i18n.T(lang, "cb.no_price"))
		return
	}
	client := a.cbClient()
	if client == nil {
		a.send(ctx, chatID, i18n.T(lang, "cb.not_configured"))
		return
	}
	asset := cfg.Asset
	if asset == "" {
		asset = "USDT"
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	inv, err := client.CreateInvoice(ctx, asset, price, chatID, months)
	if err != nil {
		a.send(ctx, chatID, i18n.T(lang, "cb.fail", err.Error()))
		return
	}
	if a.store != nil {
		_ = a.store.AddPendingInvoice(ctx, &model.PendingInvoice{
			Method: model.PayMethodCryptoBot, ExtID: "cb:" + strconv.FormatInt(inv.InvoiceID, 10),
			TelegramID: chatID, Months: months,
		})
	}
	payURL := inv.MiniAppInvoiceURL
	if payURL == "" {
		payURL = inv.BotInvoiceURL
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "cb.pay_prompt", months, price+" "+asset), [][]models.InlineKeyboardButton{
		{{Text: i18n.T(lang, "cb.btn_pay"), URL: payURL}},
		{btn(i18n.T(lang, "cb.btn_check"), "cbc:"+strconv.FormatInt(inv.InvoiceID, 10)+":"+strconv.Itoa(months))},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// onCBCheck — polling-fallback на случай, если вебхук CryptoBot не дошёл.
// Формат val: "<invoice_id>:<months>".
func (a *App) onCBCheck(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	client := a.cbClient()
	if client == nil {
		return
	}
	idStr, mosStr, _ := strings.Cut(val, ":")
	invoiceID, _ := strconv.ParseInt(idStr, 10, 64)
	months, _ := strconv.Atoi(mosStr)
	if invoiceID == 0 || months == 0 {
		return
	}
	extID := "cb:" + strconv.FormatInt(invoiceID, 10)
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, extID); done {
			a.showMySubs(ctx, chatID)
			return
		}
	}
	inv, err := client.GetInvoice(ctx, invoiceID)
	if err != nil {
		a.send(ctx, chatID, i18n.T(lang, "cb.fail", err.Error()))
		return
	}
	if inv.Status != "paid" {
		a.sendKB(ctx, chatID, i18n.T(lang, "cb.pending"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "cb.btn_check"), "cbc:"+idStr+":"+mosStr)},
			{btn(i18n.T(lang, "btn.home"), "menu:home")},
		})
		return
	}
	amount := inv.Amount + " " + inv.Asset
	link, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodCryptoBot, amount, extID)
	if err != nil && err != storage.ErrDuplicateExtID {
		a.send(ctx, chatID, i18n.T(lang, "cb.fail", err.Error()))
		return
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "cb.paid_ok", link), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// --- админ: настройки CryptoBot ---

func (a *App) showCryptoBotAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.cbConfig()
	status := i18n.T(lang, "admin.off")
	if cfg.Enabled {
		status = i18n.T(lang, "admin.on")
	}
	tok := i18n.T(lang, "admin.no")
	if cfg.Token != "" {
		tok = i18n.T(lang, "admin.yes")
	}
	asset := cfg.Asset
	if asset == "" {
		asset = i18n.T(lang, "admin.none")
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "admin.cb_title", status, tok, asset, a.formatCBPrices()), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "admin.btn_toggle"), "cb:toggle"), btn(i18n.T(lang, "admin.btn_prices"), "cb:prices")},
		{btn(i18n.T(lang, "admin.cb_btn_token"), "cb:token"), btn(i18n.T(lang, "admin.cb_btn_asset"), "cb:asset")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onCBAdmin(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	lang := a.lang(chatID)
	switch action {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.CryptoBot.Enabled = !a.botCfg.CryptoBot.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showCryptoBotAdmin(ctx, chatID)
	case "token":
		a.getUI(chatID).adminInput = "cb_token"
		a.askInput(ctx, chatID, i18n.T(lang, "admin.cb_ask_token"), "menu:cryptobot")
	case "asset":
		a.getUI(chatID).adminInput = "cb_asset"
		a.askInput(ctx, chatID, i18n.T(lang, "admin.cb_ask_asset"), "menu:cryptobot")
	case "prices":
		a.askPriceMonth(ctx, chatID, "cb")
	case "price":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "cbprice"
		ui.priceMonths = mo
		a.askInput(ctx, chatID, i18n.T(lang, "admin.cb_ask_price", mo), "menu:cryptobot")
	}
}

func (a *App) formatCBPrices() string {
	cfg := a.cbConfig()
	var parts []string
	for _, mo := range model.PlanMonths {
		if v := cfg.Prices[mo]; v != "" {
			parts = append(parts, strconv.Itoa(mo)+"м="+v)
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

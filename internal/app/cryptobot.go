package app

import (
	"context"
	"errors"
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

func (a *App) cryptoAmount(months int, fallback string) string {
	if p := a.pricing().Base[months]; p != "" {
		return p + curSuffix(curSymbol(a.hlCurrency()))
	}
	return fallback
}

func cbAmount(asset, amount, paidAsset, paidAmount, fiat string) string {
	switch {
	case asset != "":
		return amount + " " + asset
	case paidAsset != "":
		return paidAmount + " " + paidAsset
	case fiat != "":
		return amount + " " + fiat
	}
	return amount
}

func (a *App) cbClient() *cryptobot.Client {
	cfg := a.cbConfig()
	if !cfg.Enabled || cfg.Token == "" {
		return nil
	}
	return cryptobot.New(cfg.Token)
}

func (a *App) startCryptoBot(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	s := a.saleOrAsk(ctx, chatID)
	if s == nil {
		return
	}
	months := s.Months
	cfg := a.cbConfig()
	price := a.saleBase(s)
	if !cfg.Enabled || price == "" {
		a.sendHome(ctx, chatID, i18n.T(lang, "cb.no_price"))
		return
	}
	client := a.cbClient()
	if client == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "cb.not_configured"))
		return
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	payURL, invoiceID, err := a.cbCreateInvoiceSnap(ctx, chatID, months, price, false, a.saleSnapshot(s))
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "cb.fail", err.Error()))
		return
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "cb.pay_prompt", months, price+curSuffix(curSymbol(a.hlCurrency()))), [][]models.InlineKeyboardButton{
		{{Text: i18n.T(lang, "cb.btn_pay"), URL: payURL}},
		{btn(i18n.T(lang, "cb.btn_check"), "cbc:"+strconv.FormatInt(invoiceID, 10)+":"+strconv.Itoa(months))},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onCBCheck(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	client := a.cbClient()
	if client == nil {
		return
	}
	idStr, mosStr, _ := strings.Cut(val, ":")
	invoiceID, _ := strconv.ParseInt(idStr, 10, 64)
	if invoiceID == 0 {
		return
	}
	extID := "cb:" + strconv.FormatInt(invoiceID, 10)
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, extID); done {
			a.showMySubs(ctx, chatID)
			return
		}
	}

	if a.store != nil {
		if p, _ := a.store.PendingByExtID(ctx, extID); p != nil && p.Purpose == "topup" {
			inv, err := client.GetInvoice(ctx, invoiceID)
			if err != nil {
				a.sendHome(ctx, chatID, i18n.T(lang, "cb.fail", err.Error()))
				return
			}
			a.payLog(ctx, model.PayMethodCryptoBot, extID, chatID, "manual_check", "topup status=%s", inv.Status)
			if inv.Status != "paid" {
				a.sendKB(ctx, chatID, i18n.T(lang, "cb.pending"), [][]models.InlineKeyboardButton{
					{btn(i18n.T(lang, "cb.btn_check"), "cbc:"+idStr+":"+mosStr)},
					{btn(i18n.T(lang, "btn.home"), "menu:home")},
				})
				return
			}
			_ = a.finalizeTopUp(ctx, p.TelegramID, p.Kopecks, model.PayMethodCryptoBot,
				cbAmount(inv.Asset, inv.Amount, inv.PaidAsset, inv.PaidAmount, inv.Fiat), extID)
			_ = a.store.ResolvePending(ctx, p.ID)
			return
		}
	}
	inv, err := client.GetInvoice(ctx, invoiceID)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "cb.fail", err.Error()))
		return
	}
	a.payLog(ctx, model.PayMethodCryptoBot, extID, chatID, "manual_check", "status=%s", inv.Status)
	if inv.Status != "paid" {
		a.sendKB(ctx, chatID, i18n.T(lang, "cb.pending"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "cb.btn_check"), "cbc:"+idStr+":"+mosStr)},
			{btn(i18n.T(lang, "btn.home"), "menu:home")},
		})
		return
	}
	payChat, months, perr := parseCryptoBotPayload(inv.Payload)
	if perr != nil {
		a.log.Error("cryptobot check: bad invoice payload", "invoice", invoiceID, "err", perr)
		return
	}
	rawAmount := cbAmount(inv.Asset, inv.Amount, inv.PaidAsset, inv.PaidAmount, inv.Fiat)
	if months <= 0 {
		// «tg:0» — это оплаченное пополнение, у которого не осталось строки
		// счёта. Ветка вебхука зовёт админа, и ручная кнопка обязана вести
		// себя так же: иначе она продлит подписку на ноль месяцев и закроет
		// ext_id, после чего деньги не зачислить уже никак.
		a.payLog(ctx, model.PayMethodCryptoBot, extID, payChat, "error", "оплаченное пополнение без pending-записи (сумма %s) — зачислите вручную", rawAmount)
		alang := a.lang(a.cfg.AdminID)
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "cb.admin_lost_topup", extID, rawAmount, a.userLabelByID(ctx, payChat)))
		a.sendHome(ctx, chatID, i18n.T(lang, "pay.no_period"))
		return
	}
	amount := a.cryptoAmount(months, rawAmount)
	link, expireAt, err := a.finalizePurchase(ctx, payChat, months, model.PayMethodCryptoBot, amount, extID, a.pendingSnapshot(ctx, extID))
	if err != nil {
		if errors.Is(err, storage.ErrDuplicateExtID) {
			a.showMySubs(ctx, chatID)
			return
		}
		a.sendHome(ctx, chatID, i18n.T(lang, "cb.fail", err.Error()))
		return
	}
	a.sendSubActive(ctx, payChat, link, expireAt)
}

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
	a.sendPayKB(ctx, chatID, i18n.T(lang, "admin.cb_title", status, tok, asset), [][]models.InlineKeyboardButton{
		{toggleBtn(lang, cfg.Enabled, "cb:toggle")},
		{btn(i18n.T(lang, "admin.cb_btn_token"), "cb:token"), btn(i18n.T(lang, "admin.cb_btn_asset"), "cb:asset")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onCBAdmin(ctx context.Context, chatID int64, val string) {
	action, _, _ := strings.Cut(val, ":")
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
	}
}

// cbCreateInvoice creates a CryptoBot invoice + pending record and returns the
// pay URL and invoice id. Shared by chat flow and Mini App.
func (a *App) cbCreateInvoice(ctx context.Context, chatID int64, months int, price string, web bool) (string, int64, error) {
	return a.cbCreateInvoiceSnap(ctx, chatID, months, price, web, nil)
}

// cbCreateInvoiceSnap — то же с явными условиями продажи; snap == nil означает
// «Базовый по текущей сетке».
func (a *App) cbCreateInvoiceSnap(ctx context.Context, chatID int64, months int, price string, web bool, snap *model.PlanSnapshot) (string, int64, error) {
	client := a.cbClient()
	if client == nil {
		return "", 0, errors.New("cryptobot не настроен")
	}
	cfg := a.cbConfig()
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	// Валюта счёта — из прайса (hlCurrency валидирует код и по умолчанию даёт
	// RUB), а не жёстко рубли: прайс в USD не должен превращаться в счёт на то
	// же число в рублях.
	fiat := a.hlCurrency()
	inv, err := client.CreateInvoice(ctx, price, fiat, cfg.Asset, chatID, months)
	if err != nil {
		a.payLog(ctx, model.PayMethodCryptoBot, "", chatID, "invoice_error", "purchase months=%d: %v", months, err)
		return "", 0, err
	}
	a.payLog(ctx, model.PayMethodCryptoBot, "cb:"+strconv.FormatInt(inv.InvoiceID, 10), chatID, "invoice_created", "purchase months=%d price=%s %s assets=%s", months, price, fiat, cfg.Asset)
	if snap == nil {
		snap = a.planSnapshot(months)
	}
	if a.store != nil {
		_ = a.store.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodCryptoBot, ExtID: "cb:" + strconv.FormatInt(inv.InvoiceID, 10), TelegramID: chatID, Months: months, Snapshot: snap})
	}
	payURL := inv.MiniAppInvoiceURL
	if web && inv.WebAppInvoiceURL != "" {
		payURL = inv.WebAppInvoiceURL // browser cabinet: pay without Telegram
	}
	if payURL == "" {
		payURL = inv.BotInvoiceURL
	}
	return payURL, inv.InvoiceID, nil
}

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

// cryptoAmount — сумма покупки для журнала и снимка сделки: цена ПРОДАННОЙ
// сделки из снимка счёта (тарифы продаются не по сетке!), иначе цена сетки,
// иначе сырая крипто-сумма. Цена сетки для тарифной сделки записала бы в Paid
// чужую цену — и зачёт остатка при смене тарифа считался бы по ней.
func (a *App) cryptoAmount(snap *model.PlanSnapshot, months int, fallback string) string {
	if snap != nil && snap.Price != "" {
		return snap.Price + curSuffix(curSymbol(a.hlCurrency()))
	}
	if p := a.pricing().Base[months]; p != "" {
		return p + curSuffix(curSymbol(a.hlCurrency()))
	}
	return fallback
}

// cbFiatCodes — фиатные валюты, которые принимает CryptoBot (закрытый список
// провайдера). Всё остальное счётом не выставляется: молчаливая подстановка
// рубля продавала подписку по цене в сто раз меньше.
var cbFiatCodes = map[string]bool{
	"RUB": true, "USD": true, "EUR": true, "BYN": true, "UAH": true, "KZT": true,
	"UZS": true, "GEL": true, "TRY": true, "AMD": true, "THB": true, "INR": true,
	"BRL": true, "IDR": true, "AZN": true, "AED": true, "PLN": true, "ILS": true,
	"KGS": true, "TJS": true,
}

// cbFiat — код валюты для счёта CryptoBot. Пустая валюта и любые написания
// рубля дают RUB (так прайс задавался годами), ISO-код из списка провайдера —
// сам себя, всё прочее — отказ.
func cbFiat(cur string) (string, bool) {
	c := strings.TrimSpace(cur)
	if c == "" || rubCurrency(c) {
		return "RUB", true
	}
	up := strings.ToUpper(c)
	if cbFiatCodes[up] {
		return up, true
	}
	return "", false
}

// cbExpired — счёт протух: ждать нечего, кнопку повторной проверки не
// показываем и гасим счёт, чтобы сверка его больше не опрашивала.
func (a *App) cbExpired(ctx context.Context, chatID int64, extID string, pendingID int64) {
	a.payLog(ctx, model.PayMethodCryptoBot, extID, chatID, "expired", "счёт истёк — ожидание оплаты прекращено")
	if a.store != nil && pendingID != 0 {
		_ = a.store.ResolvePending(ctx, pendingID)
	}
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, i18n.T(lang, "cb.expired"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
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
	// Валюта тарифа ≠ валюта сетки: CryptoBot выставил бы счёт с числом тарифа
	// в чужой валюте.
	if !cfg.Enabled || price == "" || !a.saleGridCurrency(s) {
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
		a.sendHome(ctx, chatID, a.clientErr(ctx, chatID, "CryptoBot", err))
		return
	}
	a.sendKB(ctx, chatID, withPayNote(lang, model.PayMethodCryptoBot, i18n.T(lang, "cb.pay_prompt", months, price+curSuffix(curSymbol(a.hlCurrency())))), [][]models.InlineKeyboardButton{
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

	var pending *model.PendingInvoice
	if a.store != nil {
		pending, _ = a.store.PendingByExtID(ctx, extID)
	}
	inv, err := client.GetInvoice(ctx, invoiceID)
	if err != nil {
		a.sendHome(ctx, chatID, a.clientErr(ctx, chatID, "CryptoBot", err))
		return
	}
	if pending != nil && pending.Purpose == "topup" {
		a.payLog(ctx, model.PayMethodCryptoBot, extID, chatID, "manual_check", "topup status=%s", inv.Status)
	} else {
		a.payLog(ctx, model.PayMethodCryptoBot, extID, chatID, "manual_check", "status=%s", inv.Status)
	}
	if inv.Status == "expired" {
		// Счёт CryptoBot живёт 30 минут, а кнопка «Проверить оплату» — вечно.
		// «Оплата ещё не поступила, попробуйте через минуту» на мёртвом счёте —
		// это предложение ждать того, чего не будет.
		var pendingID int64
		if pending != nil {
			pendingID = pending.ID
		}
		a.cbExpired(ctx, chatID, extID, pendingID)
		return
	}
	if inv.Status != "paid" {
		a.sendKB(ctx, chatID, i18n.T(lang, "cb.pending"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "cb.btn_check"), "cbc:"+idStr+":"+mosStr)},
			{btn(i18n.T(lang, "btn.home"), "menu:home")},
		})
		return
	}
	rawAmount := cbAmount(inv.Asset, inv.Amount, inv.PaidAsset, inv.PaidAmount, inv.Fiat)
	res, err := a.settleCryptoBot(ctx, extID, inv.Payload, rawAmount)
	switch res {
	case cbTopUpFailed:
		a.log.Error("cryptobot topup finalize", "err", err, "ext_id", extID)
	case cbBadPayload:
		a.log.Error("cryptobot check: bad invoice payload", "invoice", invoiceID, "err", err)
	case cbLostTopUp:
		a.sendHome(ctx, chatID, i18n.T(lang, "pay.no_period"))
	case cbAlready:
		a.showMySubs(ctx, chatID)
	case cbFailed, cbOrphan, cbHeld:
		a.sendHome(ctx, chatID, a.clientErr(ctx, chatID, "CryptoBot", err))
	}
}

// cbSettled — чем закончился разбор оплаченного счёта CryptoBot.
type cbSettled int

const (
	cbIssued      cbSettled = iota // подписка выдана и отправлена получателю
	cbToppedUp                     // пополнение зачислено
	cbTopUpFailed                  // пополнение не зачислено, счёт оставлен сверке
	cbBadPayload                   // получатель неизвестен: ни payload, ни строки счёта
	cbLostTopUp                    // пополнение без строки счёта, администратор извещён
	cbAlready                      // уже выдано другой доставкой
	cbHeld                         // счёт до восстановления базы, решает администратор
	cbOrphan                       // получателя удалили, администратор извещён
	cbFailed                       // выдача не удалась
)

// settleCryptoBot разбирает оплаченный счёт. Один разбор на вебхук и кнопку
// «Проверить оплату»: кому, на какой срок и на каких условиях выдавать, оба
// обязаны решать одинаково.
func (a *App) settleCryptoBot(ctx context.Context, extID, payload, rawAmount string) (cbSettled, error) {
	var pending *model.PendingInvoice
	if a.store != nil {
		pending, _ = a.store.PendingByExtID(ctx, extID)
	}
	if pending != nil && pending.Purpose == "topup" {
		// Счёт гасится только после зачисления — см. finalizeTopUp.
		if err := a.finalizeTopUp(ctx, pending.TelegramID, pending.Kopecks, model.PayMethodCryptoBot, rawAmount, extID); err != nil {
			return cbTopUpFailed, err
		}
		_ = a.store.ResolvePending(ctx, pending.ID)
		return cbToppedUp, nil
	}
	chatID, months, perr := parseCryptoBotPayload(payload)
	// Своя строка счёта доверенней payload: если она есть, получателя и срок
	// берём из неё — в том числе когда payload не разобрался вовсе.
	if pending != nil && pending.TelegramID != 0 {
		switch {
		case perr != nil:
			a.payLog(ctx, model.PayMethodCryptoBot, extID, pending.TelegramID, "warning",
				"payload счёта не разобран (%q) — получатель и срок взяты из pending-записи (%d:%d)",
				payload, pending.TelegramID, pending.Months)
		case pending.TelegramID != chatID || pending.Months != months:
			a.payLog(ctx, model.PayMethodCryptoBot, extID, pending.TelegramID, "warning",
				"payload вебхука (%d:%d) расходится с pending-записью (%d:%d) — верим записи",
				chatID, months, pending.TelegramID, pending.Months)
		}
		chatID, months, perr = pending.TelegramID, pending.Months, nil
	}
	if perr != nil {
		return cbBadPayload, perr
	}
	if months <= 0 {
		// Payload вида «tg:0» — это пополнение, но pending-записи о нём уже нет
		// (БД откатили или запись сняли). Молча гасить нельзя: деньги приняты.
		// Выдавать подписку тоже: она продлилась бы на ноль месяцев и закрыла
		// ext_id, после чего деньги не зачислить уже никак.
		a.payLog(ctx, model.PayMethodCryptoBot, extID, chatID, "error", "оплаченное пополнение без pending-записи (сумма %s) — зачислите вручную", rawAmount)
		alang := a.lang(a.cfg.AdminID)
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "cb.admin_lost_topup", extID, rawAmount, a.userLabelByID(ctx, chatID)))
		return cbLostTopUp, nil
	}
	snap := pendingSnap(pending)
	amount := a.cryptoAmount(snap, months, rawAmount)
	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodCryptoBot, amount, extID, snap)
	var held restoreHeld
	switch {
	case errors.As(err, &held):
		// Раньше дубля: такая оплата тоже «уже обработана», но человеку
		// положен свой текст, а не экран подписки.
		return cbHeld, err
	case errors.Is(err, storage.ErrDuplicateExtID):
		return cbAlready, nil
	case errors.Is(err, errOrphanPayment):
		return cbOrphan, err
	case err != nil:
		return cbFailed, err
	}
	a.sendSubActive(ctx, chatID, link, expireAt)
	return cbIssued, nil
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

// cbCreateInvoiceSnap creates a CryptoBot invoice + pending record and returns
// the pay URL and invoice id. Shared by chat flow and Mini App. snap == nil означает
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
	// Валюта счёта — из прайса, но только та, которую CryptoBot реально
	// принимает. Раньше здесь стоял общий хелпер, который любую нераспознанную
	// строку молча превращал в RUB: прайс в «$» с ценой «10» уходил счётом на
	// 10 ₽, и увидеть это было негде — пользователю рисовался тот же рубль.
	fiat, curOK := cbFiat(a.pricing().Currency)
	if !curOK {
		a.payLog(ctx, model.PayMethodCryptoBot, "", chatID, "invoice_error",
			"валюта прайса %q не поддерживается CryptoBot — счёт не выставлен", a.pricing().Currency)
		return "", 0, errors.New(i18n.T(a.lang(chatID), "cb.cur_unsupported"))
	}
	inv, err := client.CreateInvoice(ctx, price, fiat, cfg.Asset, "", chatID, months)
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

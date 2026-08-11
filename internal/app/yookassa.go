package app

import (
	"context"
	"fmt"
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

// ykValue нормализует цену из прайса для amount.value ЮKassa: строгий разбор
// через rubToKopecks («199,50» → «199.50»), мусор вроде «1 000 ₽» отбивается,
// а не уезжает в API с гарантированным 400 у пользователя на форме.
func ykValue(raw string) (string, bool) {
	k, ok := rubToKopecks(raw)
	if !ok || k <= 0 {
		return "", false
	}
	return kopecksToRub(k), true
}

// startYooKassa — вход в оплату через ЮKassa. Платёж всегда создаётся с
// сохранением способа оплаты, когда админ включил автоплатежи (на форме ЮKassa
// пользователь видит согласие на автоплатежи), но сами автосписания включаются
// только после явного «да» в боте — предложение приходит после успешной оплаты.
func (a *App) startYooKassa(ctx context.Context, chatID int64) {
	a.ykStart(ctx, chatID, a.autoPayAvailable())
}

// ykStart создаёт платёж в ЮKassa. save=true — просим ЮKassa сохранить способ
// оплаты, чтобы потом продлевать подписку автоматически.
func (a *App) ykStart(ctx context.Context, chatID int64, save bool) {
	lang := a.lang(chatID)
	s := a.saleOrAsk(ctx, chatID)
	if s == nil {
		return
	}
	months := s.Months
	cfg := a.ykConfig()
	fiat := a.saleFiat(s, model.PayMethodYooKassa)
	value, okPrice := ykValue(fiat)
	if !cfg.Enabled || !okPrice {
		if !okPrice && fiat != "" {
			a.payLog(ctx, model.PayMethodYooKassa, "", chatID, "error", "цена за %d мес. задана некорректно (%q) — счёт не создать", months, fiat)
		}
		a.sendHome(ctx, chatID, i18n.T(lang, "yk.no_price"))
		return
	}
	if save && !a.autoPayAvailable() {
		save = false
	}
	client := a.ykClient()
	if client == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "yk.not_configured"))
		return
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	returnURL := cfg.ReturnURL
	if returnURL == "" {
		returnURL = "https://t.me"
	}
	// См. currencyCode: проверка по длине пропускала символ «₽» (три байта),
	// и ЮKassa отвечала 400 на такой код валюты.
	saleCur := a.saleCurrency(s)
	currency := saleCur
	if !currencyCode(currency) {
		currency = "RUB"
	}
	desc := i18n.T(lang, "yk.invoice_desc", months)
	payURL, extID, err := a.ykCreatePayment(ctx, chatID, months, value, currency, returnURL, desc, save, a.saleSnapshot(s))
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "yk.fail", err.Error()))
		return
	}
	prompt := i18n.T(lang, "yk.pay_prompt", months, value+curSuffix(saleCur))
	if save {
		prompt += "\n\n" + i18n.T(lang, "ap.pay_hint")
	}
	a.sendKB(ctx, chatID, prompt, [][]models.InlineKeyboardButton{
		{{Text: i18n.T(lang, "yk.btn_pay"), URL: payURL}},
		{btn(i18n.T(lang, "yk.btn_check"), "ykc:"+extID)},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onYKCheck(ctx context.Context, chatID int64, payID string) {
	lang := a.lang(chatID)
	client := a.ykClient()
	if client == nil || payID == "" {
		return
	}

	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, payID); done {
			a.showMySubs(ctx, chatID)
			return
		}
	}
	pay, err := client.GetPayment(ctx, payID)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "yk.fail", err.Error()))
		return
	}
	a.payLog(ctx, model.PayMethodYooKassa, payID, chatID, "manual_check", "status=%s paid=%v", pay.Status, pay.Paid)
	// Условия выдачи те же, что у вебхука: succeeded И paid. Ручная кнопка не
	// должна быть мягче автоматики.
	if pay.Status != "succeeded" || !pay.Paid {
		a.sendKB(ctx, chatID, i18n.T(lang, "yk.pending"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "yk.btn_check"), "ykc:"+payID)},
			{btn(i18n.T(lang, "btn.home"), "menu:home")},
		})
		return
	}
	if a.store != nil {
		if p, _ := a.store.PendingByExtID(ctx, payID); p != nil && p.Purpose == "topup" {
			amount := pay.Amount.Value + " " + pay.Amount.Currency
			_ = a.finalizeTopUp(ctx, p.TelegramID, p.Kopecks, model.PayMethodYooKassa, amount, payID)
			_ = a.store.ResolvePending(ctx, p.ID)
			return
		}
	}
	payChat, _ := strconv.ParseInt(pay.Metadata["telegram_id"], 10, 64)
	months, _ := strconv.Atoi(pay.Metadata["months"])
	if (payChat == 0 || months == 0) && a.store != nil {
		if p, _ := a.store.PendingByExtID(ctx, payID); p != nil {
			if payChat == 0 {
				payChat = p.TelegramID
			}
			if months == 0 {
				months = p.Months
			}
		}
	}
	if payChat != 0 && months == 0 {
		a.noPeriodForPayment(ctx, model.PayMethodYooKassa, payID, payChat)
		return
	}
	if payChat == 0 || months == 0 {
		// Как и в вебхуке: без metadata получатель и срок неизвестны — не
		// угадываем (нажавшему кнопку и сроку «по умолчанию» выдавать нельзя).
		a.payLog(ctx, model.PayMethodYooKassa, payID, chatID, "error", "в metadata платежа нет telegram_id/months — получатель неизвестен")
		a.sendHome(ctx, chatID, i18n.T(lang, "yk.fail", "metadata"))
		return
	}
	amount := pay.Amount.Value + " " + pay.Amount.Currency
	link, expireAt, err := a.finalizePurchase(ctx, payChat, months, model.PayMethodYooKassa, amount, payID, a.pendingSnapshot(ctx, payID))
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "yk.fail", err.Error()))
		return
	}
	a.saveAutoPayFromPayment(ctx, payChat, months, pay, a.pendingSnapshot(ctx, payID))
	a.sendSubActive(ctx, payChat, link, expireAt)
}

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
	auto := i18n.T(lang, "admin.off")
	if cfg.AutoPay {
		auto = i18n.T(lang, "admin.on")
	}
	text := i18n.T(lang, "admin.yk_title", status, shop, secret, ret, curRUB, a.formatFiatPrices(model.PayMethodYooKassa)) +
		i18n.T(lang, "admin.yk_auto_block", auto, cfg.AutoPayDays, a.autoPayCount(ctx))
	a.sendPayKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{toggleBtn(lang, cfg.Enabled, "yk:toggle"), btn(i18n.T(lang, "admin.btn_prices"), "yk:prices")},
		{btn(i18n.T(lang, "admin.yk_btn_shop"), "yk:shop"), btn(i18n.T(lang, "admin.yk_btn_secret"), "yk:secret")},
		{btn(i18n.T(lang, "admin.yk_btn_return"), "yk:return")},
		{btn(i18n.T(lang, "admin.yk_btn_auto"), "yk:auto"), btn(i18n.T(lang, "admin.yk_btn_autodays"), "yk:autodays")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// autoPayCount — сколько пользователей сейчас с включённым автопродлением
// (для админского экрана).
func (a *App) autoPayCount(ctx context.Context) int {
	if a.store == nil {
		return 0
	}
	list, err := a.store.ListAutoPay(ctx)
	if err != nil {
		return 0
	}
	n := 0
	for i := range list {
		if list[i].Enabled && list[i].MethodID != "" {
			n++
		}
	}
	return n
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
	case "auto":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.YooKassa.AutoPay = !a.botCfg.YooKassa.AutoPay
			a.botCfg.NormalizeYooKassa()
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooKassaAdmin(ctx, chatID)
	case "autodays":
		a.getUI(chatID).adminInput = "yk_autodays"
		a.askInput(ctx, chatID, i18n.T(lang, "admin.yk_ask_autodays"), "menu:yookassa")
	case "cur":
		a.getUI(chatID).adminInput = "yk_cur"
		a.askInput(ctx, chatID, i18n.T(lang, "admin.ask_currency"), "menu:yookassa")
	case "prices":
		a.askPriceMonth(ctx, chatID, "yk")
	case "price":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "ykprice"
		ui.priceMonths = mo
		// Старый экран правит «Базовый»: контекст карточки тарифа здесь чужой.
		ui.planCode = ""
		a.askInput(ctx, chatID, i18n.T(lang, "admin.yk_ask_price", mo), "menu:yookassa")
	}
}

// ykCreatePayment creates a YooKassa payment + pending invoice and returns the
// confirmation URL. Shared by the chat flow and the Mini App so the pending
// ExtID/format stay identical. snap — условия продажи; nil означает «Базовый
// по текущей сетке» (мини-апп и кабинет пока продают только его).
func (a *App) ykCreatePayment(ctx context.Context, chatID int64, months int, value, currency, returnURL, desc string, save bool, snap *model.PlanSnapshot) (payURL, extID string, err error) {
	client := a.ykClient()
	if client == nil {
		return "", "", fmt.Errorf("yookassa не настроена")
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	if snap == nil {
		snap = a.planSnapshot(months)
	}
	pay, err := client.CreatePaymentSaving(ctx, value, currency, desc, returnURL, chatID, months, snap.Code, save)
	if err != nil {
		a.payLog(ctx, model.PayMethodYooKassa, "", chatID, "invoice_error", "purchase months=%d: %v", months, err)
		return "", "", err
	}
	a.payLog(ctx, model.PayMethodYooKassa, pay.ID, chatID, "invoice_created", "purchase months=%d amount=%s %s autopay=%v", months, value, currency, save)
	if a.store != nil {
		_ = a.store.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: pay.ID, TelegramID: chatID, Months: months, Snapshot: snap})
	}
	return pay.Confirmation.ConfirmationURL, pay.ID, nil
}

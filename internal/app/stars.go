package app

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/storage"
)

func (a *App) starsConfig() model.StarsConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.StarsConfig{}
	}
	return a.botCfg.Stars
}

func (a *App) startStars(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	months := a.buyMonthsOrAsk(ctx, chatID)
	if months == 0 {
		return
	}
	pr := a.pricing()
	amount := pr.StarPrice(months)
	// Базовая цена — признак того, что срок вообще продаётся: витрина, тариф и
	// оплата с баланса смотрят именно на неё. Без этой проверки срок, снятый
	// админом с продажи, продолжал бы продаваться за звёзды.
	if !a.starsConfig().Enabled || amount <= 0 || pr.Base[months] == "" {
		a.sendHome(ctx, chatID, i18n.T(lang, "stars.no_price"))
		return
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	// Условия сделки фиксируем в таблице условий счетов: у Stars нет строки в
	// очереди незакрытых счетов, а payload трогать нельзя — иначе предпроверка
	// отклонит легитимную оплату (разбор превращает в число весь остаток
	// строки).
	a.rememberStarsSnapshot(ctx, chatID, months, a.planSnapshot(months))
	title := i18n.T(lang, "stars.invoice_title", months)
	desc := i18n.T(lang, "stars.invoice_desc", months)
	a.msg.SendInvoice(ctx, chatID, title, desc, "stars:"+strconv.Itoa(months), "XTR", amount)
	a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_sent", "purchase months=%d stars=%d", months, amount)
}

func (a *App) handlePreCheckout(ctx context.Context, q *models.PreCheckoutQuery) {
	months := 0
	if _, after, ok := strings.Cut(q.InvoicePayload, ":"); ok {
		months, _ = strconv.Atoi(after)
	}
	if !a.starsConfig().Enabled || months <= 0 || a.pricing().StarPrice(months) != q.TotalAmount {
		var fromID int64
		if q.From != nil {
			fromID = q.From.ID
		}
		a.payLog(ctx, model.PayMethodStars, "", fromID, "precheckout_rejected", "payload=%s total=%d enabled=%v", q.InvoicePayload, q.TotalAmount, a.starsConfig().Enabled)
		a.msg.AnswerPreCheckout(ctx, q.ID, false, i18n.T(a.lang(fromID), "stars.no_price"))
		return
	}
	a.msg.AnswerPreCheckout(ctx, q.ID, true, "")
}

func (a *App) handleSuccessfulPayment(ctx context.Context, m *models.Message) {
	sp := m.SuccessfulPayment
	chatID := m.Chat.ID
	months := 0
	if _, after, ok := strings.Cut(sp.InvoicePayload, ":"); ok {
		months, _ = strconv.Atoi(after)
	}
	amount := strconv.Itoa(sp.TotalAmount) + " ⭐"
	a.payLog(ctx, model.PayMethodStars, sp.TelegramPaymentChargeID, chatID, "payment_received", "total=%d payload=%s", sp.TotalAmount, sp.InvoicePayload)
	if months <= 0 {
		// Payload наш и проверен на предпроверке, так что сюда попасть можно
		// только при испорченной доставке. Подставлять срок «по умолчанию»
		// нельзя: человек заплатил за другой.
		a.noPeriodForPayment(ctx, model.PayMethodStars, sp.TelegramPaymentChargeID, chatID)
		return
	}
	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodStars, amount, sp.TelegramPaymentChargeID,
		a.starsSnapshot(ctx, chatID, months))
	if err != nil {
		if errors.Is(err, storage.ErrDuplicateExtID) {
			// Telegram доставил апдейт повторно (рестарт до сдвига offset и
			// т.п.) — подписка уже выдана первой доставкой, пугать пользователя
			// «не удалось активировать» нельзя.
			a.payLog(ctx, model.PayMethodStars, sp.TelegramPaymentChargeID, chatID, "duplicate", "повторная доставка successful_payment — уже финализирован")
			return
		}
		a.payLog(ctx, model.PayMethodStars, sp.TelegramPaymentChargeID, chatID, "finalize_error", "%v", err)
		a.notify(ctx, chatID, i18n.T(a.lang(chatID), "stars.fail", err.Error()))
		return
	}
	a.sendSubActive(ctx, chatID, link, expireAt)
}

// handleRefundedPayment — Telegram сообщил о возврате звёзд плательщику.
// Доступ автоматически не отзываем (возврат мог сделать сам админ по
// договорённости) — фиксируем в журнале и зовём админа.
func (a *App) handleRefundedPayment(ctx context.Context, m *models.Message) {
	rp := m.RefundedPayment
	if rp == nil {
		return
	}
	chatID := m.Chat.ID
	a.payLog(ctx, model.PayMethodStars, rp.TelegramPaymentChargeID, chatID, "refunded", "возврат %d %s, payload=%s", rp.TotalAmount, rp.Currency, rp.InvoicePayload)
	alang := a.lang(a.cfg.AdminID)
	a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "stars.admin_refunded", rp.TelegramPaymentChargeID, rp.TotalAmount, a.userLabelByID(ctx, chatID)))
}

func (a *App) showStarsAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	status := i18n.T(lang, "admin.off")
	if a.starsConfig().Enabled {
		status = i18n.T(lang, "admin.on")
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "admin.stars_title", status, a.formatStarPrices()), [][]models.InlineKeyboardButton{
		{toggleBtn(lang, a.starsConfig().Enabled, "star:toggle"), btn(i18n.T(lang, "admin.btn_prices"), "star:prices")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onStars(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	switch action {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Stars.Enabled = !a.botCfg.Stars.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showStarsAdmin(ctx, chatID)
	case "prices":
		lang := a.lang(chatID)
		var row []models.InlineKeyboardButton
		for _, mo := range model.PlanMonths {
			row = append(row, btn(strconv.Itoa(mo)+"м", "star:price:"+strconv.Itoa(mo)))
		}
		a.sendKB(ctx, chatID, i18n.T(lang, "admin.ask_price_month"), [][]models.InlineKeyboardButton{row, navBack(lang, "menu:stars")})
	case "price":
		mo, _ := strconv.Atoi(arg)
		lang := a.lang(chatID)
		ui := a.getUI(chatID)
		ui.adminInput = "starprice"
		ui.priceMonths = mo
		// Старый экран правит «Базовый»: контекст карточки тарифа здесь чужой.
		ui.planCode = ""
		prompt := i18n.T(lang, "admin.stars_ask_price", mo)
		if s := a.starsSuggestion(lang, mo); s != "" {
			prompt += "\n\n" + s
		}
		a.askInput(ctx, chatID, prompt, "menu:stars")
	}
}

const approxRubPerStar = 1.5

func (a *App) starsSuggestion(lang string, months int) string {
	base := a.pricing().Base[months]
	k, ok := rubToKopecks(base)
	if !ok || k <= 0 {
		return ""
	}
	rub := float64(k) / 100.0
	stars := int(math.Ceil(rub / approxRubPerStar * 1.05))
	return i18n.T(lang, "stars.suggest", base, stars)
}

func (a *App) formatStarPrices() string {
	pr := a.pricing()
	var parts []string
	for _, mo := range model.PlanMonths {
		if v := pr.StarPrice(mo); v > 0 {
			parts = append(parts, strconv.Itoa(mo)+"м="+strconv.Itoa(v)+"⭐")
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

// starsInvoiceLink builds a Telegram Stars invoice LINK (for Mini App
// openInvoice). Uses the same payload as the chat invoice so the
// pre-checkout/successful-payment handlers treat them identically.
var errStarsUnavailable = errors.New("оплата звёздами недоступна")

func (a *App) starsInvoiceLink(ctx context.Context, chatID int64, months int) (string, error) {
	pr := a.pricing()
	amount := pr.StarPrice(months)
	// Тот же гейт, что и в чате: срок без базовой цены с продажи снят.
	if !a.starsConfig().Enabled || amount <= 0 || pr.Base[months] == "" {
		return "", errStarsUnavailable
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	lang := a.lang(chatID)
	a.rememberStarsSnapshot(ctx, chatID, months, a.planSnapshot(months))
	title := i18n.T(lang, "stars.invoice_title", months)
	desc := i18n.T(lang, "stars.invoice_desc", months)
	link, err := a.msg.CreateInvoiceLink(ctx, title, desc, "stars:"+strconv.Itoa(months), "XTR", amount)
	if err != nil {
		a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_error", "purchase months=%d stars=%d: %v", months, amount, err)
		return "", err
	}
	a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_link", "purchase months=%d stars=%d", months, amount)
	return link, nil
}

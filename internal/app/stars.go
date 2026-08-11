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
	s := a.saleOrAsk(ctx, chatID)
	if s == nil {
		return
	}
	months := s.Months
	amount := a.saleStars(s)
	// Базовая цена — признак того, что срок вообще продаётся: витрина, тариф и
	// оплата с баланса смотрят именно на неё. Без этой проверки срок, снятый
	// админом с продажи, продолжал бы продаваться за звёзды.
	if !a.starsConfig().Enabled || amount <= 0 || a.saleBase(s) == "" {
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
	a.rememberStarsSnapshot(ctx, chatID, months, a.saleSnapshot(s))
	title := i18n.T(lang, "stars.invoice_title", months)
	desc := i18n.T(lang, "stars.invoice_desc", months)
	a.msg.SendInvoice(ctx, chatID, title, desc, "stars:"+strconv.Itoa(months), "XTR", amount)
	a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_sent", "purchase plan=%s months=%d stars=%d", s.planCode(), months, amount)
}

func (a *App) handlePreCheckout(ctx context.Context, q *models.PreCheckoutQuery) {
	months := 0
	if _, after, ok := strings.Cut(q.InvoicePayload, ":"); ok {
		months, _ = strconv.Atoi(after)
	}
	var fromID int64
	if q.From != nil {
		fromID = q.From.ID
	}
	// Сумма сверяется с текущей ценой срока — либо в сетке «Базового», либо в
	// тарифе, чей счёт выставлен этому человеку (payload у Stars менять нельзя,
	// тариф опознаётся по сохранённым условиям счёта). Иначе счёт тарифа по
	// ссылке отклонялся бы на предпроверке: его цена в звёздах своя.
	amountOK := a.pricing().StarPrice(months) == q.TotalAmount
	if !amountOK && months > 0 && fromID != 0 {
		if snap := a.starsSnapshot(ctx, fromID, months); snap != nil && snap.Code != "" && snap.Code != model.PlanCodeBase {
			if p, err := a.planByCode(ctx, snap.Code); err == nil && p != nil {
				if d := p.Duration(months); d != nil && d.Stars > 0 && d.Stars == q.TotalAmount {
					amountOK = true
				}
			}
		}
	}
	if !a.starsConfig().Enabled || months <= 0 || !amountOK {
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
	snap, ok := a.starsSnapshotForAmount(ctx, chatID, months, sp.TotalAmount)
	if !ok {
		// Сумма не совпала ни с одним известным счётом на этот срок. Выдавать
		// «что-нибудь» нельзя: применённые условия обязаны стоить ровно
		// оплаченного. Деньги приняты — случай разбирает админ.
		a.payLog(ctx, model.PayMethodStars, sp.TelegramPaymentChargeID, chatID, "error", "оплаченная сумма %d⭐ не совпала с условиями счетов — выдача не проводится", sp.TotalAmount)
		alang := a.lang(a.cfg.AdminID)
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "admin.pay_no_period", model.PayMethodStars+" "+sp.TelegramPaymentChargeID))
		a.notify(ctx, chatID, i18n.T(a.lang(chatID), "pay.no_period"))
		return
	}
	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodStars, amount, sp.TelegramPaymentChargeID, snap)
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

// starsSnapshotForAmount подбирает условия сделки под фактически оплаченную
// сумму.
//
// Строка условий счёта у Stars одна на (человек, срок): счёт «Базового» и
// счёт тарифа по ссылке на тот же срок перезаписывают её по очереди, и
// применять последний записанный снимок к ЛЮБОЙ оплате нельзя — выставив себе
// оба счёта и оплатив дешёвый, человек получал бы условия дорогого.
//
// Возвращает снимок, чья цена в звёздах равна оплаченной: сохранённый снимок
// тарифа, если сходится его цена; иначе условия «Базового», если сумма — цена
// сетки; иначе (nil, false) — выдачи нет, случай уходит админу.
func (a *App) starsSnapshotForAmount(ctx context.Context, chatID int64, months, paid int) (*model.PlanSnapshot, bool) {
	snap := a.starsSnapshot(ctx, chatID, months)
	if snap != nil && snap.Code != "" && snap.Code != model.PlanCodeBase {
		if p, err := a.planByCode(ctx, snap.Code); err == nil && p != nil {
			if d := p.Duration(months); d != nil && d.Stars > 0 && d.Stars == paid {
				return snap, true
			}
		}
		// Снимок тарифный, но сумма его цене не отвечает — это оплата другого
		// счёта (базового) либо цена тарифа изменилась между счётом и оплатой.
		snap = nil
	}
	if a.pricing().StarPrice(months) == paid {
		if snap != nil {
			// Сохранённый снимок «Базового» — условия на момент счёта.
			return snap, true
		}
		// Снимок перезаписан счётом тарифа или его не было вовсе — берём
		// текущие условия сетки: ровно то, что предпроверка сверила по цене.
		return a.planSnapshot(months), true
	}
	return nil, false
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

func (a *App) starsInvoiceLink(ctx context.Context, chatID int64, s *sale) (string, error) {
	months := s.Months
	amount := a.saleStars(s)
	// Тот же гейт, что и в чате: срок без базовой цены с продажи снят.
	if !a.starsConfig().Enabled || amount <= 0 || a.saleBase(s) == "" {
		return "", errStarsUnavailable
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	lang := a.lang(chatID)
	// Payload остаётся "stars:<месяцы>" — это замороженный формат; условия
	// сделки едут отдельной таблицей условий счетов, как и в чате.
	a.rememberStarsSnapshot(ctx, chatID, months, a.saleSnapshot(s))
	title := i18n.T(lang, "stars.invoice_title", months)
	desc := i18n.T(lang, "stars.invoice_desc", months)
	link, err := a.msg.CreateInvoiceLink(ctx, title, desc, "stars:"+strconv.Itoa(months), "XTR", amount)
	if err != nil {
		a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_error", "purchase plan=%s months=%d stars=%d: %v", s.planCode(), months, amount, err)
		return "", err
	}
	a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_link", "purchase plan=%s months=%d stars=%d", s.planCode(), months, amount)
	return link, nil
}

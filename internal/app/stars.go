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

// starsPayload — разбор payload звёздного счёта.
//
// Формат «stars:<месяцы>» заморожен исторически, поэтому разбор обратно
// совместим: старые счета из переписки продолжают работать. Новый хвост
// «:<tgID>» — это тот, КОМУ счёт выставляли; он нужен, чтобы отличить оплату
// по своей ссылке от оплаты по ссылке, которую переслали третьему лицу.
func starsPayload(payload string) (months int, forID int64) {
	parts := strings.Split(payload, ":")
	if len(parts) < 2 || parts[0] != "stars" {
		return 0, 0
	}
	months, _ = strconv.Atoi(parts[1])
	if len(parts) >= 3 {
		forID, _ = strconv.ParseInt(parts[2], 10, 64)
	}
	return months, forID
}

func (a *App) handlePreCheckout(ctx context.Context, q *models.PreCheckoutQuery) {
	months, forID := starsPayload(q.InvoicePayload)
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
	// Ссылка-счёт по своей природе многоразовая и переносимая: у метода
	// createInvoiceLink нет ни адресата, ни срока, ни лимита оплат. Значит
	// единственное место, где можно не пустить чужого плательщика, — вот этот
	// ответ на предпроверку.
	//
	// Проверяем ровно тогда, когда платит НЕ тот, кому счёт выставляли: иначе
	// пришлось бы дублировать все гейты на каждой оплате, а свои они уже
	// прошли при выставлении счёта.
	if forID != 0 && fromID != 0 && forID != fromID {
		if reason := a.starsForeignPayerRefusal(ctx, fromID); reason != "" {
			a.payLog(ctx, model.PayMethodStars, "", fromID, "precheckout_rejected",
				"счёт выставлен %d, платит %d: %s", forID, fromID, reason)
			a.msg.AnswerPreCheckout(ctx, q.ID, false, reason)
			return
		}
	}
	a.msg.AnswerPreCheckout(ctx, q.ID, true, "")
}

// starsForeignPayerRefusal — почему этому плательщику нельзя продать по чужой
// ссылке. Пустая строка — можно.
//
// Гейты при выставлении счёта проверялись для того, кому его выставляли:
// документы, блокировка, режим доступа. Пересланная ссылка их обходила
// целиком — третий человек платил и получал подписку, не приняв оферту и не
// пройдя модерацию.
func (a *App) starsForeignPayerRefusal(ctx context.Context, payerID int64) string {
	lang := a.lang(payerID)
	if a.store != nil {
		if u, err := a.store.GetUser(ctx, payerID); err == nil && u != nil && u.Blocked {
			return i18n.T(lang, "stars.payer_blocked")
		}
	}
	if a.legalRequired(ctx, payerID) {
		return i18n.T(lang, "stars.payer_legal")
	}
	return ""
}

func (a *App) handleSuccessfulPayment(ctx context.Context, m *models.Message) {
	sp := m.SuccessfulPayment
	// Подписку получает ПЛАТЕЛЬЩИК: сервисное сообщение об оплате приходит в
	// его личный чат с ботом. Это и требование доки Telegram, и единственное
	// честное поведение — деньги списаны у него.
	chatID := m.Chat.ID
	months, _ := starsPayload(sp.InvoicePayload)
	amount := strconv.Itoa(sp.TotalAmount) + " ⭐"
	a.payLog(ctx, model.PayMethodStars, sp.TelegramPaymentChargeID, chatID, "payment_received", "total=%d payload=%s", sp.TotalAmount, sp.InvoicePayload)
	if months <= 0 {
		// Payload наш и проверен на предпроверке, так что сюда попасть можно
		// только при испорченной доставке. Подставлять срок «по умолчанию»
		// нельзя: человек заплатил за другой.
		a.noPeriodForPayment(ctx, model.PayMethodStars, sp.TelegramPaymentChargeID, chatID)
		a.starsOfferRefund(ctx, chatID, sp.TelegramPaymentChargeID, sp.TotalAmount)
		return
	}
	snap, ok := a.starsSnapshotForAmount(ctx, chatID, months, sp.TotalAmount)
	if !ok {
		// Сумма не совпала ни с одним известным счётом на этот срок. Выдавать
		// «что-нибудь» нельзя: применённые условия обязаны стоить ровно
		// оплаченного. Деньги приняты — случай разбирает админ.
		a.payLog(ctx, model.PayMethodStars, sp.TelegramPaymentChargeID, chatID, "error", "оплаченная сумма %d⭐ не совпала с условиями счетов — выдача не проводится", sp.TotalAmount)
		alang := a.lang(a.cfg.AdminID)
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "admin.pay_no_period", model.PayMethodStars+" "+sp.TelegramPaymentChargeID, a.userLabelByID(ctx, chatID)))
		a.starsOfferRefund(ctx, chatID, sp.TelegramPaymentChargeID, sp.TotalAmount)
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
		a.starsOfferRefund(ctx, chatID, sp.TelegramPaymentChargeID, sp.TotalAmount)
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
	// Платёж помечается возвращённым. Без этого он навсегда оставался
	// «оплаченным»: человек считался покупателем, тариф «только новым» ему был
	// закрыт, а возвращённые звёзды продолжали числиться выручкой.
	if a.store != nil && rp.TelegramPaymentChargeID != "" {
		if err := a.store.SetPaymentStatus(ctx, rp.TelegramPaymentChargeID, model.PaymentRefunded); err != nil {
			a.log.Warn("платёж не помечен возвращённым", "err", err, "charge_id", rp.TelegramPaymentChargeID)
		}
	}
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
	case "refund":
		a.starsRefund(ctx, chatID, arg)
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
	// В payload ссылки едет её адресат: ссылку можно переслать кому угодно, и
	// без адресата чужого плательщика не отличить от своего (см. предпроверку).
	payload := "stars:" + strconv.Itoa(months) + ":" + strconv.FormatInt(chatID, 10)
	link, err := a.msg.CreateInvoiceLink(ctx, title, desc, payload, "XTR", amount)
	if err != nil {
		a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_error", "purchase plan=%s months=%d stars=%d: %v", s.planCode(), months, amount, err)
		return "", err
	}
	a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_link", "purchase plan=%s months=%d stars=%d", s.planCode(), months, amount)
	return link, nil
}

// starsOfferRefund — предложить админу вернуть звёзды.
//
// Бот сам создаёт три состояния «деньги приняты, выдачи нет»: неизвестный срок,
// сумма не совпала ни с одним счётом, панель не отдала подписку. Вернуть их
// было нечем, а команда /paysupport в боте возврат обещает. Кнопка приходит
// ровно туда, где состояние и возникло.
func (a *App) starsOfferRefund(ctx context.Context, payerID int64, chargeID string, amount int) {
	if chargeID == "" || payerID == 0 {
		return
	}
	alang := a.lang(a.cfg.AdminID)
	a.notifyKB(ctx, a.cfg.AdminID,
		i18n.T(alang, "stars.admin_refund_offer", amount, a.userLabelByID(ctx, payerID), chargeID),
		[][]models.InlineKeyboardButton{{
			btn(i18n.T(alang, "stars.btn_refund"), "star:refund:"+strconv.FormatInt(payerID, 10)+":"+chargeID),
		}})
}

// starsRefund — админ подтвердил возврат. Возврат необратим и делается целиком:
// частичного у Telegram нет.
func (a *App) starsRefund(ctx context.Context, adminID int64, arg string) {
	lang := a.lang(adminID)
	idStr, chargeID, ok := strings.Cut(arg, ":")
	payerID, err := strconv.ParseInt(idStr, 10, 64)
	if !ok || err != nil || chargeID == "" {
		a.sendHome(ctx, adminID, i18n.T(lang, "stars.refund_failed", "неверные данные возврата"))
		return
	}
	if rerr := a.msg.RefundStars(ctx, payerID, chargeID); rerr != nil {
		a.payLog(ctx, model.PayMethodStars, chargeID, payerID, "refund_error", "%v", rerr)
		a.sendHome(ctx, adminID, i18n.T(lang, "stars.refund_failed", rerr.Error()))
		return
	}
	// Отметку о возврате ставит Telegram отдельным событием refunded_payment,
	// но ждать его не обязательно: платёж помечаем сразу, повторная отметка
	// безвредна.
	a.payLog(ctx, model.PayMethodStars, chargeID, payerID, "refunded", "возврат сделан админом")
	if a.store != nil {
		if serr := a.store.SetPaymentStatus(ctx, chargeID, model.PaymentRefunded); serr != nil {
			a.log.Warn("платёж не помечен возвращённым", "err", serr, "charge_id", chargeID)
		}
	}
	a.sendHome(ctx, adminID, i18n.T(lang, "stars.refund_done", a.userLabelByID(ctx, payerID)))
}

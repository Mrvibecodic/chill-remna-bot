package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/storage"
	"remnabot/internal/web"
)

func (a *App) tributeCfg() model.TributeConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.TributeConfig{}
	}
	return a.botCfg.Tribute.Clone()
}

// tributeURLFor — ссылка оплаты тарифа через Tribute или "", если Tribute им
// не торгует. Тариф без ID подписки ссылку не получает: вебхук по такой оплате
// не узнал бы тариф и выдал бы «Базовый».
func (a *App) tributeURLFor(code string) string {
	cfg := a.tributeCfg()
	if !cfg.Enabled {
		return ""
	}
	if code == "" || code == model.PlanCodeBase {
		if validButtonURL(cfg.PayURL) {
			return cfg.PayURL
		}
		return ""
	}
	l := cfg.LinkFor(code)
	if l == nil || l.SubID == 0 || !validButtonURL(l.URL) {
		return ""
	}
	return l.URL
}

// tributeOffer — ссылка оплаты тарифа через Tribute и сроки, которые бот по
// ней выдаст. p — строка тарифа (nil у «Базового» из сетки). Пустая ссылка —
// Tribute у тарифа нет, в том числе когда периоды подписки известны и ни один
// не выдаётся: такая оплата ушла бы в отказ. Сроки пустые — периоды
// подписки неизвестны (ID введён вручную или у «Базового» подписки нет).
func (a *App) tributeOffer(lang string, p *model.Plan, code string) (string, []string) {
	url := a.tributeURLFor(code)
	if url == "" {
		return "", nil
	}
	l := a.tributeCfg().LinkFor(code)
	if l == nil || len(l.Periods) == 0 {
		return url, nil
	}
	if p == nil && code != "" && code != model.PlanCodeBase {
		return "", nil
	}
	var terms []string
	seen := map[string]bool{}
	for _, per := range l.Periods {
		days, months, ok := tributePeriod(per)
		if !ok {
			continue
		}
		sale, term := months, monthsWord(lang, months)
		if days > 0 {
			sale, term = 1, i18n.T(lang, "trb.week")
		}
		if !a.tributeCovers(p, sale) || seen[term] {
			continue
		}
		seen[term] = true
		terms = append(terms, term)
	}
	if len(terms) == 0 {
		return "", nil
	}
	return url, terms
}

// saleTitleHTML — имя продаваемого тарифа для текста сообщения.
func (a *App) saleTitleHTML(ctx context.Context, lang string, s *sale) string {
	if s != nil && s.Plan != nil {
		return planTitleHTML(lang, s.Plan)
	}
	if p := a.basePlanRow(ctx); p != nil {
		return planTitleHTML(lang, p)
	}
	a.mu.Lock()
	_, name := a.basePlanIdentLocked()
	a.mu.Unlock()
	return html.EscapeString(name)
}

func (a *App) startTribute(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	// Тариф и срок — из намерения покупки, с тем же гейтом доступности, что у
	// остальных способов: счёт живёт на стороне Tribute, и вторая точка гейта
	// здесь, при выдаче ссылки. Цену бота не сверяем: платят цену Tribute.
	s := a.saleOrAskPrice(ctx, chatID, false)
	if s == nil {
		return
	}
	// Битая ссылка (сохранённая до проверки при вводе) сюда не попадает:
	// кнопку с ней Telegram отвергает вместе со всем сообщением.
	url, terms := a.tributeOffer(lang, s.Plan, s.planCode())
	if url == "" {
		if !a.tributeCfg().Enabled {
			a.sendHome(ctx, chatID, i18n.T(lang, "trb.not_configured"))
			return
		}
		// Старая кнопка с экрана другого тарифа: у выбранного Tribute нет.
		a.notify(ctx, chatID, i18n.T(lang, "trb.plan_unavailable"))
		a.showMethodsSale(ctx, chatID, s)
		return
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	text := withPayNote(lang, model.PayMethodTribute, i18n.T(lang, "trb.pay_prompt", a.saleTitleHTML(ctx, lang, s)))
	if len(terms) > 0 {
		text += "\n\n" + i18n.T(lang, "trb.pay_terms", html.EscapeString(strings.Join(terms, ", ")))
	}
	a.sendKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{{Text: i18n.T(lang, "trb.btn_pay"), URL: url}},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// tributePeriod — срок из события Tribute: дни (сколько выдать) и месяцы
// (по какому ключу сетки брать условия). ok=false — период не продаётся, и
// тогда подписку выдавать НЕЛЬЗЯ.
//
// Раньше здесь был подстрочный разбор с «по умолчанию месяц» в конце. Он и
// печатал дни, и обманывал: недельная подписка выдавала календарный месяц, а
// любое новое или незнакомое значение продавалось как месяц — бот не может
// продавать срок, которого не понимает. Список закрытый и повторяет
// официальный enum Tribute; всё, чего в нём нет, отбивается.
//
// Дни у сроков от месяца не задаются: там выдача идёт календарными месяцами,
// как у всех остальных способов оплаты (days=0 — «считать по месяцам»).
func tributePeriod(period string) (days, months int, ok bool) {
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "weekly":
		// Купил неделю — получает неделю. Календарного эквивалента у недели
		// нет, поэтому единственный срок, который выдаётся днями.
		return 7, 0, true
	case "monthly":
		return 0, 1, true
	case "quarterly":
		return 0, 3, true
	case "halfyearly":
		return 0, 6, true
	case "yearly", "annual":
		return 0, 12, true
	}
	// trial, onetime, пустая строка, новые значения enum и опечатки — сюда.
	return 0, 0, false
}

// tributeWebhook — часть полезной нагрузки вебхука Tribute, которая нам нужна.
// Получателя ищем по telegram_user_id; устаревшее user_id (и пришедший ему на
// смену trb_user_id вида «T-31326»/«W-31326») не читаем — привязка у нас идёт
// по Telegram ID.
type tributeWebhook struct {
	Name    string `json:"name"`
	Payload struct {
		// SubscriptionID — подписка автора (общая для всех её подписчиков):
		// по ней находится тариф. SubscriptionName — для журнала.
		SubscriptionID   int64  `json:"subscription_id"`
		SubscriptionName string `json:"subscription_name"`
		Period           string `json:"period"`
		// Price — сколько заплатил клиент, Amount — сколько осталось после
		// комиссии Tribute. Обе суммы в минимальных единицах валюты.
		Price    int64  `json:"price"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
		// Type — regular/gift/trial. У gift и trial оплата нулевая, а их
		// продление Tribute присылает уже как regular.
		Type string `json:"type"`
		// TrbUserID — ID пользователя в Tribute («T-31326» — вход через
		// Telegram, «W-31326» — через почту). Пишем его в журнал, чтобы платёж
		// нашёлся в кабинете Tribute; для выдачи подписки он не нужен.
		TrbUserID        string    `json:"trb_user_id"`
		TelegramUserID   int64     `json:"telegram_user_id"`
		TelegramUsername string    `json:"telegram_username"`
		ExpiresAt        time.Time `json:"expires_at"`
		// Поля события digital_product_refunded. По спеке Tribute purchase_id
		// — ключ, которым возврат сопоставляется с исходной покупкой.
		PurchaseID   string `json:"purchase_id"`
		RefundReason string `json:"refund_reason"`
	} `json:"payload"`
}

// who — приписка к журналу, по которой платёж сопоставляется с кабинетом
// Tribute, даже если Telegram ID не пришёл.
func (wh tributeWebhook) who() string {
	parts := make([]string, 0, 4)
	if wh.Payload.SubscriptionID != 0 {
		sub := "подписка #" + strconv.FormatInt(wh.Payload.SubscriptionID, 10)
		if n := strings.TrimSpace(wh.Payload.SubscriptionName); n != "" {
			sub += " «" + truncRunes(n, 64) + "»"
		}
		parts = append(parts, sub)
	}
	if wh.Payload.TrbUserID != "" {
		parts = append(parts, "trb="+wh.Payload.TrbUserID)
	}
	if wh.Payload.TelegramUsername != "" {
		parts = append(parts, "@"+wh.Payload.TelegramUsername)
	}
	if wh.Payload.Type != "" {
		parts = append(parts, "тип="+wh.Payload.Type)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// tributeAmount печатает сумму так же, как остальные способы оплаты. Tribute
// присылает её в минимальных единицах валюты (700 eur — это 7.00 EUR), а
// строка платежа потом разбирается обратно в числа: из неё считаются процент
// рефереру, выручка в статистике и чек «Мой налог».
func tributeAmount(minor int64, cur string) string {
	return fmt.Sprintf("%.2f %s", float64(minor)/100, curSymbol(cur))
}

func (a *App) HandleTributeWebhook(ctx context.Context, signatureHex string, body []byte) (bool, error) {
	cfg := a.tributeCfg()
	if cfg.APIKey == "" {
		// Без ключа подпись не проверить. Отвечаем 401, а не 200: Tribute будет
		// ретраить доставку до суток, и настоящая оплата не потеряется, если
		// админ успеет вписать ключ.
		a.payLogThrottled(ctx, "trb-webhook-off", model.PayMethodTribute, "", 0, "error", "вебхук отброшен: не задан API-ключ Tribute — оплату нельзя верифицировать")
		a.log.Warn("tribute webhook: ignored — api key not set")
		return false, fmt.Errorf("tribute webhook: %w", web.ErrUnauthorized)
	}
	mac := hmac.New(sha256.New, []byte(cfg.APIKey))
	mac.Write(body)
	got, err := hex.DecodeString(strings.TrimSpace(signatureHex))
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		a.payLogThrottled(ctx, "trb-webhook-sign", model.PayMethodTribute, "", 0, "sign_error", "подпись вебхука не сошлась (проверьте API-ключ Tribute)")
		a.log.Warn("tribute webhook: bad signature")
		// web отдаёт на это 401: с 500 Tribute сутки долбил бы ретраями чужой
		// мусор и запросы с неверным ключом.
		return false, fmt.Errorf("tribute webhook: %w", web.ErrUnauthorized)
	}
	var wh tributeWebhook
	if err := json.Unmarshal(body, &wh); err != nil {
		a.payLogThrottled(ctx, "trb-webhook-json", model.PayMethodTribute, "", 0, "error", "тело вебхука не разобрано: %v", err)
		return false, fmt.Errorf("tribute webhook: bad json: %w", err)
	}
	chatID := wh.Payload.TelegramUserID
	switch wh.Name {
	case "new_subscription", "renewed_subscription":
	case "":
		// Кнопка «Тест» в кабинете Tribute шлёт событие без имени. Отмечаем его
		// в журнале: это единственное подтверждение, что адрес и ключ сошлись.
		a.payLog(ctx, model.PayMethodTribute, "", 0, "test", "тестовый вебхук принят, подпись верна")
		return true, nil
	case "cancelled_subscription":
		// Доступ не трогаем: в Tribute отмена выключает автопродление, а
		// оплаченный период дорабатывает до expires_at. Это НЕ возврат денег.
		a.payLog(ctx, model.PayMethodTribute, "", chatID, "cancelled",
			"подписка отменена в Tribute: продления не будет, оплаченный период до %s%s",
			wh.Payload.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"), wh.who())
		return true, nil
	case "digital_product_refunded":
		a.tributeRefunded(ctx, wh)
		return true, nil
	default:
		a.log.Info("tribute webhook: ignored", "event", wh.Name)
		return true, nil
	}
	if chatID == 0 {
		a.payLog(ctx, model.PayMethodTribute, "", 0, "error", "в вебхуке нет telegram_user_id — получатель неизвестен (событие %s)%s", wh.Name, wh.who())
		a.log.Warn("tribute webhook: no telegram_user_id")
		// Покупатель вошёл в Tribute по почте (веб-ссылка): деньги приняты, а
		// выдать некому. Без уведомления это видно только в журнале.
		paid := wh.Payload.Price
		if paid == 0 {
			paid = wh.Payload.Amount
		}
		if kind := strings.ToLower(strings.TrimSpace(wh.Payload.Type)); paid > 0 && kind != "trial" && kind != "gift" {
			alang := a.lang(a.cfg.AdminID)
			a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "admin.trb_no_tg",
				html.EscapeString(tributeAmount(paid, wh.Payload.Currency)), html.EscapeString(strings.TrimSpace(wh.who()))))
		}
		return true, nil
	}
	days, months, periodOK := tributePeriod(wh.Payload.Period)
	// В ключе дедупликации обязателен telegram_user_id: subscription_id — это
	// идентификатор тарифа автора, общий для всех подписчиков, и двое купивших
	// один тариф в одну секунду иначе получили бы одинаковый ext_id (второму —
	// «duplicate» без подписки при принятых деньгах).
	extID := fmt.Sprintf("trb_%d_%d_%d", chatID, wh.Payload.SubscriptionID, wh.Payload.ExpiresAt.Unix())
	paid := wh.Payload.Price
	if paid == 0 {
		paid = wh.Payload.Amount
	}
	amount := tributeAmount(paid, wh.Payload.Currency)
	if !cfg.Enabled {
		// Тумблер выключен, но деньги уже приняты Tribute — оплату обрабатываем,
		// а факт отмечаем: терять её с ответом 200 нельзя.
		a.payLog(ctx, model.PayMethodTribute, extID, chatID, "warning", "Tribute выключен в админке, но оплата пришла — обрабатываем")
	}
	a.payLog(ctx, model.PayMethodTribute, extID, chatID, "webhook", "%s period=%s amount=%s%s", wh.Name, wh.Payload.Period, amount, wh.who())

	// Повтор уже выданной оплаты отсекается раньше всех отказов: иначе
	// повторная доставка после смены привязок звала бы админа и покупателя по
	// подписке, которая давно выдана.
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, extID); done {
			a.payLog(ctx, model.PayMethodTribute, extID, chatID, "duplicate", "уже финализирован, вебхук пропущен")
			return true, nil
		}
	}

	// Триал и подарок подпиской НЕ являются. Триал у нас бесплатный, свой,
	// выдаётся ботом без всякой платёжки и помечается «использован»; выдавать
	// за него полный оплаченный месяц — это раздача подписок за ноль рублей по
	// повторяемой схеме «подписался на триал → отменил». Признаков два: тип
	// сделки и нулевая цена (по спеке Tribute поле type в new_subscription
	// необязательное, а у триала и подарка оплата нулевая).
	if kind := strings.ToLower(strings.TrimSpace(wh.Payload.Type)); kind == "trial" || kind == "gift" || paid <= 0 {
		a.payLog(ctx, model.PayMethodTribute, extID, chatID, "not_a_payment",
			"событие без оплаты (тип=%q, сумма=%d) — подписка не выдана%s", wh.Payload.Type, paid, wh.who())
		return true, nil
	}
	// Неизвестный период не продаётся. Раньше он молча становился месяцем —
	// то есть бот продавал срок, которого не понимает.
	if !periodOK {
		a.tributeRejected(ctx, extID, chatID, amount, "неизвестный период %q%s", wh.Payload.Period, wh.who())
		return true, nil
	}
	saleMonths := months
	if saleMonths == 0 {
		// Недельная подписка: условия берём по ближайшему проданному сроку —
		// месячному. Если и его нет, продавать нечего.
		saleMonths = 1
	}
	plan, reason, err := a.tributePlanFor(ctx, cfg, wh.Payload.SubscriptionID)
	if err != nil {
		// Хранилище недоступно: ошибка уходит в 5xx, и Tribute повторит доставку.
		a.payLog(ctx, model.PayMethodTribute, extID, chatID, "error", "тариф подписки не прочитан: %v", err)
		return false, fmt.Errorf("tribute plan %s: %w", extID, err)
	}
	if reason != "" {
		a.tributeRejected(ctx, extID, chatID, amount, "%s%s", reason, wh.who())
		return true, nil
	}
	// Срок обязан продаваться в тарифе: из него берутся трафик, лимит
	// устройств и сквады. Для срока вне сетки трафик равен нулю, а ноль в
	// панели означает БЕЗЛИМИТ — покупатель получал бы безлимит по чужой цене.
	// Проверка та же, что у всех остальных путей продажи.
	var snap *model.PlanSnapshot
	if plan == nil {
		if !a.periodOnSale(saleMonths) {
			a.tributeRejected(ctx, extID, chatID, amount, "в тарифе «Базовый» нет срока %d мес. (period=%q)%s", saleMonths, wh.Payload.Period, wh.who())
			return true, nil
		}
		snap = a.planSnapshot(saleMonths)
	} else {
		d := plan.Duration(saleMonths)
		if d == nil || d.Base == "" {
			a.tributeRejected(ctx, extID, chatID, amount, "в тарифе %q нет срока %d мес. (period=%q)%s", plan.Code, saleMonths, wh.Payload.Period, wh.who())
			return true, nil
		}
		snap = a.planSnapshotOf(plan, d, saleMonths)
	}
	// Фактический срок в днях — в снимок: ядро выдачи по нему продлевает днями,
	// а не календарными месяцами. Цена снимка — цена месячного срока тарифа,
	// поэтому стоимость недели оценивается долей от неё (Paid): иначе при
	// смене тарифа неделя зачлась бы как месяц. Рублёвая оплата в рублёвый
	// тариф перезапишет оценку фактической суммой.
	if snap != nil && days > 0 {
		snap.Days = days
		snap.Paid = scaledPrice(snap.Price, days, saleMonths*30)
	}
	link, expireAt, err := a.finalizePurchase(ctx, chatID, saleMonths, model.PayMethodTribute, amount, extID, snap)
	if errors.Is(err, storage.ErrDuplicateExtID) {
		// Параллельная доставка того же события уже выдала подписку.
		return true, nil
	}
	if err != nil {
		a.payLog(ctx, model.PayMethodTribute, extID, chatID, "finalize_error", "%v", err)
		return false, fmt.Errorf("tribute finalize %s: %w", extID, err)
	}
	a.sendSubActive(ctx, chatID, link, expireAt)
	a.log.Info("tribute webhook: finalized", "chat_id", chatID, "months", saleMonths, "days", days)
	return true, nil
}

// scaledPrice — доля цены срока в periodDays дней на days дней. Пусто, если
// цену не разобрать.
func scaledPrice(price string, days, periodDays int) string {
	k, ok := rubToKopecks(price)
	if !ok || k <= 0 || days <= 0 || periodDays <= 0 {
		return ""
	}
	v := (k*int64(days) + int64(periodDays)/2) / int64(periodDays)
	if v <= 0 {
		return ""
	}
	return kopecksToRub(v)
}

// tributePlanFor — какой тариф продаёт подписка Tribute. nil без причины —
// «Базовый» (сетка конфига); непустая причина — оплату выдавать нельзя;
// ошибка — хранилище недоступно.
func (a *App) tributePlanFor(ctx context.Context, cfg model.TributeConfig, subID int64) (*model.Plan, string, error) {
	l := cfg.LinkBySub(subID)
	if l == nil {
		// Подписки без привязки выдают «Базовый» по периоду, как до появления
		// привязок, — пока у «Базового» не задан ID своей подписки. Тогда
		// непривязанная подписка — чужой канал автора, и выдавать по ней нечего.
		if cfg.StrictSubs() {
			return nil, fmt.Sprintf("подписка Tribute #%d не привязана ни к одному тарифу", subID), nil
		}
		return nil, "", nil
	}
	if l.Plan == model.PlanCodeBase {
		return nil, "", nil
	}
	p, err := a.planByCode(ctx, l.Plan)
	if err != nil {
		return nil, "", err
	}
	if p == nil {
		return nil, fmt.Sprintf("подписка Tribute #%d привязана к тарифу %q, которого больше нет", subID, l.Plan), nil
	}
	return p, "", nil
}

// tributeRejected — оплата принята Tribute, но выдать по ней нечего. Отвечаем
// 200: ретраи сутки подряд ничего не изменят, а человека и админа надо звать
// сейчас.
func (a *App) tributeRejected(ctx context.Context, extID string, chatID int64, amount, format string, args ...any) {
	reason := fmt.Sprintf(format, args...)
	a.payLog(ctx, model.PayMethodTribute, extID, chatID, "error", "%s — подписка не выдана", reason)
	alang := a.lang(a.cfg.AdminID)
	a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "admin.trb_rejected",
		html.EscapeString(extID), html.EscapeString(amount), html.EscapeString(reason), a.userLabelByID(ctx, chatID)))
	a.notify(ctx, chatID, i18n.T(a.lang(chatID), "trb.user_rejected"))
}

// tributeRefunded — Tribute вернул деньги по цифровому товару. Событий возврата
// по ПОДПИСКАМ провайдер не присылает вовсе (отмена подписки — это выключение
// автопродления, а не возврат), поэтому здесь обрабатывается единственное
// refund-событие, которое существует.
//
// Доступ не отзывается: возврат мог сделать сам админ по договорённости.
// Платёж помечается возвращённым — он перестаёт быть выручкой и перестаёт
// закрывать тарифы «только новым».
func (a *App) tributeRefunded(ctx context.Context, wh tributeWebhook) {
	chatID := wh.Payload.TelegramUserID
	extID := wh.Payload.PurchaseID
	amount := tributeAmount(wh.Payload.Amount, wh.Payload.Currency)
	a.payLog(ctx, model.PayMethodTribute, extID, chatID, "refunded",
		"возврат %s по покупке %q (причина: %s)%s", amount, extID, wh.Payload.RefundReason, wh.who())
	if a.store != nil && extID != "" {
		if err := a.store.SetPaymentStatus(ctx, extID, model.PaymentRefunded); err != nil {
			a.log.Warn("платёж не помечен возвращённым", "err", err, "ext_id", extID)
		}
	}
	alang := a.lang(a.cfg.AdminID)
	a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "admin.refunded",
		model.PayMethodTribute, extID, a.userLabelByID(ctx, chatID), amount))
}

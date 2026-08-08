package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/storage"
	"remnabot/internal/yookassa"
)

// Автосписание (автопродление) на базе ЮKassa.
//
// Как это работает:
//  1. При оплате пользователь выбирает «с автопродлением» — бот создаёт платёж
//     с save_payment_method=true, ЮKassa показывает согласие на автоплатежи и
//     после успешной оплаты возвращает payment_method.id.
//  2. Этот id сохраняется в таблице autopay вместе с периодом подписки.
//  3. Планировщик RunAutoPay раз в час смотрит, у кого подписка кончается
//     через AutoPayDays дней, и списывает деньги сохранённым способом.
//  4. Пользователь в любой момент выключает автопродление одной кнопкой
//     («Моя подписка» → «Автопродление»), в мини-аппе и в веб-кабинете.

const autoPayTick = time.Hour

// autoPayRetryDelay — пауза между неудачными попытками списания (карта не
// прошла), autoPayRetrySoon — короткая пауза, когда виноваты не деньги
// пользователя (обрыв связи, сбой или неверные настройки ЮKassa).
const (
	autoPayRetryDelay = 24 * time.Hour
	autoPayRetrySoon  = time.Hour
)

func (a *App) autoPayCfg() model.YooKassaConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.YooKassaConfig{}
	}
	a.botCfg.NormalizeYooKassa()
	return a.botCfg.YooKassa
}

// autoPayAvailable сообщает, можно ли вообще предлагать автопродление:
// ЮKassa включена, ключи заданы и админ включил автоплатежи.
func (a *App) autoPayAvailable() bool {
	cfg := a.autoPayCfg()
	return cfg.Enabled && cfg.AutoPay && cfg.ShopID != "" && cfg.SecretKey != ""
}

// getAutoPay возвращает запись автосписания пользователя (nil, если нет).
func (a *App) getAutoPay(ctx context.Context, chatID int64) *model.AutoPay {
	if a.store == nil {
		return nil
	}
	ap, err := a.store.GetAutoPay(ctx, chatID)
	if err != nil {
		a.log.Warn("autopay: чтение записи", "tg_id", chatID, "err", err)
		return nil
	}
	return ap
}

// autoPayOn сообщает, включено ли автопродление у пользователя прямо сейчас.
func (a *App) autoPayOn(ctx context.Context, chatID int64) bool {
	ap := a.getAutoPay(ctx, chatID)
	return ap != nil && ap.Enabled && ap.MethodID != ""
}

// saveAutoPayFromPayment запоминает сохранённый ЮKassa способ оплаты после
// успешного платежа и ПРЕДЛАГАЕТ пользователю подключить автопродление.
// Само по себе сохранение карты списаний не включает: запись создаётся
// выключенной, деньги начнут списываться только после явного «Подключить».
// Вызывается и из вебхука, и из ручной проверки платежа; повторный вызов не
// перетирает уже принятое пользователем решение.
func (a *App) saveAutoPayFromPayment(ctx context.Context, chatID int64, months int, pay *yookassa.Payment, snap *model.PlanSnapshot) {
	if a.store == nil || pay == nil || chatID == 0 {
		return
	}
	if pay.Metadata["autopay"] != "1" || !pay.PaymentMethod.Saved || pay.PaymentMethod.ID == "" {
		return
	}
	if months <= 0 {
		months = model.PlanMonths[0]
	}
	prev := a.getAutoPay(ctx, chatID)
	// Уже подключено — просто обновляем карту и период, ничего не спрашиваем.
	alreadyOn := prev != nil && prev.Enabled && prev.MethodID != ""
	ap := &model.AutoPay{
		TelegramID: chatID,
		Method:     model.PayMethodYooKassa,
		MethodID:   pay.PaymentMethod.ID,
		Title:      pay.SavedMethodTitle(),
		Months:     months,
		Amount:     pay.Amount.Value,
		Currency:   pay.Amount.Currency,
		Enabled:    alreadyOn,
		LastPayAt:  time.Now().UTC().Format(time.RFC3339),
		// Условия последнего продления. Служат для сравнения: если они
		// изменились, человека надо предупредить — само продление всегда идёт
		// по действующему тарифу.
		Snapshot: snap,
	}
	if ap.Snapshot == nil {
		ap.Snapshot = a.planSnapshot(months)
	}
	if prev != nil {
		ap.CreatedAt = prev.CreatedAt
		// Отметку «за этот период уже списано» сохраняем: ручная оплата с
		// сохранением карты не должна открывать дорогу повторному списанию за
		// период, на котором застряло продление.
		ap.PaidPeriod = prev.PaidPeriod
	}
	if err := a.store.SetAutoPay(ctx, ap); err != nil {
		a.log.Warn("autopay: сохранение способа оплаты", "tg_id", chatID, "err", err)
		return
	}
	a.payLog(ctx, model.PayMethodYooKassa, pay.ID, chatID, "autopay_saved", "months=%d method=%s enabled=%v", months, ap.Title, alreadyOn)
	lang := a.lang(chatID)
	if alreadyOn {
		// Купили другой период — регулярное списание меняется, молчать нельзя.
		// Молчать нельзя не только при смене срока: при тех же месяцах могли
		// поменяться и сумма, и условия — регулярное списание станет другим.
		if prev.Months != months || prev.Snapshot.Fingerprint() != ap.Snapshot.Fingerprint() {
			a.notifyKB(ctx, chatID, i18n.T(lang, "ap.period_changed", monthsWord(lang, months), a.autoPayDaysText(lang)),
				[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "ap.btn_manage"), "ap:show")}})
		}
		return
	}
	if !a.autoPayAvailable() {
		return
	}
	// Предложение подключить автопродление — после успешной оплаты, когда
	// пользователь уже получил доступ и ничем не рискует.
	a.notifyKB(ctx, chatID, i18n.T(lang, "ap.offer", monthsWord(lang, months), a.autoPayDaysText(lang)),
		[][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "ap.btn_enable_now"), "ap:on")},
			{btn(i18n.T(lang, "ap.btn_decline"), "ap:no")},
		})
}

// autoPayRow — строка «🔁 Автопродление: вкл/выкл» для экранов подписки.
// Показывается, если автопродление доступно в магазине или уже подключено у
// этого пользователя (выключить его должно быть можно всегда, в том числе когда
// подписка уже кончилась или заблокирована).
func (a *App) autoPayRow(ctx context.Context, chatID int64, lang string) []models.InlineKeyboardButton {
	on := a.autoPayOn(ctx, chatID)
	if !a.autoPayAvailable() && !on {
		return nil
	}
	state := i18n.T(lang, "ap.state_off")
	if on {
		state = i18n.T(lang, "ap.state_on")
	}
	return []models.InlineKeyboardButton{btn(i18n.T(lang, "ap.btn_row", state), "ap:show")}
}

// autoPayDaysText — человеческая формулировка «когда спишем».
func (a *App) autoPayDaysText(lang string) string {
	d := a.autoPayCfg().AutoPayDays
	if d <= 0 {
		return i18n.T(lang, "ap.when_expiry")
	}
	return i18n.T(lang, "ap.when_days", d)
}

// showAutoPay — пользовательский экран автопродления: что это, когда спишется,
// и кнопка выключения/включения.
func (a *App) showAutoPay(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	ap := a.getAutoPay(ctx, chatID)
	on := ap != nil && ap.Enabled && ap.MethodID != ""
	back := []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:mysubs"), btn(i18n.T(lang, "btn.home"), "menu:home")}

	if !a.autoPayAvailable() && !on {
		a.sendKBSection(ctx, chatID, assets.SectionMySubscription, i18n.T(lang, "ap.unavailable"),
			[][]models.InlineKeyboardButton{back})
		return
	}
	if !on {
		text := i18n.T(lang, "ap.off_title", a.autoPayDaysText(lang))
		rows := [][]models.InlineKeyboardButton{}
		if ap != nil && ap.MethodID != "" {
			// Способ оплаты уже сохранён — включаем обратно одной кнопкой.
			rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "ap.btn_on"), "ap:on")})
		} else {
			rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "ap.btn_buy"), "menu:buy")})
		}
		rows = append(rows, back)
		a.sendKBSection(ctx, chatID, assets.SectionMySubscription, text, rows)
		return
	}

	price := a.autoPayPrice(ap.Months)
	if price == "" {
		price = ap.Amount
	}
	card := ap.Title
	if card == "" {
		card = i18n.T(lang, "ap.card_unknown")
	}
	cur := ap.Currency
	if pr := a.pricing().Currency; pr != "" {
		cur = pr
	}
	text := i18n.T(lang, "ap.on_title", monthsWord(lang, ap.Months), price+curSuffix(curSymbol(cur)), card, a.autoPayDaysText(lang))
	if ap.Fails > 0 {
		text += "\n\n" + i18n.T(lang, "ap.fails", ap.Fails, model.AutoPayMaxFails)
	}
	a.sendKBSection(ctx, chatID, assets.SectionMySubscription, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "ap.btn_off"), "ap:off")},
		back,
	})
}

// onAutoPayUser обрабатывает пользовательские кнопки автопродления.
func (a *App) onAutoPayUser(ctx context.Context, chatID int64, val string) {
	action, _, _ := strings.Cut(val, ":")
	lang := a.lang(chatID)
	switch action {
	case "show":
		a.showAutoPay(ctx, chatID)
	case "off":
		if a.store != nil {
			_ = a.store.SetAutoPayEnabled(ctx, chatID, false)
		}
		a.payLog(ctx, model.PayMethodYooKassa, "", chatID, "autopay_off", "выключено пользователем")
		a.sendKBSection(ctx, chatID, assets.SectionMySubscription, i18n.T(lang, "ap.turned_off"),
			[][]models.InlineKeyboardButton{
				{btn(i18n.T(lang, "ap.btn_on"), "ap:on")},
				{btn(i18n.T(lang, "btn.back"), "menu:mysubs"), btn(i18n.T(lang, "btn.home"), "menu:home")},
			})
	case "on":
		ap := a.getAutoPay(ctx, chatID)
		if ap == nil || ap.MethodID == "" || !a.autoPayAvailable() {
			a.showAutoPay(ctx, chatID)
			return
		}
		if a.store != nil {
			_ = a.store.SetAutoPayEnabled(ctx, chatID, true)
		}
		a.payLog(ctx, model.PayMethodYooKassa, "", chatID, "autopay_on", "включено пользователем")
		a.sendKBSection(ctx, chatID, assets.SectionMySubscription,
			i18n.T(lang, "ap.turned_on", monthsWord(lang, ap.Months), a.autoPayDaysText(lang)),
			[][]models.InlineKeyboardButton{
				{btn(i18n.T(lang, "ap.btn_off"), "ap:off")},
				{btn(i18n.T(lang, "btn.back"), "menu:mysubs"), btn(i18n.T(lang, "btn.home"), "menu:home")},
			})
	case "no":
		// Отказ от предложения: карта остаётся сохранённой в ЮKassa, но
		// списаний нет — включить можно позже той же кнопкой.
		if a.store != nil {
			_ = a.store.SetAutoPayEnabled(ctx, chatID, false)
		}
		a.sendKBSection(ctx, chatID, assets.SectionMySubscription, i18n.T(lang, "ap.declined"),
			[][]models.InlineKeyboardButton{
				{btn(i18n.T(lang, "ap.btn_on"), "ap:on")},
				{btn(i18n.T(lang, "btn.home"), "menu:home")},
			})
	}
}

// autoPayPrice — актуальная цена периода для автосписания.
func (a *App) autoPayPrice(months int) string {
	return a.pricing().Fiat(model.PayMethodYooKassa, months)
}

// RunAutoPay — планировщик автосписаний: раз в час ищет подписки, которым пора
// продлеваться, и списывает деньги сохранённым способом оплаты.
func (a *App) RunAutoPay(ctx context.Context) {
	t := time.NewTicker(autoPayTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.autoPayOnce(ctx)
		}
	}
}

func (a *App) autoPayOnce(ctx context.Context) {
	if a.store == nil || !a.autoPayAvailable() {
		return
	}
	list, err := a.store.ListAutoPay(ctx)
	if err != nil {
		a.log.Warn("autopay: список", "err", err)
		return
	}
	now := time.Now().UTC()
	shopIssue := ""
	for i := range list {
		ap := list[i]
		exp, due := a.autoPayDue(ctx, &ap, now)
		if !due {
			continue
		}
		if reason := a.chargeAutoPay(ctx, &ap, now, exp); reason != "" {
			shopIssue = reason
		}
	}
	// Проблема магазина (нет цены, неверные ключи, сбой ЮKassa) касается сразу
	// всех подписчиков — админу уходит ОДНО сообщение за проход, а не по штуке
	// на пользователя, и сами пользователи такими ошибками не тревожатся.
	if shopIssue != "" {
		alang := a.lang(a.cfg.AdminID)
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "ap.admin_shop_issue", escapeName(shopIssue)))
	}
}

// autoPayDue решает, пора ли списывать, и возвращает дату окончания подписки
// (она же — период, за который списываем). Условия: запись включена, способ
// оплаты есть, пользователь не заблокирован, пауза после прошлой попытки
// прошла, с прошлого успешного списания прошли сутки и до конца подписки
// осталось не больше AutoPayDays дней.
func (a *App) autoPayDue(ctx context.Context, ap *model.AutoPay, now time.Time) (time.Time, bool) {
	if !ap.Enabled || ap.MethodID == "" || ap.Method != model.PayMethodYooKassa {
		return time.Time{}, false
	}
	if ap.NextTryAt != "" {
		if t, err := time.Parse(time.RFC3339, ap.NextTryAt); err == nil && t.After(now) {
			return time.Time{}, false
		}
	}
	// Страховка от повторного списания: если оплата (ручная или автоматическая)
	// была меньше суток назад, не списываем, даже если срок подписки почему-то
	// не сдвинулся.
	if ap.LastPayAt != "" {
		if t, err := time.Parse(time.RFC3339, ap.LastPayAt); err == nil && now.Sub(t) < 24*time.Hour {
			return time.Time{}, false
		}
	}
	u, err := a.store.GetUser(ctx, ap.TelegramID)
	if err != nil || u == nil || u.Blocked {
		return time.Time{}, false
	}
	if u.SubExpireAt == "" {
		return time.Time{}, false
	}
	exp, err := time.Parse(time.RFC3339, u.SubExpireAt)
	if err != nil {
		return time.Time{}, false
	}
	days := a.autoPayCfg().AutoPayDays
	if now.Before(exp.Add(-time.Duration(days) * 24 * time.Hour)) {
		return time.Time{}, false
	}
	// За этот период деньги уже списаны: если продление в панели не удалось,
	// срок подписки не сдвинулся — но списывать второй раз нельзя. Платёж лежит
	// в очереди незавершённых, его добьёт реконсилятор.
	if ap.PaidPeriod != "" && ap.PaidPeriod == autoPayPeriod(exp) {
		return time.Time{}, false
	}
	return exp, true
}

// autoPayPeriod — метка периода подписки (дата окончания), за который списываем.
func autoPayPeriod(exp time.Time) string { return exp.UTC().Format("20060102") }

// curSymbol печатает валюту так же, как остальной бот: рубли — символом,
// прочие — кодом.
func curSymbol(cur string) string {
	switch strings.ToUpper(cur) {
	case "", "RUB", "RUR":
		return curRUB
	}
	return strings.ToUpper(cur)
}

// currencyCode проверяет, что валюта задана трёхбуквенным кодом (а не, скажем,
// символом «₽», в котором тоже три байта) — иначе ЮKassa вернёт 400.
func currencyCode(cur string) bool {
	if len([]rune(cur)) != 3 {
		return false
	}
	for _, r := range strings.ToUpper(cur) {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// chargeAutoPay делает одну попытку списания за период, заканчивающийся exp.
// Возвращает непустую строку, если проблема на стороне магазина (её показываем
// админу один раз за проход планировщика, а пользователя не трогаем).
func (a *App) chargeAutoPay(ctx context.Context, ap *model.AutoPay, now, exp time.Time) string {
	// Проход по большой базе длится минуты: пользователь мог выключить
	// автопродление после снятия снапшота ListAutoPay — перечитываем запись
	// непосредственно перед списанием.
	if cur := a.getAutoPay(ctx, ap.TelegramID); cur == nil || !cur.Enabled {
		return ""
	}
	lang := a.lang(ap.TelegramID)
	months := ap.Months
	if months <= 0 {
		months = model.PlanMonths[0]
	}
	// Продление — новая сделка на действующих условиях: человек платит
	// сегодняшнюю цену, значит и лимиты получает сегодняшние. Снимок здесь
	// не «достаётся из подписки», а снимается заново — он фиксирует условия
	// на время между списанием и финализацией (её могут добить вебхук или
	// реконсилятор через минуты).
	snap := a.planSnapshot(months)
	pr := a.pricing()
	value, okPrice := ykValue(pr.Fiat(model.PayMethodYooKassa, months))
	currency := strings.ToUpper(pr.Currency)
	if !currencyCode(currency) {
		currency = "RUB"
	}
	client := a.ykClient()
	if !okPrice || client == nil {
		reason := "нет цены (или цена некорректна) для периода " + strconv.Itoa(months) + " мес."
		if client == nil {
			reason = "ЮKassa не настроена"
		}
		a.autoPayDefer(ctx, ap, now, autoPayRetryDelay, reason)
		return reason
	}
	// Ключ идемпотентности привязан к периоду, а не к дате попытки: повтор
	// после обрыва связи попадёт в тот же платёж, а не создаст второй.
	// Ключ НЕ включает сумму и условия намеренно: повтор после обрыва связи
	// обязан попасть в тот же платёж, иначе человека спишут дважды. Защита от
	// «вернулся старый платёж по старой цене» сделана проверкой суммы ниже.
	idem := fmt.Sprintf("ap-%d-%s-%d", ap.TelegramID, autoPayPeriod(exp), ap.Fails)
	desc := i18n.T(lang, "yk.invoice_desc", months)
	pay, err := client.ChargeSaved(ctx, ap.MethodID, value, currency, desc, ap.TelegramID, months, idem)
	if err != nil {
		a.payLog(ctx, model.PayMethodYooKassa, "", ap.TelegramID, "autocharge_error", "months=%d: %v", months, err)
		var apiErr *yookassa.APIError
		switch {
		case errors.As(err, &apiErr) && apiErr.ShopSide():
			// Ключи, неверный запрос или сбой ЮKassa: карта пользователя ни при
			// чём. Первую попытку повторяем через час, дальше раз в сутки —
			// чтобы неустранимая проблема не молотила вечно каждый час.
			delay := autoPayRetrySoon
			if ap.LastError != "" {
				delay = autoPayRetryDelay
			}
			a.autoPayDefer(ctx, ap, now, delay, err.Error())
			return err.Error()
		case errors.As(err, &apiErr):
			// Осмысленный отказ (например, способ оплаты недействителен).
			a.autoPayFail(ctx, ap, now, err.Error())
		default:
			// Обрыв связи: платёж мог создаться. Счётчик неудач НЕ трогаем,
			// чтобы повтор ушёл с тем же ключом идемпотентности.
			a.autoPayDefer(ctx, ap, now, autoPayRetrySoon, err.Error())
		}
		return ""
	}
	a.payLog(ctx, model.PayMethodYooKassa, pay.ID, ap.TelegramID, "autocharge", "months=%d amount=%s status=%s", months, value, pay.Status)

	// Окно идемпотентности ЮKassa — сутки. Если цена изменилась между
	// попытками одного периода, по тому же ключу вернётся ПРЕЖНИЙ платёж со
	// старой суммой. Продление при этом НЕ останавливаем: деньги уже списаны,
	// и оставить человека без подписки, забрав оплату, — худший из исходов.
	// Обработка идёт как обычно, по фактической сумме платежа; расхождение
	// уходит в журнал и админу, чтобы он свёл цифры.
	if pay.Amount.Value != "" && !sameMoney(pay.Amount.Value, value) {
		a.payLog(ctx, model.PayMethodYooKassa, pay.ID, ap.TelegramID, "autocharge_amount_mismatch",
			"ожидали %s %s, платёж на %s %s — продлеваем по факту", value, currency, pay.Amount.Value, pay.Amount.Currency)
		alang := a.lang(a.cfg.AdminID)
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "ap.admin_amount_mismatch",
			a.userLabelByID(ctx, ap.TelegramID), value+" "+currency, pay.Amount.Value+" "+pay.Amount.Currency))
	}

	if pay.Status != "succeeded" || !pay.Paid {
		if pay.Status == "pending" || pay.Status == "waiting_for_capture" {
			// Платёж ещё в процессе — финализирует вебхук или реконсилятор.
			// Повтор через час, НЕ через сутки: окно идемпотентности ЮKassa —
			// 24 часа, и суточная отсрочка уводила бы повтор с тем же ключом за
			// его границу — ЮKassa обработала бы запрос как НОВЫЙ платёж
			// (двойное списание). Часовые повторы тем же ключом просто
			// возвращают текущее состояние того же платежа.
			a.autoPayEnqueue(ctx, pay.ID, ap.TelegramID, months, snap)
			a.autoPayDefer(ctx, ap, now, autoPayRetrySoon, "ожидает подтверждения")
			return ""
		}
		reason := "платёж не прошёл: " + pay.Status
		if r := pay.CancellationDetails.Reason; r != "" {
			reason += " (" + r + ")"
		}
		a.autoPayFail(ctx, ap, now, reason)
		return ""
	}

	// Деньги уже списаны. Сначала фиксируем платёж как незавершённый: если
	// продление в панели упадёт, его добьёт реконсилятор, и оплата не пропадёт.
	pi := a.autoPayEnqueue(ctx, pay.ID, ap.TelegramID, months, snap)
	// И сразу помечаем период оплаченным: повторно списывать за него нельзя,
	// чем бы ни закончилось продление.
	stamp := now.Format(time.RFC3339)
	_ = a.store.MarkAutoPayCharged(ctx, ap.TelegramID, stamp, autoPayPeriod(exp), "", "")
	amount := pay.Amount.Value + " " + pay.Amount.Currency
	_, expireAt, err := a.finalizePurchase(ctx, ap.TelegramID, months, model.PayMethodYooKassa, amount, pay.ID, snap)
	if err != nil {
		if errors.Is(err, storage.ErrDuplicateExtID) {
			// Гонку выиграл вебхук — подписка продлена им, повторное сообщение
			// пользователю не шлём.
			a.autoPayResolve(ctx, pi)
			return ""
		}
		a.log.Warn("autopay: финализация", "tg_id", ap.TelegramID, "err", err)
		_ = a.store.MarkAutoPayCharged(ctx, ap.TelegramID, stamp, autoPayPeriod(exp),
			now.Add(autoPayRetryDelay).Format(time.RFC3339), err.Error())
		// Деньги списаны, а подписка не продлена — это ЧП, админ должен узнать
		// сразу, не дожидаясь, пока реконсилятор сдастся.
		alang := a.lang(a.cfg.AdminID)
		a.notifyKB(ctx, a.cfg.AdminID,
			i18n.T(alang, "ap.admin_stuck", a.userLabelByID(ctx, ap.TelegramID), amount, escapeName(err.Error())),
			[][]models.InlineKeyboardButton{{btn(i18n.T(alang, "guard.btn_card"), "usr:view:"+strconv.FormatInt(ap.TelegramID, 10))}})
		return ""
	}
	a.autoPayResolve(ctx, pi)
	a.notifyKB(ctx, ap.TelegramID,
		i18n.T(lang, "ap.charged", monthsWord(lang, months), value+curSuffix(curSymbol(currency)), formatExpire(expireAt, lang)),
		[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "ap.btn_manage"), "ap:show")}})
	a.log.Info("autopay: подписка продлена", "tg_id", ap.TelegramID, "months", months, "expire", expireAt)
	return ""
}

// sameMoney сравнивает две денежные строки в формате ЮKassa («199.50»).
// Сравнение по копейкам, а не побайтно: «199.5» и «199.50» — одна сумма.
func sameMoney(a, b string) bool {
	ka, oka := rubToKopecks(a)
	kb, okb := rubToKopecks(b)
	if !oka || !okb {
		return a == b
	}
	return ka == kb
}

// autoPayEnqueue кладёт платёж в очередь незавершённых, если его там ещё нет
// (повторная попытка возвращает тот же платёж по ключу идемпотентности — дубли
// в очереди не нужны).
func (a *App) autoPayEnqueue(ctx context.Context, extID string, tgID int64, months int, snap *model.PlanSnapshot) *model.PendingInvoice {
	if a.store == nil || extID == "" {
		return nil
	}
	if p, _ := a.store.PendingByExtID(ctx, extID); p != nil {
		return p
	}
	// Снимок кладём в очередь: продление часто добивают вебхук или
	// реконсилятор, и условия они возьмут именно отсюда.
	pi := &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: extID, TelegramID: tgID, Months: months, Snapshot: snap}
	if err := a.store.AddPendingInvoice(ctx, pi); err != nil {
		a.log.Warn("autopay: очередь платежей", "ext_id", extID, "err", err)
		return nil
	}
	return pi
}

// autoPayResolve закрывает запись очереди после успешного продления.
func (a *App) autoPayResolve(ctx context.Context, pi *model.PendingInvoice) {
	if a.store != nil && pi != nil {
		_ = a.store.ResolvePending(ctx, pi.ID)
	}
}

// autoPayDefer откладывает следующую попытку, не считая её неудачей
// пользователя: так проблемы магазина и обрывы связи не выключают
// автопродление и не пугают человека сообщениями про карту.
func (a *App) autoPayDefer(ctx context.Context, ap *model.AutoPay, now time.Time, delay time.Duration, reason string) {
	if a.store == nil {
		return
	}
	_ = a.store.UpdateAutoPayResult(ctx, ap.TelegramID, ap.LastPayAt,
		now.Add(delay).Format(time.RFC3339), ap.Fails, reason)
}

// autoPayFail фиксирует неудачную попытку (карта отклонена) и предупреждает
// пользователя.
func (a *App) autoPayFail(ctx context.Context, ap *model.AutoPay, now time.Time, reason string) {
	lang := a.lang(ap.TelegramID)
	fails := ap.Fails + 1
	if fails >= model.AutoPayMaxFails {
		if a.store != nil {
			_ = a.store.SetAutoPayEnabled(ctx, ap.TelegramID, false)
			_ = a.store.UpdateAutoPayResult(ctx, ap.TelegramID, ap.LastPayAt, "", fails, reason)
		}
		a.payLog(ctx, model.PayMethodYooKassa, "", ap.TelegramID, "autopay_disabled", "%d неудач подряд: %s", fails, reason)
		a.notifyKB(ctx, ap.TelegramID, i18n.T(lang, "ap.disabled_fail"),
			[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.buy"), "menu:buy")}})
		a.notifyAutoPayFailAdmin(ctx, ap.TelegramID, fails, reason)
		return
	}
	if a.store != nil {
		_ = a.store.UpdateAutoPayResult(ctx, ap.TelegramID, ap.LastPayAt,
			now.Add(autoPayRetryDelay).Format(time.RFC3339), fails, reason)
	}
	a.notifyKB(ctx, ap.TelegramID, i18n.T(lang, "ap.charge_failed"),
		[][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
			{btn(i18n.T(lang, "ap.btn_manage"), "ap:show")},
		})
	if fails == 1 {
		// Админа дёргаем на первой неудаче и на итоговом отключении — иначе
		// один «умерший» способ оплаты завалит его тремя сообщениями подряд.
		a.notifyAutoPayFailAdmin(ctx, ap.TelegramID, fails, reason)
	}
}

// notifyAutoPayFailAdmin сообщает админу о проблеме с автосписанием. Успешные
// списания админу не шлём — они видны в истории платежей и в логе заказа.
func (a *App) notifyAutoPayFailAdmin(ctx context.Context, uid int64, fails int, reason string) {
	alang := a.lang(a.cfg.AdminID)
	key := "ap.admin_fail"
	if fails >= model.AutoPayMaxFails {
		key = "ap.admin_disabled"
	}
	a.notifyKB(ctx, a.cfg.AdminID, i18n.T(alang, key, a.userLabelByID(ctx, uid), escapeName(reason)),
		[][]models.InlineKeyboardButton{{btn(i18n.T(alang, "guard.btn_card"), "usr:view:"+strconv.FormatInt(uid, 10))}})
}

// SetAutoPayEnabled — вход для мини-аппа и веб-кабинета: пользователь включает
// или выключает автопродление у себя.
func (a *App) SetAutoPayEnabled(ctx context.Context, tgID int64, on bool) error {
	if a.store == nil {
		return fmt.Errorf("хранилище недоступно")
	}
	ap, err := a.store.GetAutoPay(ctx, tgID)
	if err != nil {
		return err
	}
	if ap == nil || ap.MethodID == "" {
		return fmt.Errorf("автопродление ещё не подключено")
	}
	if on && !a.autoPayAvailable() {
		return fmt.Errorf("автопродление отключено администратором")
	}
	return a.store.SetAutoPayEnabled(ctx, tgID, on)
}

// AutoPayState отдаёт состояние автопродления наружу (мини-апп / кабинет).
func (a *App) AutoPayState(ctx context.Context, tgID int64) (available, on bool, months int, title string) {
	available = a.autoPayAvailable()
	ap := a.getAutoPay(ctx, tgID)
	if ap == nil {
		return available, false, 0, ""
	}
	return available, ap.Enabled && ap.MethodID != "", ap.Months, ap.Title
}

// monthsWord — «1 месяц» / «3 месяца» / «12 месяцев» для текстов.
func monthsWord(lang string, months int) string {
	if lang == model.LangEN {
		if months == 1 {
			return "1 month"
		}
		return strconv.Itoa(months) + " months"
	}
	switch {
	case months%10 == 1 && months%100 != 11:
		return strconv.Itoa(months) + " месяц"
	case months%10 >= 2 && months%10 <= 4 && (months%100 < 12 || months%100 > 14):
		return strconv.Itoa(months) + " месяца"
	default:
		return strconv.Itoa(months) + " месяцев"
	}
}

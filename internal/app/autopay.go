package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
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

// autoPayRetryDelay — пауза между неудачными попытками списания.
const autoPayRetryDelay = 24 * time.Hour

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
func (a *App) saveAutoPayFromPayment(ctx context.Context, chatID int64, months int, pay *yookassa.Payment) {
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
	}
	if prev != nil {
		ap.CreatedAt = prev.CreatedAt
	}
	if err := a.store.SetAutoPay(ctx, ap); err != nil {
		a.log.Warn("autopay: сохранение способа оплаты", "tg_id", chatID, "err", err)
		return
	}
	a.payLog(ctx, model.PayMethodYooKassa, pay.ID, chatID, "autopay_saved", "months=%d method=%s enabled=%v", months, ap.Title, alreadyOn)
	lang := a.lang(chatID)
	if alreadyOn {
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
	text := i18n.T(lang, "ap.on_title", monthsWord(lang, ap.Months), price+curSuffix(curRUB), card, a.autoPayDaysText(lang))
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
	action, arg, _ := strings.Cut(val, ":")
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
	case "pay":
		// Выбор на экране оплаты ЮKassa: с автопродлением или разовый платёж.
		a.ykStart(ctx, chatID, arg == "1")
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
	for i := range list {
		ap := list[i]
		if !a.autoPayDue(ctx, &ap, now) {
			continue
		}
		a.chargeAutoPay(ctx, &ap, now)
	}
}

// autoPayDue решает, пора ли списывать: запись включена, способ оплаты есть,
// пользователь не заблокирован, пауза после неудачи прошла и до конца подписки
// осталось не больше AutoPayDays дней.
func (a *App) autoPayDue(ctx context.Context, ap *model.AutoPay, now time.Time) bool {
	if !ap.Enabled || ap.MethodID == "" || ap.Method != model.PayMethodYooKassa {
		return false
	}
	if ap.NextTryAt != "" {
		if t, err := time.Parse(time.RFC3339, ap.NextTryAt); err == nil && t.After(now) {
			return false
		}
	}
	u, err := a.store.GetUser(ctx, ap.TelegramID)
	if err != nil || u == nil || u.Blocked {
		return false
	}
	if u.SubExpireAt == "" {
		return false
	}
	exp, err := time.Parse(time.RFC3339, u.SubExpireAt)
	if err != nil {
		return false
	}
	days := a.autoPayCfg().AutoPayDays
	return !now.Before(exp.Add(-time.Duration(days) * 24 * time.Hour))
}

// chargeAutoPay делает одну попытку списания. Успех финализируется как обычная
// покупка (тот же finalizePurchase, что и у ручной оплаты), неудача копит
// счётчик и после model.AutoPayMaxFails подряд выключает автопродление.
func (a *App) chargeAutoPay(ctx context.Context, ap *model.AutoPay, now time.Time) {
	lang := a.lang(ap.TelegramID)
	months := ap.Months
	if months <= 0 {
		months = model.PlanMonths[0]
	}
	value := a.autoPayPrice(months)
	if value == "" {
		a.autoPayFail(ctx, ap, now, "нет цены для периода")
		return
	}
	client := a.ykClient()
	if client == nil {
		a.autoPayFail(ctx, ap, now, "ЮKassa не настроена")
		return
	}
	currency := ap.Currency
	if currency == "" || len(currency) != 3 {
		currency = "RUB"
	}
	// Ключ идемпотентности стабилен в пределах одной попытки: повтор запроса
	// после сетевой ошибки не создаст второй платёж.
	idem := fmt.Sprintf("ap-%d-%s-%d", ap.TelegramID, now.Format("20060102"), ap.Fails)
	desc := i18n.T(lang, "yk.invoice_desc", months)
	pay, err := client.ChargeSaved(ctx, ap.MethodID, value, currency, desc, ap.TelegramID, months, idem)
	if err != nil {
		a.payLog(ctx, model.PayMethodYooKassa, "", ap.TelegramID, "autocharge_error", "months=%d: %v", months, err)
		a.autoPayFail(ctx, ap, now, err.Error())
		return
	}
	a.payLog(ctx, model.PayMethodYooKassa, pay.ID, ap.TelegramID, "autocharge", "months=%d amount=%s status=%s", months, value, pay.Status)
	if pay.Status != "succeeded" || !pay.Paid {
		if pay.Status == "pending" || pay.Status == "waiting_for_capture" {
			// Платёж ещё в процессе (например, банк требует подтверждения) —
			// финализирует вебхук, а мы просто ждём следующего тика.
			if a.store != nil {
				_ = a.store.AddPendingInvoice(ctx, &model.PendingInvoice{
					Method: model.PayMethodYooKassa, ExtID: pay.ID, TelegramID: ap.TelegramID, Months: months,
				})
			}
			_ = a.store.UpdateAutoPayResult(ctx, ap.TelegramID, ap.LastPayAt,
				now.Add(autoPayRetryDelay).Format(time.RFC3339), ap.Fails, "ожидает подтверждения")
			return
		}
		a.autoPayFail(ctx, ap, now, "платёж не прошёл: "+pay.Status)
		return
	}
	amount := pay.Amount.Value + " " + pay.Amount.Currency
	_, expireAt, err := a.finalizePurchase(ctx, ap.TelegramID, months, model.PayMethodYooKassa, amount, pay.ID)
	if err != nil {
		a.log.Warn("autopay: финализация", "tg_id", ap.TelegramID, "err", err)
		return
	}
	_ = a.store.UpdateAutoPayResult(ctx, ap.TelegramID, now.Format(time.RFC3339), "", 0, "")
	a.notifyKB(ctx, ap.TelegramID,
		i18n.T(lang, "ap.charged", monthsWord(lang, months), value+curSuffix(curRUB), formatExpire(expireAt, lang)),
		[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "ap.btn_manage"), "ap:show")}})
	a.log.Info("autopay: подписка продлена", "tg_id", ap.TelegramID, "months", months, "expire", expireAt)
}

// autoPayFail фиксирует неудачную попытку и предупреждает пользователя.
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

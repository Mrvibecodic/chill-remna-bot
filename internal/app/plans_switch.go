package app

import (
	"context"
	"math"
	"strings"
	"time"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Смена тарифа: зачёт остатка днями (решение владельца, этап 5).
//
// Продление добавляет месяцы к текущему концу срока, поэтому при покупке
// ДРУГОГО тарифа остаток дней физически не сгорает — но без пересчёта он
// молча менял стоимость: 10 оставшихся дней дешёвого тарифа превращались бы в
// 10 дней дорогого (апгрейд почти даром), а дорогого — в 10 дней дешёвого
// (потеря денег при даунгрейде). Зачёт конвертирует остаток по соотношению
// цен: стоимость неиспользованных дней старой сделки делится на цену дня
// новой, и разница (обычно отрицательная при апгрейде) сдвигает конец срока.
//
// Зачёт считается на момент ФИНАЛИЗАЦИИ — экраны показывают ту же математику,
// но по состоянию на момент показа: пока счёт ждёт оплаты, остаток тает, и
// применённая поправка может отличаться от показанной на дни ожидания.

// switchCreditCap — потолок ВЫИГРЫША зачёта в днях. Соотношение цен задаёт
// админ, и остаток дорогого тарифа при переходе на копеечный дал бы тысячи
// дней — столько бот не обещает. Потолок режет только положительную поправку:
// отрицательная ограничена самой математикой (не раньше «сейчас + месяцы»).
const switchCreditCap = 1095

// switchCredit — поправка зачёта в днях к обычному продлению: чистая
// математика над снимком старой сделки, концом срока и снимком новой сделки.
// Ноль — зачёта нет: тот же тариф, нет остатка, сделки без цен или
// несопоставимые валюты (тогда остаток переезжает днями один к одному, как и
// до этапа 5).
func switchCredit(old *model.PlanSnapshot, subExpireAt string, newSnap *model.PlanSnapshot) int {
	if old == nil || newSnap == nil || old.Code == "" || newSnap.Code == "" || old.Code == newSnap.Code {
		return 0
	}
	exp, err := time.Parse(time.RFC3339, subExpireAt)
	if err != nil {
		return 0
	}
	remaining := time.Until(exp).Hours() / 24
	if remaining <= 0 {
		return 0
	}
	// Цена старой сделки — фактически уплаченная, если её удалось разобрать
	// (Paid), иначе базовая цена снимка. Иначе покупка со скидочным
	// переопределением способа оплаты зачлась бы по полной цене — печатала бы
	// дни из скидки.
	oldPrice := old.Paid
	if oldPrice == "" {
		oldPrice = old.Price
	}
	oldK, ok := rubToKopecks(oldPrice)
	if !ok || oldK <= 0 {
		return 0
	}
	newK, ok := rubToKopecks(newSnap.Price)
	if !ok || newK <= 0 {
		return 0
	}
	// Сравнивать цены можно только в одной валюте; разные написания рублей —
	// одна валюта (см. rubCurrency).
	if !(old.Currency == newSnap.Currency || (rubCurrency(old.Currency) && rubCurrency(newSnap.Currency))) {
		return 0
	}
	oldPeriod := paidWindowDays(old)
	newPeriod := newSnap.Months * 30
	if oldPeriod <= 0 || newPeriod <= 0 {
		return 0
	}
	// Конвертируется не больше оплаченного окна: бесплатные дни (рефералки,
	// промокоды, подарки) двигают конец срока, не меняя снимок, — они
	// переезжают один к одному, а не по цене старого тарифа. Оплаченные
	// продления в окне НАКОПЛЕНЫ (BoughtDays), иначе стопка месячных продлений
	// зачитывалась бы как один месяц.
	if remaining > float64(oldPeriod) {
		remaining = float64(oldPeriod)
	}
	// Цена дня старой сделки — по цене ОДНОЙ сделки (Days или Months×30):
	// оплаченное окно может состоять из нескольких продлений по этой цене.
	oneDeal := old.Days
	if oneDeal <= 0 {
		oneDeal = old.Months * 30
	}
	if oneDeal <= 0 {
		return 0
	}
	// Стоимость остатка по цене дня старой сделки, поделённая на цену дня новой.
	converted := remaining * (float64(oldK) / float64(oneDeal)) / (float64(newK) / float64(newPeriod))
	extra := int(math.Round(converted - remaining))
	if extra > switchCreditCap {
		extra = switchCreditCap
	}
	return extra
}

// paidWindowDays — оплаченное окно снимка: накопленные купленные дни, а для
// старых снимков (и после отката, теряющего поле) — окно одной сделки.
func paidWindowDays(s *model.PlanSnapshot) int {
	if s == nil {
		return 0
	}
	if s.BoughtDays > 0 {
		return s.BoughtDays
	}
	if s.Days > 0 {
		return s.Days
	}
	return s.Months * 30
}

// boughtDaysAfter — оплаченное окно НОВОГО снимка после покупки months месяцев
// с поправкой зачёта extraDays: остаток старого оплаченного окна (продление —
// как есть, смена тарифа — конвертированный) плюс купленные месяцы.
func boughtDaysAfter(old *model.PlanSnapshot, subExpireAt string, newSnap *model.PlanSnapshot, months, extraDays int) int {
	bought := months * 30
	if old == nil || newSnap == nil {
		return bought
	}
	if exp, err := time.Parse(time.RFC3339, subExpireAt); err == nil {
		if remaining := time.Until(exp).Hours() / 24; remaining > 0 {
			carried := math.Min(remaining, float64(paidWindowDays(old)))
			if old.Code != newSnap.Code {
				// Смена тарифа: перенесённое окно — конвертированный остаток
				// (extra — это converted − carried, см. switchCredit).
				carried += float64(extraDays)
			}
			if carried > 0 {
				bought += int(math.Round(carried))
			}
		}
	}
	return bought
}

// switchCreditDays — поправка зачёта для пользователя tgID при покупке сделки
// newSnap (читает снимок последней сделки и конец срока из базы).
func (a *App) switchCreditDays(ctx context.Context, tgID int64, newSnap *model.PlanSnapshot) int {
	if newSnap == nil || newSnap.Code == "" {
		return 0
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return 0
	}
	u, err := st.GetUser(ctx, tgID)
	if err != nil || u == nil || u.Snapshot == nil {
		return 0
	}
	return switchCredit(u.Snapshot, u.SubExpireAt, newSnap)
}

// switchCreditFor — зачёт для кандидата на покупку (карточки тарифов, экран
// способов): та же математика, что применит финализация, но по снимку, снятому
// сейчас. 0 — зачёта не будет или показывать нечего.
func (a *App) switchCreditFor(ctx context.Context, tgID int64, s *sale) int {
	if s == nil {
		return 0
	}
	return a.switchCreditDays(ctx, tgID, a.saleSnapshot(s))
}

// switchingFrom — код тарифа, с которого человек уходил бы, покупая toCode:
// пусто, если активной подписки нет или тариф тот же. Гейт для плашки «остаток
// не сгорает» на карточке тарифа.
func (a *App) switchingFrom(ctx context.Context, tgID int64, toCode string) string {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return ""
	}
	u, err := st.GetUser(ctx, tgID)
	if err != nil || u == nil || u.Snapshot == nil {
		return ""
	}
	code := u.Snapshot.Code
	if code == "" || code == toCode {
		return ""
	}
	exp, err := time.Parse(time.RFC3339, u.SubExpireAt)
	if err != nil || !exp.After(time.Now()) {
		return ""
	}
	return code
}

// autoPayFollowPurchase держит снимок автосписания на ПОСЛЕДНЕЙ сделке.
// Раньше он обновлялся только платежами ЮKassa: купивший другой тариф через
// P2P/Stars/крипту молча возвращался бы автосписанием на прежний — с зачётом
// остатка в обратную сторону, которого никто не видел. Сумма и срок списания
// не трогаются: цену chargeAutoPay пересчитывает по коду тарифа из снимка, а
// отсутствие срока у нового тарифа честно откладывает списание с причиной.
func (a *App) autoPayFollowPurchase(ctx context.Context, tgID int64, applied *model.PlanSnapshot) {
	if applied == nil || applied.Code == "" {
		return
	}
	ap := a.getAutoPay(ctx, tgID)
	if ap == nil || planCodeOf(ap.Snapshot) == applied.Code {
		return
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return
	}
	// Атомарное обновление одного поля: полная перезапись строки гонялась бы
	// с параллельным «Отключить автопродление» и включала бы его обратно.
	if err := st.UpdateAutoPaySnapshot(ctx, tgID, applied); err != nil {
		a.log.Warn("autopay: снимок не обновлён после смены тарифа", "tg_id", tgID, "err", err)
		return
	}
	a.payLog(ctx, ap.Method, "", tgID, "autopay_plan", "автосписание переведено на тариф %s", applied.Code)
	// Регулярное списание меняется — молчать нельзя (та же логика, что при
	// смене срока в saveAutoPayFromPayment).
	if ap.Enabled {
		name := applied.Name
		if name == "" {
			name = applied.Code
		}
		a.notify(ctx, tgID, i18n.T(a.lang(tgID), "ap.plan_changed", escapeName(name)))
	}
}

// paidRub — сумма платежа в валюте сетки: «990 ₽» → «990». Пусто — сумма не в
// рублях (звёзды, чужая валюта) или не разобрана. Кладётся в снимок сделки как
// Paid: по ней потом считается зачёт остатка при смене тарифа.
func paidRub(amount string) string {
	f := strings.Fields(strings.TrimSpace(amount))
	if len(f) == 0 {
		return ""
	}
	if len(f) > 1 && !rubCurrency(strings.Join(f[1:], " ")) {
		return ""
	}
	if k, ok := rubToKopecks(f[0]); ok && k > 0 {
		return f[0]
	}
	return ""
}

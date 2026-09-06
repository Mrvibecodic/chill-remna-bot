package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"remnabot/internal/model"
)

// Математика зачёта: остаток дешёвого тарифа конвертируется в дни дорогого по
// соотношению цен (и наоборот), несопоставимые случаи дают ноль.
func TestSwitchCreditDays_Math(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	const uid int64 = 850
	_ = fs.UpsertUser(ctx, uid)

	in15 := time.Now().UTC().Add(15 * 24 * time.Hour).Format(time.RFC3339)
	set := func(snap *model.PlanSnapshot, expire string) {
		t.Helper()
		_ = fs.SetUserSnapshot(ctx, uid, snap)
		_ = fs.SetSubExpiry(ctx, uid, expire, "sub")
	}

	// Апгрейд: 15 дней по 300₽/30д = 150₽ остатка; день нового (900₽/30д) =
	// 30₽ → 5 дней. Поправка ≈ −10.
	set(&model.PlanSnapshot{Code: "base", Months: 1, Price: "300"}, in15)
	newSnap := &model.PlanSnapshot{Code: "vipvipvipvip", Months: 1, Price: "900"}
	if got := a.switchCreditDays(ctx, uid, newSnap); got != -10 {
		t.Fatalf("апгрейд: ожидалась поправка -10, получено %d", got)
	}

	// Даунгрейд: та же пара наоборот → 15 дней по 900₽ = 450₽ → 45 дней по
	// 300₽ → поправка +30.
	set(&model.PlanSnapshot{Code: "vipvipvipvip", Months: 1, Price: "900"}, in15)
	if got := a.switchCreditDays(ctx, uid, &model.PlanSnapshot{Code: "base", Months: 1, Price: "300"}); got != 30 {
		t.Fatalf("даунгрейд: ожидалась поправка +30, получено %d", got)
	}

	// Тот же тариф — обычное продление, поправки нет.
	set(&model.PlanSnapshot{Code: "base", Months: 1, Price: "300"}, in15)
	if got := a.switchCreditDays(ctx, uid, &model.PlanSnapshot{Code: "base", Months: 1, Price: "300"}); got != 0 {
		t.Fatalf("продление того же тарифа: %d", got)
	}

	// Истёкшая подписка — зачитывать нечего.
	set(&model.PlanSnapshot{Code: "base", Months: 1, Price: "300"}, "2001-01-01T00:00:00Z")
	if got := a.switchCreditDays(ctx, uid, newSnap); got != 0 {
		t.Fatalf("истёкшая подписка: %d", got)
	}

	// Чужая валюта старой сделки — цены несопоставимы.
	set(&model.PlanSnapshot{Code: "base", Months: 1, Price: "300", Currency: "$"}, in15)
	if got := a.switchCreditDays(ctx, uid, newSnap); got != 0 {
		t.Fatalf("чужая валюта: %d", got)
	}

	// Импортированная сделка с днями: 15 дней по 300₽/45д — цена дня ниже.
	set(&model.PlanSnapshot{Code: "base", Months: 1, Days: 45, Price: "300"}, in15)
	// Остаток 15 × (300/45) = 100₽ → 100/30 ≈ 3.33 дня нового → поправка −12.
	if got := a.switchCreditDays(ctx, uid, newSnap); got != -12 {
		t.Fatalf("сделка в днях: ожидалась поправка -12, получено %d", got)
	}

	// Бонусные дни сверх периода сделки не конвертируются: при месячной сделке
	// и 60 днях до конца считаются только 30 — рефералки и промо-дни не
	// зачитываются по цене тарифа. Конверт 30×(300/30)/(900/30)=10 → −20.
	in60 := time.Now().UTC().Add(60 * 24 * time.Hour).Format(time.RFC3339)
	set(&model.PlanSnapshot{Code: "base", Months: 1, Price: "300"}, in60)
	if got := a.switchCreditDays(ctx, uid, newSnap); got != -20 {
		t.Fatalf("бонусные дни: ожидалась поправка -20, получено %d", got)
	}

	// Скидочная покупка: считаем по фактически уплаченному (Paid), а не по
	// полной цене. 15×(500/30)=250₽ → /30₽ ≈ 8.33 → поправка −7.
	set(&model.PlanSnapshot{Code: "base", Months: 1, Price: "1000", Paid: "500"}, in15)
	if got := a.switchCreditDays(ctx, uid, newSnap); got != -7 {
		t.Fatalf("скидочная сделка: ожидалась поправка -7, получено %d", got)
	}

	// Потолок выигрыша: безумное соотношение цен не обещает тысячи дней.
	set(&model.PlanSnapshot{Code: "base", Months: 1, Price: "99000"}, in15)
	if got := a.switchCreditDays(ctx, uid, newSnap); got != switchCreditCap {
		t.Fatalf("потолок: ожидалось %d, получено %d", switchCreditCap, got)
	}

	// Стопка оплаченных продлений: окно НАКОПЛЕНО (BoughtDays), а не равно
	// одной сделке — 12 месячных продлений по 100₽ при переходе на 1000₽/мес
	// конвертируются целиком: 360×(100/30)/(1000/30)=36 → поправка −324.
	in360 := time.Now().UTC().Add(360 * 24 * time.Hour).Format(time.RFC3339)
	set(&model.PlanSnapshot{Code: "base", Months: 1, Price: "100", BoughtDays: 360}, in360)
	if got := a.switchCreditDays(ctx, uid, &model.PlanSnapshot{Code: "vipvipvipvip", Months: 1, Price: "1000"}); got != -324 {
		t.Fatalf("стопка продлений: ожидалась поправка -324, получено %d", got)
	}
}

// Оплаченное окно копится продлениями и конвертируется при смене тарифа;
// бонусные дни в него не попадают.
func TestBoughtDaysAfter(t *testing.T) {
	in30 := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	base := &model.PlanSnapshot{Code: "base", Months: 1, Price: "100"}
	vip := &model.PlanSnapshot{Code: "vip", Months: 1, Price: "1000"}

	// Продление того же тарифа: остаток окна + купленный месяц.
	if got := boughtDaysAfter(base, in30, base, 1, 0); got != 60 {
		t.Fatalf("продление: ожидалось 60, получено %d", got)
	}
	// Первая покупка без прежней подписки.
	if got := boughtDaysAfter(nil, "", base, 3, 0); got != 90 {
		t.Fatalf("первая покупка: ожидалось 90, получено %d", got)
	}
	// Смена тарифа: перенесённое окно — конвертированный остаток (30−27=3).
	if got := boughtDaysAfter(base, in30, vip, 1, -27); got != 33 {
		t.Fatalf("смена: ожидалось 33, получено %d", got)
	}
	// Бонусные дни сверх окна не копятся: остаток 30, окно одной сделки 30 —
	// даже если конец срока дальше за счёт рефералок.
	in90 := time.Now().UTC().Add(90 * 24 * time.Hour).Format(time.RFC3339)
	if got := boughtDaysAfter(base, in90, base, 1, 0); got != 60 {
		t.Fatalf("бонусные дни попали в окно: %d", got)
	}
}

// Сумма платежа разбирается в Paid только в валюте сетки.
func TestPaidRub(t *testing.T) {
	for in, want := range map[string]string{
		"990 ₽": "990", "150.50 руб": "150.50", "99": "99",
		"99 ⭐": "", "5 $": "", "": "", "abc ₽": "",
	} {
		if got := paidRub(in); got != want {
			t.Fatalf("paidRub(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

// Финализация применяет зачёт: конец срока сдвигается относительно обычного
// продления, а экран способов показывает те же цифры до оплаты.
func TestSwitchCredit_AppliedAndShown(t *testing.T) {
	var patched map[string]any
	// Пользователь живёт на «Базовом» (150₽/мес по сетке), осталось 15 дней —
	// и панель, и зеркало держат один и тот же конец срока.
	in15 := time.Now().UTC().Add(15 * 24 * time.Hour).Format(time.RFC3339)
	srv := snapPanelExpiry(t, &patched, in15)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const uid int64 = 555
	_ = fs.UpsertUser(ctx, uid)
	_ = fs.AddBalance(ctx, uid, 99000)
	p := vipPlan(t, fs, model.PlanAvailAll)

	_ = fs.SetUserSnapshot(ctx, uid, &model.PlanSnapshot{Code: model.PlanCodeBase, Months: 1, Price: "150"})
	_ = fs.SetSubExpiry(ctx, uid, in15, "sub")

	// Экран способов оплаты показывает сдвиг.
	fm := &fakeMsg{}
	a.msg = fm
	d := p.Duration(1)
	a.showMethodsSale(ctx, uid, &sale{Plan: p, D: d, Months: 1})
	if !strings.Contains(fm.joined(), "Зачёт остатка") {
		t.Fatalf("экран способов не показывает зачёт:\n%s", fm.joined())
	}

	// Покупка применяет поправку: 15 дней по 5₽/д = 75₽ → 75/33 = 2.27 дня
	// нового (990₽/30д) → поправка −13. Ожидание: конец срока плюс месяц
	// минус 13 дней.
	dto := a.MiniCheckout(ctx, uid, p.Code, 1, model.PayMethodBalance, false)
	if !dto.OK {
		t.Fatalf("покупка не прошла: %+v", dto)
	}
	got, _ := patched["expireAt"].(string)
	exp, err := time.Parse(time.RFC3339, in15)
	if err != nil {
		t.Fatal(err)
	}
	want := exp.AddDate(0, 1, 0).AddDate(0, 0, -13).Format(time.RFC3339)
	if got != want {
		t.Fatalf("конец срока: получено %s, ожидалось %s", got, want)
	}

	// Снимок сохранённой сделки обязан нести стоимость окна: без неё
	// следующая смена тарифа снова считает цену дня по одной сделке и печатает
	// дни. Юнит-тесты на формулу этого не ловят — проводку проверяем здесь.
	u, err := fs.GetUser(ctx, uid)
	if err != nil || u == nil || u.Snapshot == nil {
		t.Fatalf("снимок не сохранён: %v %+v", err, u)
	}
	// 15 дней старого окна (150₽/30д = 75₽) плюс сделка 990₽.
	if want := int64(7500 + 99000); u.Snapshot.WindowPaidK != want {
		t.Fatalf("стоимость окна в снимке: получено %d, ожидалось %d", u.Snapshot.WindowPaidK, want)
	}
	if u.Snapshot.BoughtDays <= 30 {
		t.Fatalf("оплаченное окно в снимке не накопилось: %d", u.Snapshot.BoughtDays)
	}
}

// Зачёт считает цену дня по стоимости ВСЕГО окна, а не по цене последней
// сделки: иначе год, купленный по годовой цене, после докупки одного месяца
// пересчитывался бы по месячной — а месяц в пересчёте на день дороже года, и
// разница выдавалась бы бесплатными днями.
func TestSwitchCredit_WindowValueNotLastDeal(t *testing.T) {
	in390 := time.Now().UTC().Add(390 * 24 * time.Hour).Format(time.RFC3339)
	// Окно: год за 1500₽ (360 дней) плюс месяц за 150₽ (30 дней) = 1650₽/390д.
	old := &model.PlanSnapshot{
		Code: model.PlanCodeBase, Months: 1, Price: "150",
		BoughtDays: 390, WindowPaidK: 165000,
	}
	vip := &model.PlanSnapshot{Code: "vip", Months: 1, Price: "990"}

	// 390 д × (165000/390) = 165000 коп. → /3300 коп. за день нового = 50 дней.
	if got := switchCredit(old, in390, vip); got != -340 {
		t.Fatalf("зачёт по стоимости окна: получено %d, ожидалось -340", got)
	}
	// Тот же снимок без накопленной стоимости (старый снимок, откат версии)
	// считается по цене одной сделки — это и есть та самая печать дней, ради
	// совместимости оставленная только там, где стоимости окна попросту нет.
	legacy := *old
	legacy.WindowPaidK = 0
	if got := switchCredit(&legacy, in390, vip); got <= -340 {
		t.Fatalf("запасной путь должен быть щедрее: получено %d", got)
	}
}

// Стоимость окна копится: перенесённая доля старого окна плюс уплаченное за
// эту покупку. Бесплатные дни сверх оплаченного окна стоимости не добавляют.
func TestWindowPaidAfter(t *testing.T) {
	in30 := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	base := &model.PlanSnapshot{Code: model.PlanCodeBase, Months: 1, Price: "150"}

	// Первая покупка: только цена сделки.
	if got := windowPaidAfter(nil, "", base); got != 15000 {
		t.Fatalf("первая покупка: получено %d, ожидалось 15000", got)
	}
	// Продление: перенесено всё старое окно (30 из 30) плюс новая сделка.
	if got := windowPaidAfter(base, in30, base); got != 30000 {
		t.Fatalf("продление: получено %d, ожидалось 30000", got)
	}
	// Бонусные дни сверх окна стоимости не приносят: остаток 90, окно 30.
	in90 := time.Now().UTC().Add(90 * 24 * time.Hour).Format(time.RFC3339)
	if got := windowPaidAfter(base, in90, base); got != 30000 {
		t.Fatalf("бонусные дни добавили стоимость: %d", got)
	}
	// Истёкшая подписка: переносить нечего.
	past := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	if got := windowPaidAfter(base, past, base); got != 15000 {
		t.Fatalf("истёкшее окно: получено %d, ожидалось 15000", got)
	}
}

// Цикл накрутки из перепроверки: год → месяц того же тарифа → год другого
// тарифа. До правки он печатал 1151 день за 13 000 ₽ и повторялся бесконечно.
// Здесь фиксируется, что выданное совпадает с оплаченным.
func TestSwitchCredit_NoDayPrintingCycle(t *testing.T) {
	now := time.Now().UTC()
	// Шаг 1: год за 6000 на тарифе A.
	sA1 := &model.PlanSnapshot{Code: "a", Months: 12, Price: "6000", Currency: "₽"}
	sA1.BoughtDays = boughtDaysAfter(nil, "", sA1, 12, 0)
	sA1.WindowPaidK = windowPaidAfter(nil, "", sA1)
	exp := now.AddDate(0, 12, 0)
	t.Logf("шаг1: дни=%d стоимость=%d конец=%s", sA1.BoughtDays, sA1.WindowPaidK, exp.Format("2006-01-02"))

	// Шаг 2: месяц за 1000 на том же тарифе A.
	sA2 := &model.PlanSnapshot{Code: "a", Months: 1, Price: "1000", Currency: "₽"}
	cr := switchCredit(sA1, exp.Format(time.RFC3339), sA2)
	sA2.BoughtDays = boughtDaysAfter(sA1, exp.Format(time.RFC3339), sA2, 1, cr)
	sA2.WindowPaidK = windowPaidAfter(sA1, exp.Format(time.RFC3339), sA2)
	exp = exp.AddDate(0, 1, 0).AddDate(0, 0, cr)
	t.Logf("шаг2: зачёт=%+d дни=%d стоимость=%d конец=%s", cr, sA2.BoughtDays, sA2.WindowPaidK, exp.Format("2006-01-02"))

	// Шаг 3: год за 6000 на тарифе B.
	sB := &model.PlanSnapshot{Code: "b", Months: 12, Price: "6000", Currency: "₽"}
	cr = switchCredit(sA2, exp.Format(time.RFC3339), sB)
	sB.BoughtDays = boughtDaysAfter(sA2, exp.Format(time.RFC3339), sB, 12, cr)
	sB.WindowPaidK = windowPaidAfter(sA2, exp.Format(time.RFC3339), sB)
	exp = exp.AddDate(0, 12, 0).AddDate(0, 0, cr)
	total := int(time.Until(exp).Hours() / 24)
	t.Logf("шаг3: зачёт=%+d дни=%d стоимость=%d конец=%s ВСЕГО ДНЕЙ=%d за 13000₽", cr, sB.BoughtDays, sB.WindowPaidK, exp.Format("2006-01-02"), total)

	// Честно: 13000₽ по 6000/год = 2.17 года ≈ 790 дней.
	if total > 830 {
		t.Fatalf("напечатано дней: %d при честных ~790", total)
	}
}

// Стоимость окна НЕ входит в отпечаток условий. Она меняется при каждой
// покупке, а по расхождению отпечатка рассылается «условия изменились» —
// без этого исключения одно продление разослало бы уведомление всем.
func TestPlanSnapshot_WindowPaidNotInFingerprint(t *testing.T) {
	a := &model.PlanSnapshot{Code: model.PlanCodeBase, Months: 1, Price: "150"}
	b := *a
	b.WindowPaidK = 987654
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("стоимость окна попала в отпечаток: %q против %q", a.Fingerprint(), b.Fingerprint())
	}
}

// Деньги разных валют не складываются. Окно рублёвого тарифа, перенесённое в
// долларовый, иначе превращало бы рубли в центы — и следующая смена внутри
// долларов печатала бы дни.
func TestWindowPaidAfter_NoCrossCurrencyCarry(t *testing.T) {
	in300 := time.Now().UTC().Add(300 * 24 * time.Hour).Format(time.RFC3339)
	rub := &model.PlanSnapshot{Code: "a", Months: 12, Price: "6000", Currency: "₽", BoughtDays: 360, WindowPaidK: 600000}
	usd := &model.PlanSnapshot{Code: "d", Months: 1, Price: "50", Currency: "$"}

	// Перенос запрещён: в окне только цена самой сделки.
	own, _ := dealValueK(usd)
	if got := windowPaidAfter(rub, in300, usd); got != own {
		t.Fatalf("рубли перетекли в долларовое окно: получено %d, ожидалось %d", got, own)
	}
	// Внутри одной валюты перенос работает как обычно.
	usd2 := &model.PlanSnapshot{Code: "d2", Months: 1, Price: "50", Currency: "$"}
	usdOld := &model.PlanSnapshot{Code: "d", Months: 1, Price: "50", Currency: "$", BoughtDays: 30, WindowPaidK: 5000}
	in30 := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	if got := windowPaidAfter(usdOld, in30, usd2); got != 10000 {
		t.Fatalf("перенос внутри валюты: получено %d, ожидалось 10000", got)
	}
}

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
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const uid int64 = 555
	_ = fs.UpsertUser(ctx, uid)
	_ = fs.AddBalance(ctx, uid, 99000)
	p := vipPlan(t, fs, model.PlanAvailAll)

	// Пользователь живёт на «Базовом» (150₽/мес по сетке), осталось 15 дней.
	in15 := time.Now().UTC().Add(15 * 24 * time.Hour).Format(time.RFC3339)
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
	// нового (990₽/30д) → поправка −13. Панель в стабе держит конец
	// 2030-01-01: ожидание = 2030-02-01 (месяц) минус 13 дней.
	dto := a.MiniCheckout(ctx, uid, p.Code, 1, model.PayMethodBalance, false)
	if !dto.OK {
		t.Fatalf("покупка не прошла: %+v", dto)
	}
	got, _ := patched["expireAt"].(string)
	want := time.Date(2030, 2, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -13).Format(time.RFC3339)
	if got != want {
		t.Fatalf("конец срока: получено %s, ожидалось %s", got, want)
	}
}

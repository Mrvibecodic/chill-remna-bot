package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/web"
)

// Витрина мини-аппа v2: тарифы со своими сроками, «выгодный» считает сервер.

func TestMiniPlans_PlansWithDurationsAndBest(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	vip := vipPlan(t, fs, model.PlanAvailAll)

	dto := a.MiniPlans(ctx, 555)
	// «Базовый» синтезируется из сетки, VIP — строка тарифа.
	if len(dto.Plans) != 2 {
		t.Fatalf("ожидались 2 тарифа, получено %d: %+v", len(dto.Plans), dto.Plans)
	}
	var got *web.MiniPlanDTO
	for i := range dto.Plans {
		if dto.Plans[i].Code == vip.Code {
			got = &dto.Plans[i]
		}
	}
	if got == nil {
		t.Fatalf("VIP не попал в витрину: %+v", dto.Plans)
	}
	if len(got.Durations) != 2 {
		t.Fatalf("у VIP должно быть 2 срока: %+v", got.Durations)
	}
	// «Выгодный» — минимальная цена за месяц (9900/12 < 990/1), решает сервер,
	// а не жёсткий индекс на фронте.
	for _, d := range got.Durations {
		if d.Months == 12 && !d.Best {
			t.Fatalf("год должен быть отмечен выгодным: %+v", got.Durations)
		}
		if d.Months == 1 && d.Best {
			t.Fatalf("месяц не должен быть отмечен выгодным: %+v", got.Durations)
		}
	}
}

// Продажа по коду тарифа: баланс списывается по цене тарифа, применяется
// снимок его условий.
func TestMiniCheckout_SellsPlanByCode(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	_ = fs.AddBalance(ctx, u, 99000)
	p := vipPlan(t, fs, model.PlanAvailAll)

	dto := a.MiniCheckout(ctx, u, p.Code, 1, model.PayMethodBalance, false)
	if !dto.OK {
		t.Fatalf("покупка тарифа с баланса не прошла: %+v", dto)
	}
	if b := a.userBalance(ctx, u); b != 0 {
		t.Fatalf("списана не цена тарифа (990): остаток %d", b)
	}
	u2, _ := fs.GetUser(ctx, u)
	if u2 == nil || u2.Snapshot == nil || u2.Snapshot.Code != p.Code {
		t.Fatalf("снимок сделки не применён: %+v", u2)
	}
	// Панель получила лимит устройств ТАРИФА, а не базовой сетки.
	if v, ok := patched["hwidDeviceLimit"].(float64); !ok || int(v) != 7 {
		t.Fatalf("панели ушли не условия тарифа: %+v", patched)
	}
}

// Пустой код тарифа — старый фронт из кэша: продаётся «Базовый», как раньше.
func TestMiniCheckout_EmptyPlanIsBase(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	_ = fs.AddBalance(ctx, u, 15000)

	dto := a.MiniCheckout(ctx, u, "", 1, model.PayMethodBalance, false)
	if !dto.OK {
		t.Fatalf("покупка «Базового» не прошла: %+v", dto)
	}
	u2, _ := fs.GetUser(ctx, u)
	if u2 == nil || u2.Snapshot == nil || u2.Snapshot.Code != model.PlanCodeBase {
		t.Fatalf("снимок «Базового» не применён: %+v", u2)
	}
}

// Тариф «по ссылке» и неизвестный код не продаются через API мини-аппа:
// иначе API становился бы обходом скрытности витрины.
func TestMiniCheckout_HiddenAndUnknownPlansRefused(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	p := vipPlan(t, fs, model.PlanAvailLink)

	for _, pd := range a.MiniPlans(ctx, 555).Plans {
		if pd.Code == p.Code {
			t.Fatalf("тариф «по ссылке» виден в витрине мини-аппа: %+v", pd)
		}
	}
	if dto := a.MiniCheckout(ctx, 555, p.Code, 1, model.PayMethodBalance, false); dto.Error == "" {
		t.Fatalf("тариф «по ссылке» продан через мини-апп: %+v", dto)
	}
	if dto := a.MiniCheckout(ctx, 555, "nosuchplan", 1, model.PayMethodBalance, false); dto.Error == "" {
		t.Fatalf("неизвестный код продан: %+v", dto)
	}
	// Выключенный тариф тоже не продаётся.
	p.Availability = model.PlanAvailAll
	p.Enabled = false
	_ = fs.SavePlan(ctx, p)
	if dto := a.MiniCheckout(ctx, 555, p.Code, 1, model.PayMethodBalance, false); dto.Error == "" {
		t.Fatalf("выключенный тариф продан: %+v", dto)
	}
}

// Счёт Stars из мини-аппа на тариф: сумма — звёздная цена ТАРИФА, а не сетки.
func TestMiniCheckout_StarsUsesPlanPrice(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	a.botCfg.Stars = model.StarsConfig{Enabled: true, Prices: map[int]int{1: 99}}
	p := vipPlan(t, fs, model.PlanAvailAll) // 1 мес = 55⭐

	dto := a.MiniCheckout(ctx, 555, p.Code, 1, model.PayMethodStars, false)
	if !dto.OK || !dto.Invoice {
		t.Fatalf("счёт Stars не создан: %+v", dto)
	}
	if !strings.Contains(dto.PayURL, "_55_stars:1") {
		t.Fatalf("в счёте не звёздная цена тарифа: %q", dto.PayURL)
	}
	// Условия счёта сняты с тарифа.
	snap, _, _ := fs.InvoiceSnapshot(ctx, 555, model.PayMethodStars, 1)
	if snap == nil || snap.Code != p.Code {
		t.Fatalf("условия счёта не с тарифа: %+v", snap)
	}
}

// Выключенный «Базовый» с продажи снят целиком: витрина пустеет, а старые
// кнопки и мини-апп не продают в обход (ветка появилась вместе с флагом).
func TestBasePlanDisabled_NotSold(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	base, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	base.Enabled = false
	_ = fs.SavePlan(ctx, base)

	const uid int64 = 730
	// Мини-апп: витрина и checkout.
	if dto := a.MiniPlans(ctx, uid); len(dto.Plans) != 0 {
		t.Fatalf("выключенный «Базовый» виден в мини-аппе: %+v", dto.Plans)
	}
	if dto := a.MiniCheckout(ctx, uid, "", 1, model.PayMethodBalance, false); dto.Error == "" {
		t.Fatalf("мини-апп продал выключенный «Базовый»: %+v", dto)
	}
	// Чат: старая кнопка срока из переписки не должна открывать способы оплаты.
	a.handleCallback(ctx, cb(uid, "buy:1"))
	a.handleCallback(ctx, cb(uid, "method:bal"))
	if s, _ := a.saleFor(ctx, uid); s != nil {
		t.Fatalf("saleFor продаёт выключенный «Базовый»: %+v", s)
	}
	_ = fm
}

// Тариф в чужой валюте не продаётся способами, считающими в валюте сетки:
// «5 $» не должны молча списаться как «5 ₽».
func TestPlanForeignCurrency_GridMethodsRefuse(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)
	p.Currency = "$"
	_ = fs.SavePlan(ctx, p)

	const uid int64 = 731
	_ = fs.UpsertUser(ctx, uid)
	_ = fs.AddBalance(ctx, uid, 990000)

	if dto := a.MiniCheckout(ctx, uid, p.Code, 1, model.PayMethodBalance, false); dto.Error == "" {
		t.Fatalf("баланс списал тариф в чужой валюте: %+v", dto)
	}
	if b := a.userBalance(ctx, uid); b != 990000 {
		t.Fatalf("баланс тронут: %d", b)
	}
	a.botCfg.CryptoBot.Enabled = true
	a.botCfg.CryptoBot.Token = "t"
	if _, _, err := a.miniPayURL(ctx, uid, &sale{Plan: p, D: &p.Durations[0], Months: 1}, model.PayMethodCryptoBot, false); err == nil {
		t.Fatal("CryptoBot выставил счёт в чужой валюте")
	}
	// ЮKassa валюту тарифа передаёт явно — её гейт не трогаем.
	if !a.saleGridCurrency(baseSale(1)) {
		t.Fatal("«Базовый» всегда в валюте сетки")
	}
}

// Гейт триала: пока триал идёт, витрина и чекаут закрыты в мини-аппе и
// кабинете так же, как в чате — если админ не разрешил покупку.
func TestMiniPlans_TrialLock(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	_ = fs.SetSubExpiry(ctx, u, time.Now().UTC().Add(72*time.Hour).Format(time.RFC3339), "trial")

	dto := a.MiniPlans(ctx, u)
	if len(dto.Plans) != 0 || dto.Notice == "" {
		t.Fatalf("во время триала витрина закрыта: %+v", dto)
	}
	if res := a.MiniCheckout(ctx, u, p.Code, 1, model.PayMethodBalance, false); res.Error == "" {
		t.Fatal("чекаут во время триала должен отказывать")
	}

	a.mu.Lock()
	a.botCfg.Trial.AllowBuy = true
	a.mu.Unlock()
	if dto := a.MiniPlans(ctx, u); len(dto.Plans) == 0 || dto.Notice != "" {
		t.Fatalf("админ разрешил покупку — витрина открыта: %+v", dto)
	}
}

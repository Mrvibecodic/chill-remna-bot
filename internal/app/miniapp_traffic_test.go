package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/yookassa"
)

func TestMiniTraffic_Storefront(t *testing.T) {
	a, fs, _, plan := trafficDealApp(t)
	ctx := context.Background()
	_ = fs.SavePlan(ctx, &model.Plan{Code: "term01", Name: "Стандарт", Enabled: true, Currency: "₽", Availability: model.PlanAvailAll,
		Durations: []model.PlanDuration{{Months: 1, Base: "199"}}})

	dto := a.MiniPlans(ctx, trafficUID)
	for _, p := range dto.Plans {
		if p.Code == plan.Code {
			t.Fatal("трафик-тариф в общем списке: прошлая страница нарисовала бы его без сроков")
		}
	}
	if len(dto.Traffic) != 1 {
		t.Fatalf("трафик-тарифов в витрине: %d", len(dto.Traffic))
	}
	tp := dto.Traffic[0]
	if tp.Kind != model.PlanKindTraffic || tp.Strategy != "NO_RESET" || len(tp.Durations) != 1 {
		t.Fatalf("трафик-тариф: %+v", tp)
	}
	d := tp.Durations[0]
	if d.Months != 0 || d.Price != "450" || d.Stars != 300 || d.TrafficGB != 150 || d.Devices != 3 {
		t.Fatalf("условия трафик-тарифа: %+v", d)
	}
	if tp.TrafficNote == "" || tp.ReplaceNote != "" {
		t.Fatalf("строки карточки без подписки: %q %q", tp.TrafficNote, tp.ReplaceNote)
	}

	if r := a.MiniCheckout(ctx, trafficUID, plan.Code, 1, "", model.PayMethodBalance, "450", false, false); r.OK {
		t.Fatal("трафик-тариф продан как срок в месяц")
	}
	if r := a.MiniCheckout(ctx, trafficUID, plan.Code, 0, "pk5000", model.PayMethodBalance, "160", false, false); r.OK {
		t.Fatal("пакет продан не владельцу")
	}
	if r := a.MiniCheckout(ctx, trafficUID, "term01", 1, "pk5000", model.PayMethodBalance, "199", false, false); r.OK {
		t.Fatal("пакет к обычному тарифу")
	}
	if r := a.MiniCheckout(ctx, trafficUID, plan.Code, 0, "", model.PayMethodBalance, "400", false, false); r.OK || !strings.Contains(r.Error, "цен") {
		t.Fatalf("сверка показанной цены: %+v", r)
	}
	if got := a.MiniTraffic(ctx, trafficUID); got.Owner || got.Notice == "" {
		t.Fatalf("пакеты не владельцу: %+v", got)
	}
}

func TestMiniTraffic_BalanceBuyAndPacks(t *testing.T) {
	a, _, panel, plan := trafficDealApp(t)
	ctx := context.Background()

	r := a.MiniCheckout(ctx, trafficUID, plan.Code, 0, "", model.PayMethodBalance, "450", false, false)
	if !r.OK || !r.Done || r.ExpireAt != "бессрочно" {
		t.Fatalf("покупка трафик-тарифа: %+v", r)
	}
	if exp, limit, _, _ := panel.state(); exp != remnawave.EternalExpire || limit != 150*gib {
		t.Fatalf("панель после покупки: %s %d", exp, limit)
	}

	menu := a.MiniMenu(ctx, trafficUID, false)
	if !menu.TrafficOwner || menu.CanRenew {
		t.Fatalf("меню владельца: %+v", menu)
	}
	sub := a.MiniSubscription(ctx, trafficUID)
	if !sub.Eternal || sub.ExpireTS != 0 || sub.ExpireAt != "бессрочно" {
		t.Fatalf("подписка: eternal=%v ts=%d at=%q", sub.Eternal, sub.ExpireTS, sub.ExpireAt)
	}
	if again := a.MiniPlans(ctx, trafficUID); len(again.Traffic) != 1 || again.Traffic[0].ReplaceNote == "" || !again.Traffic[0].Own {
		t.Fatalf("витрина владельцу: %+v", again.Traffic)
	}

	panel.setUsed(100 * gib)
	tr := a.MiniTraffic(ctx, trafficUID)
	if !tr.Owner || !tr.LimitOK || tr.Used != 100*gib || tr.Limit != 150*gib || len(tr.Packs) != 1 || tr.Packs[0].Price != "160" {
		t.Fatalf("экран пакетов: %+v", tr)
	}

	for i, want := range []int64{200 * gib, 250 * gib} {
		a.thrMu.Lock()
		a.recentBuy = nil // второй пакет — осознанная покупка, а не двойное нажатие
		a.thrMu.Unlock()
		r := a.MiniCheckout(ctx, trafficUID, plan.Code, 0, "pk5000", model.PayMethodBalance, "160", false, false)
		if !r.OK || !r.Done || !strings.Contains(r.Message, "50 ГБ") {
			t.Fatalf("пакет %d: %+v", i+1, r)
		}
		if _, limit, used, _ := panel.state(); limit != want || used != 100*gib {
			t.Fatalf("пакет %d: лимит %d расход %d", i+1, limit, used)
		}
	}
	if r := a.MiniCheckout(ctx, trafficUID, plan.Code, 0, "pk5000", model.PayMethodBalance, "160", false, false); r.OK {
		t.Fatal("двойное нажатие купило пакет ещё раз")
	}
	if _, limit, _, _ := panel.state(); limit != 250*gib {
		t.Fatalf("двойное нажатие изменило лимит: %d", limit)
	}
}

func TestMiniTraffic_YooKassaInvoice(t *testing.T) {
	a, fs, _, plan := trafficDealApp(t)
	ctx := context.Background()
	var mu sync.Mutex
	var bodies []map[string]any
	yk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"yk-mini","status":"pending","confirmation":{"type":"redirect","confirmation_url":"https://pay.example.com/yk-mini"}}`))
	}))
	defer yk.Close()
	oldBase := yookassa.BaseURL
	yookassa.BaseURL = yk.URL
	defer func() { yookassa.BaseURL = oldBase }()
	a.mu.Lock()
	a.botCfg.YooKassa = model.YooKassaConfig{Enabled: true, ShopID: "shop", SecretKey: "sec", ReturnURL: "https://t.me", AutoPay: true}
	a.mu.Unlock()

	r := a.MiniCheckout(ctx, trafficUID, plan.Code, 0, "", model.PayMethodYooKassa, "450", false, false)
	if !r.OK || r.PayURL != "https://pay.example.com/yk-mini" {
		t.Fatalf("счёт ЮKassa: %+v", r)
	}
	inv, _ := fs.TrafficInvoiceByExtID(ctx, "yk-mini")
	if inv == nil || inv.Purpose != purposeTraffic {
		t.Fatalf("строка счёта трафика: %+v", inv)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("запросов к ЮKassa: %d", len(bodies))
	}
	if v, ok := bodies[0]["save_payment_method"]; ok && v == true {
		t.Fatal("карта сохраняется для автопродления бессрочного тарифа")
	}
	meta, _ := bodies[0]["metadata"].(map[string]any)
	if meta["months"] != "0" {
		t.Fatalf("срок в метаданных: %v", meta["months"])
	}
	if r := a.MiniCheckout(ctx, trafficUID, plan.Code, 0, "", model.PayMethodTribute, "", false, false); r.OK {
		t.Fatal("трафик-тариф через Tribute")
	}
}

func TestMiniTraffic_PlanLink(t *testing.T) {
	a, fs, _, plan := trafficDealApp(t)
	ctx := context.Background()
	plan.Availability = model.PlanAvailLink
	_ = fs.SavePlan(ctx, plan)
	if dto := a.MiniPlans(ctx, trafficUID); len(dto.Traffic) != 0 {
		t.Fatal("тариф «по ссылке» в витрине")
	}
	if r := a.MiniCheckout(ctx, trafficUID, plan.Code, 0, "", model.PayMethodBalance, "450", false, false); r.OK {
		t.Fatal("тариф «по ссылке» продан без ссылки")
	}
	dto := a.MiniPlanLink(ctx, trafficUID, plan.Code)
	if len(dto.Traffic) != 1 || len(dto.Plans) != 0 {
		t.Fatalf("ссылка трафик-тарифа: %+v", dto)
	}
	if r := a.MiniCheckout(ctx, trafficUID, plan.Code, 0, "", model.PayMethodBalance, "450", false, false); !r.OK {
		t.Fatalf("покупка по ссылке: %+v", r)
	}
}

func TestMiniTraffic_NoAutoPay(t *testing.T) {
	a, fs, _, plan := trafficDealApp(t)
	ctx := context.Background()
	a.mu.Lock()
	a.botCfg.YooKassa = model.YooKassaConfig{Enabled: true, ShopID: "shop", SecretKey: "sec", AutoPay: true}
	a.mu.Unlock()
	_ = fs.SetAutoPay(ctx, &model.AutoPay{TelegramID: trafficUID, Method: model.PayMethodYooKassa, MethodID: "pm-1", Title: "Card *4417", Months: 1})
	if !a.MiniAutoPay(ctx, trafficUID).Available {
		t.Fatal("до трафик-тарифа автопродление должно предлагаться")
	}
	buyTrafficWithBalance(t, a, plan.Code, "")
	if ap := a.MiniAutoPay(ctx, trafficUID); ap.Available || ap.CanEnable || ap.On {
		t.Fatalf("автопродление у трафик-тарифа: %+v", ap)
	}
	if r := a.MiniSetAutoPay(ctx, trafficUID, true); r.OK {
		t.Fatal("автопродление включено на бессрочном тарифе")
	}
	if a.autoPayRow(ctx, trafficUID, "ru") != nil {
		t.Fatal("строка автопродления на экране трафик-тарифа")
	}
	fm := a.msg.(*fakeMsg)
	tap(a, "ap:on")
	if got, _ := fs.GetAutoPay(ctx, trafficUID); got == nil || got.Enabled {
		t.Fatalf("кнопка включила автопродление: %+v", got)
	}
	if !strings.Contains(fm.last(), "нет срока") {
		t.Fatalf("экран автопродления: %s", fm.last())
	}
}

func TestMiniTraffic_PayCheck(t *testing.T) {
	a, _, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	var mu sync.Mutex
	paid := false
	yk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet && paid {
			_, _ = w.Write([]byte(`{"id":"yk-chk","status":"succeeded","paid":true,"amount":{"value":"450.00","currency":"RUB"},"metadata":{"telegram_id":"555","months":"0"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"yk-chk","status":"pending","confirmation":{"type":"redirect","confirmation_url":"https://pay.example.com/yk-chk"}}`))
	}))
	defer yk.Close()
	oldBase := yookassa.BaseURL
	yookassa.BaseURL = yk.URL
	defer func() { yookassa.BaseURL = oldBase }()
	a.mu.Lock()
	a.botCfg.YooKassa = model.YooKassaConfig{Enabled: true, ShopID: "shop", SecretKey: "sec", ReturnURL: "https://t.me"}
	a.mu.Unlock()

	if r := a.MiniCheckout(ctx, trafficUID, plan.Code, 0, "", model.PayMethodYooKassa, "450", false, false); !r.OK {
		t.Fatalf("счёт: %+v", r)
	}
	if chk := a.MiniPayCheck(ctx, trafficUID, 0); chk.Paid || chk.Pending != 1 {
		t.Fatalf("до оплаты: %+v", chk)
	}
	mu.Lock()
	paid = true
	mu.Unlock()
	a.webMu.Lock()
	a.payChecks = nil // вторая проверка — не частое нажатие
	a.webMu.Unlock()
	chk := a.MiniPayCheck(ctx, trafficUID, 0)
	if !chk.Paid || chk.Pending != 0 {
		t.Fatalf("после оплаты: %+v", chk)
	}
	if exp, limit, _, _ := panel.state(); exp != remnawave.EternalExpire || limit != 150*gib {
		t.Fatalf("тариф не выдан проверкой: %s %d", exp, limit)
	}
}

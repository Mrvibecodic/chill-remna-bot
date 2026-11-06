package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// Срок без своего лимита показывает лимит панели по умолчанию: «до 3», а при
// выключенном в панели контроле устройств — «без ограничений». Свой лимит
// срока важнее панели; правило внешнего сквада срока — важнее общего.
func TestPlanTermsPanelDefault(t *testing.T) {
	def := remnawave.HwidDefaults{
		Global:   remnawave.HwidRule{Enabled: true, Fallback: 3},
		Ext:      map[string]remnawave.HwidRule{"e-off": {Enabled: false}},
		ExtKnown: true,
	}
	five := 5
	p := &model.Plan{Durations: []model.PlanDuration{{Months: 1, Base: "150"}, {Months: 3, Base: "400"}}}
	dev := func(d *model.PlanDuration) devShown { return planDevShown(p, d, def, true) }
	if got := planTermsText("ru", p, dev); !strings.Contains(got, "Устройства: до 3") {
		t.Fatalf("нет лимита панели: %q", got)
	}
	p.Durations[1].DeviceLimit = &five
	if got := planTermsText("ru", p, dev); !strings.Contains(got, "1 мес — до 3 · 3 мес — до 5") {
		t.Fatalf("свой лимит срока должен быть важнее панели: %q", got)
	}
	p.Durations[1].DeviceLimit = nil
	p.ExtSquad = "e-off"
	if got := planTermsText("ru", p, dev); !strings.Contains(got, "Устройства: без ограничений") {
		t.Fatalf("сквад без контроля устройств: %q", got)
	}
	// Контроль устройств выключен — свой лимит тарифа не применяется, и
	// обещать его покупателю нельзя.
	p.DeviceLimit = 5
	if got := planTermsText("ru", p, dev); !strings.Contains(got, "Устройства: без ограничений") || strings.Contains(got, "до 5") {
		t.Fatalf("при выключенном контроле лимит тарифа — неправда: %q", got)
	}
	p.DeviceLimit = 0
	// Панель не прочитана — как раньше: строки нет.
	nodef := func(d *model.PlanDuration) devShown { return planDevShown(p, d, def, false) }
	if got := planTermsText("ru", p, nodef); strings.Contains(got, "Устройства") {
		t.Fatalf("без панели строки быть не должно: %q", got)
	}
}

// Кэш: удачное чтение живёт hwidDefTTL, после сбоя панель не дёргается
// чаще hwidDefRetry, а экран пользователя без своего лимита получает «N / 3».
func TestHwidDefaultsCache(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/subscription-settings":
			calls.Add(1)
			if fail.Load() {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"Forbidden"}`))
				return
			}
			_, _ = w.Write([]byte(`{"response":{"hwidSettings":{"enabled":true,"fallbackDeviceLimit":3,"maxDevicesAnnounce":null}}}`))
		case "/api/external-squads":
			_, _ = w.Write([]byte(`{"response":{"total":0,"externalSquads":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a, _, _ := newTestApp(t)
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	ctx := context.Background()

	// Первые экраны открываются разом: все ждут один ответ панели, и ни один
	// не рисуется без лимита.
	var wg sync.WaitGroup
	var miss atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := a.hwidDefaults(ctx); !ok {
				miss.Add(1)
			}
		}()
	}
	wg.Wait()
	if miss.Load() != 0 {
		t.Fatalf("%d запросов не дождались ответа панели", miss.Load())
	}
	a.hwidDefaults(ctx)
	if calls.Load() != 1 {
		t.Fatalf("в пределах TTL панель спрошена %d раз", calls.Load())
	}
	info := a.withHwidDefault(ctx, remnawave.DeviceInfo{Used: 2})
	if !info.HasLimit || info.Limit != 3 {
		t.Fatalf("нет лимита по умолчанию: %+v", info)
	}
	if own := a.withHwidDefault(ctx, remnawave.DeviceInfo{Used: 2, OwnLimit: true}); own.HasLimit {
		t.Fatalf("свой «без ограничений» не должен заменяться дефолтом: %+v", own)
	}
	if own := a.withHwidDefault(ctx, remnawave.DeviceInfo{Used: 2, Limit: 4, HasLimit: true, OwnLimit: true}); !own.HasLimit || own.Limit != 4 {
		t.Fatalf("свой лимит при включённом контроле остаётся: %+v", own)
	}

	// Устарело, а панель отвечает 403: последнее значение ещё показывается,
	// и повтор — не раньше hwidDefRetry.
	fail.Store(true)
	a.hwidDef.mu.Lock()
	a.hwidDef.at = time.Now().Add(-hwidDefTTL - time.Second)
	a.hwidDef.mu.Unlock()
	if _, ok := a.hwidDefaults(ctx); !ok {
		t.Fatal("последнее удачное значение должно жить до hwidDefStale")
	}
	a.hwidDefaults(ctx)
	if calls.Load() != 2 {
		t.Fatalf("после сбоя панель спрошена %d раз", calls.Load())
	}
	a.hwidDef.mu.Lock()
	a.hwidDef.at = time.Now().Add(-hwidDefStale - time.Second)
	a.hwidDef.mu.Unlock()
	if _, ok := a.hwidDefaults(ctx); ok {
		t.Fatal("слишком старое значение показывать нельзя")
	}
}

// Контроль устройств в панели выключен: у пользователя со своим лимитом экран
// не пишет «2 / 4» — панель его не применяет.
func TestWithHwidDefaultOff(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.hwidDef.def = remnawave.HwidDefaults{Global: remnawave.HwidRule{Enabled: false, Fallback: 3}, ExtKnown: true}
	a.hwidDef.at = time.Now()
	info := a.withHwidDefault(context.Background(), remnawave.DeviceInfo{Used: 2, Limit: 4, HasLimit: true, OwnLimit: true})
	if info.HasLimit {
		t.Fatalf("лимит при выключенном контроле: %+v", info)
	}
	if got := a.hwidOwnLabel(context.Background(), "ru", 5, ""); !strings.Contains(got, "не применяется") {
		t.Fatalf("админке нужна пометка: %q", got)
	}
}

// Строка сброса в карточке тарифа — по стратегии и только при лимите трафика.
func TestPlanTermsReset(t *testing.T) {
	gb := 30
	p := &model.Plan{Strategy: "MONTH_ROLLING", Durations: []model.PlanDuration{{Months: 3, Base: "400", TrafficGB: &gb}}}
	if got := planTermsText("ru", p, nil); !strings.Contains(got, "Сброс трафика: каждый месяц в день подключения") {
		t.Fatalf("нет строки сброса: %q", got)
	}
	p.Strategy = "NO_RESET"
	if got := planTermsText("ru", p, nil); strings.Contains(got, "Сброс") {
		t.Fatalf("NO_RESET — строки сброса нет: %q", got)
	}
	p.Strategy, p.Durations[0].TrafficGB = "MONTH", nil
	if got := planTermsText("ru", p, nil); strings.Contains(got, "Сброс") {
		t.Fatalf("у безлимита сбрасывать нечего: %q", got)
	}
}

// Экран подписки: трафик периода и дата сброса по Москве.
func TestSubTrafficLine(t *testing.T) {
	s := remnawave.SubSnapshot{TrafficUsed: 5 << 30, TrafficLimit: 30 << 30, NextReset: time.Date(2026, 12, 1, 0, 20, 0, 0, time.UTC)}
	got := trafficLine("ru", s)
	if !strings.Contains(got, "Трафик: <b>5 из 30 ГБ</b>") || !strings.Contains(got, "Сброс трафика: <b>01.12.2026</b>") {
		t.Fatalf("got %q", got)
	}
	if got = trafficLine("ru", remnawave.SubSnapshot{TrafficUsed: 1 << 30}); strings.Contains(got, "Сброс") || !strings.Contains(got, "безлимит") {
		t.Fatalf("безлимит: %q", got)
	}
}

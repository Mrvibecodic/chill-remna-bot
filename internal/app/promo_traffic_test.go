package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remnabot/internal/model"
)

const gb = int64(1024 * 1024 * 1024)

// Код на трафик поднимает потолок и НЕ трогает срок: в панель уходит один
// патч без expireAt.
func TestPromoTraffic_RaisesLimitAndKeepsExpiry(t *testing.T) {
	a, fs, patches := repairFixture(t, 5, 50*gb)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})

	msg, ok := a.redeemPromo(ctx, 555, "giga")
	if !ok {
		t.Fatalf("код на трафик не начислился: %s", msg)
	}
	if len(*patches) != 1 {
		t.Fatalf("ожидался ровно один патч в панель, got %d", len(*patches))
	}
	p := (*patches)[0]
	if got := p["trafficLimitBytes"]; got != float64(75*gb) {
		t.Fatalf("потолок должен стать 75 ГБ, got %v", got)
	}
	if _, has := p["expireAt"]; has {
		t.Fatal("бонусный трафик сдвинул срок подписки")
	}
	for _, k := range []string{"activeInternalSquads", "hwidDeviceLimit", "trafficLimitStrategy"} {
		if _, has := p[k]; has {
			t.Fatalf("бонусный трафик переписал чужие условия: %s", k)
		}
	}
	if pr, _ := fs.GetPromo(ctx, "GIGA"); pr.Used != 1 {
		t.Fatalf("код должен быть потрачен: used=%d", pr.Used)
	}
}

// Безлимиту прибавлять нечего: конечное число ОТОБРАЛО бы безлимит. Отказ,
// код остаётся неиспользованным.
func TestPromoTraffic_UnlimitedRefusedCodeKept(t *testing.T) {
	a, fs, patches := repairFixture(t, 5, 0)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})

	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); ok {
		t.Fatal("при безлимите код не должен срабатывать")
	}
	if len(*patches) != 0 {
		t.Fatalf("в панель ничего слать не должны: %v", *patches)
	}
	if pr, _ := fs.GetPromo(ctx, "GIGA"); pr.Used != 0 {
		t.Fatalf("отказ не должен тратить код: used=%d", pr.Used)
	}
	if done, _ := fs.PromoRedeemedBy(ctx, "GIGA", 555); done {
		t.Fatal("код остался закреплён за человеком после отказа")
	}
}

// Учётки в панели нет — отвечаем «сначала оформите подписку», код цел.
func TestPromoTraffic_NoAccountAsksForSub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_, _ = w.Write([]byte(`{"response":[]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{}})
	}))
	t.Cleanup(srv.Close)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})

	msg, ok := a.redeemPromo(ctx, 555, "GIGA")
	if ok {
		t.Fatal("без учётки в панели начислять некуда")
	}
	if !strings.Contains(msg, "оформите подписку") {
		t.Fatalf("человеку надо сказать, что нужна подписка, а сказали: %s", msg)
	}
	if pr, _ := fs.GetPromo(ctx, "GIGA"); pr.Used != 0 {
		t.Fatalf("отказ не должен тратить код: used=%d", pr.Used)
	}
}

// Панель отказала — код обязан вернуться в оборот, а человек получить отказ.
func TestPromoTraffic_PanelErrorKeepsCode(t *testing.T) {
	var patched int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"response": []map[string]any{{
				"uuid": "u1", "tag": "CHILLBOT", "username": "tg_555",
				"expireAt": "2099-01-01T00:00:00Z", "trafficLimitBytes": 50 * gb,
			}}})
			return
		}
		if r.Method == http.MethodPatch {
			patched++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1"}}`))
	}))
	t.Cleanup(srv.Close)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})

	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); ok {
		t.Fatal("отказ панели выдан за успех")
	}
	if patched == 0 {
		t.Fatal("патч в панель вообще не уходил")
	}
	if pr, _ := fs.GetPromo(ctx, "GIGA"); pr.Used != 0 {
		t.Fatalf("код списан при отказе панели: used=%d", pr.Used)
	}
	if done, _ := fs.PromoRedeemedBy(ctx, "GIGA", 555); done {
		t.Fatal("код остался закреплён за человеком после отказа панели")
	}
}

// Ответ панели потерялся, хотя патч применён: код НЕ возвращаем в оборот,
// иначе одна активация одноразового кода даёт бонус дважды.
func TestPromoTraffic_LostAnswerCountsAsGranted(t *testing.T) {
	limit := int64(50 * gb)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"response": []map[string]any{{
				"uuid": "u1", "tag": "CHILLBOT", "username": "tg_555",
				"expireAt": "2099-01-01T00:00:00Z", "trafficLimitBytes": limit,
			}}})
			return
		}
		if r.Method == http.MethodPatch {
			// Панель применила патч и оборвала ответ.
			limit = 75 * gb
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1"}}`))
	}))
	t.Cleanup(srv.Close)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})

	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); !ok {
		t.Fatal("применённый патч не засчитан")
	}
	if pr, _ := fs.GetPromo(ctx, "GIGA"); pr.Used != 1 {
		t.Fatalf("код вернулся в оборот после применённого патча: used=%d", pr.Used)
	}
}

// Истёкшая подписка: гигабайты уехали бы в мёртвую учётку, код тратить нельзя.
func TestPromoTraffic_ExpiredSubRefused(t *testing.T) {
	a, fs, patches := repairFixture(t, 5, 50*gb)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})
	// repairPanel отдаёт срок 2099 — подменяем стенд на истёкший.
	past := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"response": []map[string]any{{
				"uuid": "u1", "tag": "CHILLBOT", "username": "tg_555",
				"expireAt": "2020-01-01T00:00:00Z", "trafficLimitBytes": 50 * gb,
			}}})
			return
		}
		past++
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1"}}`))
	}))
	t.Cleanup(srv.Close)
	a.panel = testPanel(srv.URL)
	_ = patches

	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); ok {
		t.Fatal("истёкшей подписке начислили трафик")
	}
	if past != 0 {
		t.Fatalf("в панель ушёл запрос на изменение: %d", past)
	}
	if pr, _ := fs.GetPromo(ctx, "GIGA"); pr.Used != 0 {
		t.Fatalf("код списан впустую: used=%d", pr.Used)
	}
}

// Патч обязан адресовать пользователя: без ссылки панель либо ответит 400,
// либо, хуже, применит изменение не туда.
func TestPromoTraffic_PatchCarriesUserRef(t *testing.T) {
	a, fs, patches := repairFixture(t, 5, 50*gb)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})
	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); !ok {
		t.Fatal("не начислилось")
	}
	p := (*patches)[0]
	if p["uuid"] != "u1" && p["id"] == nil {
		t.Fatalf("патч без ссылки на пользователя: %v", p)
	}
}

// Разбор админской строки: вид traffic принимается, значение сверх потолка — нет.
func TestPromoAdmin_CreateTrafficCode(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()

	a.createPromoFromText(ctx, 100, "GIGA traffic 50 10")
	p, _ := fs.GetPromo(ctx, "GIGA")
	if p == nil || p.Kind != model.PromoKindTraffic || p.Value != 50 || p.MaxUses != 10 {
		t.Fatalf("код на трафик не создан как надо: %+v", p)
	}

	a.createPromoFromText(ctx, 100, "HUGE traffic 99999999")
	if got, _ := fs.GetPromo(ctx, "HUGE"); got != nil {
		t.Fatalf("значение сверх потолка принято: %+v", got)
	}
	a.createPromoFromText(ctx, 100, "BAD nonsense 5")
	if got, _ := fs.GetPromo(ctx, "BAD"); got != nil {
		t.Fatalf("неизвестный вид принят: %+v", got)
	}
}

// Дедлайн запроса истёк (мини-апп даёт 12 секунд на два похода в панель).
// Компенсация обязана идти на фоновом контексте: на мёртвом она падала молча,
// и код списывался навсегда — человек получал «вы уже активировали» без бонуса.
func TestPromoTraffic_DeadContextStillReleasesCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[]}`))
	}))
	t.Cleanup(srv.Close)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})

	dead, cancel := context.WithCancel(ctx)
	cancel()
	if _, ok := a.redeemPromo(dead, 555, "GIGA"); ok {
		t.Fatal("на мёртвом контексте бонус не мог начислиться")
	}
	if pr, _ := fs.GetPromo(ctx, "GIGA"); pr.Used != 0 {
		t.Fatalf("код остался списанным после отказа: used=%d", pr.Used)
	}
	if done, _ := fs.PromoRedeemedBy(ctx, "GIGA", 555); done {
		t.Fatal("код остался закреплён за человеком навсегда")
	}
}

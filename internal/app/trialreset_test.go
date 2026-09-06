package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"remnabot/internal/model"
)

// trialPanel отдаёт учётку с заданным трафиком и сроком и считает обнуления.
func trialPanel(t *testing.T, limit, used int64, expireAt string, resets *int) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/actions/reset-traffic"):
			mu.Lock()
			*resets++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"response":{"uuid":"u1"}}`))
		case strings.Contains(r.URL.Path, "/by-telegram-id/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"response": []map[string]any{{
				"uuid": "u1", "tag": "CHILLBOT", "username": "tg_555",
				"subscriptionUrl": "https://sub/x", "expireAt": expireAt,
				"trafficLimitBytes": limit, "usedTrafficBytes": used,
				"trafficLimitStrategy": "NO_RESET",
			}}})
		default:
			_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/x","expireAt":"2099-01-01T00:00:00Z"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func trialResetApp(t *testing.T, limit, used int64, panelExpire string) (*App, *fakeStore, *int) {
	t.Helper()
	resets := 0
	srv := trialPanel(t, limit, used, panelExpire, &resets)
	a, fs := snapApp(t, srv.URL)
	a.botCfg.Trial = model.TrialConfig{
		Enabled: true, Days: 3, TrafficGB: 10,
		ResetUnused: true, ResetUnusedPct: 5, ResetUnusedMax: 1,
	}
	return a, fs, &resets
}

const trialGB = int64(10) * 1024 * 1024 * 1024

// Свежий кандидат: триал брал, срок кончился, трафик не тронут, денег не
// платил — отметка снимается, счётчик в панели обнуляется, человека зовут.
func TestTrialReset_UnusedComesBack(t *testing.T) {
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	a, fs, resets := trialResetApp(t, trialGB, 0, past)
	ctx := context.Background()
	seedTrialUser(t, fs, past)

	if n := a.resetTrialsOnce(ctx); n != 1 {
		t.Fatalf("ожидался один возврат, got %d", n)
	}
	u, _ := fs.GetUser(ctx, 555)
	if u.TrialUsedAt != "" || u.SubExpireAt != "" {
		t.Fatalf("отметка не снята: trial=%q exp=%q", u.TrialUsedAt, u.SubExpireAt)
	}
	if *resets != 1 {
		t.Fatalf("счётчик трафика в панели не обнулён: %d", *resets)
	}
	if !a.trialAvailable(ctx, 555) {
		t.Fatal("после возврата триал обязан снова быть доступен")
	}
	if !strings.Contains(strings.Join(a.msg.(*fakeMsg).texts, "\n"), "Пробный период не пригодился") {
		t.Fatal("человека не позвали обратно")
	}
	// Второй проход не должен возвращать снова: потолок повторов — один.
	if n := a.resetTrialsOnce(ctx); n != 0 {
		t.Fatalf("потолок повторов не соблюдён: %d", n)
	}
}

// Потрачено больше порога — триал был настоящим, возвращать нечего.
func TestTrialReset_UsedAboveThresholdStays(t *testing.T) {
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	a, fs, resets := trialResetApp(t, trialGB, trialGB/2, past)
	ctx := context.Background()
	seedTrialUser(t, fs, past)

	if n := a.resetTrialsOnce(ctx); n != 0 {
		t.Fatalf("использованный триал вернули: %d", n)
	}
	if *resets != 0 {
		t.Fatalf("трафик обнулили зря: %d", *resets)
	}
	if u, _ := fs.GetUser(ctx, 555); u.TrialUsedAt == "" {
		t.Fatal("отметка об использованном триале снята зря")
	}
}

// Платившему клиенту триал не возвращают, даже если трафик не тронут.
func TestTrialReset_PayingCustomerNotTouched(t *testing.T) {
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	a, fs, resets := trialResetApp(t, trialGB, 0, past)
	ctx := context.Background()
	seedTrialUser(t, fs, past)
	_ = fs.AddPayment(ctx, &model.Payment{
		TelegramID: 555, Method: model.PayMethodYooKassa, Months: 1,
		Amount: "150", Status: model.PaymentPaid,
	})

	if n := a.resetTrialsOnce(ctx); n != 0 {
		t.Fatalf("платившему вернули триал: %d", n)
	}
	if *resets != 0 {
		t.Fatalf("трафик обнулили зря: %d", *resets)
	}
}

// Срок в панели ещё живой (админ продлил руками) — подписка у человека есть,
// снимать отметку рано.
func TestTrialReset_LiveSubInPanelNotTouched(t *testing.T) {
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	future := time.Now().UTC().Add(72 * time.Hour).Format(time.RFC3339)
	a, fs, resets := trialResetApp(t, trialGB, 0, future)
	ctx := context.Background()
	seedTrialUser(t, fs, past)

	if n := a.resetTrialsOnce(ctx); n != 0 {
		t.Fatalf("живую подписку сбросили: %d", n)
	}
	if *resets != 0 {
		t.Fatalf("трафик обнулили зря: %d", *resets)
	}
}

// Выключенный возврат ничего не делает, даже когда кандидат идеальный.
func TestTrialReset_DisabledDoesNothing(t *testing.T) {
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	a, fs, resets := trialResetApp(t, trialGB, 0, past)
	a.botCfg.Trial.ResetUnused = false
	ctx := context.Background()
	seedTrialUser(t, fs, past)

	if n := a.resetTrialsOnce(ctx); n != 0 {
		t.Fatalf("выключенный возврат сработал: %d", n)
	}
	if *resets != 0 {
		t.Fatalf("трафик обнулили зря: %d", *resets)
	}
}

// Регрессия: после возврата человек выглядит как новичок, и привязка
// панельного аккаунта ставила ему «триал использован» обратно — возврат молча
// отменялся на первом же заходе в бота.
func TestTrialReset_PanelSyncDoesNotTakeItBack(t *testing.T) {
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	a, fs, _ := trialResetApp(t, trialGB, 0, past)
	ctx := context.Background()
	seedTrialUser(t, fs, past)

	if n := a.resetTrialsOnce(ctx); n != 1 {
		t.Fatalf("возврат не сработал: %d", n)
	}
	// Человек заходит в бота: панель по-прежнему знает его аккаунт.
	a.syncPanelAccount(ctx, 555)
	if u, _ := fs.GetUser(ctx, 555); u.TrialUsedAt != "" || u.SubExpireAt != "" {
		t.Fatalf("привязка панели отобрала возвращённый триал: trial=%q exp=%q", u.TrialUsedAt, u.SubExpireAt)
	}
	if !a.trialAvailable(ctx, 555) {
		t.Fatal("после захода в бота триал снова недоступен")
	}
}

// Учётки в панели нет — расход проверить не у кого, возвращать вслепую нельзя.
func TestTrialReset_NoPanelAccountSkipped(t *testing.T) {
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_, _ = w.Write([]byte(`{"response":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1"}}`))
	}))
	t.Cleanup(srv.Close)
	a, fs := snapApp(t, srv.URL)
	a.botCfg.Trial = model.TrialConfig{Enabled: true, Days: 3, TrafficGB: 10,
		ResetUnused: true, ResetUnusedPct: 5, ResetUnusedMax: 1}
	ctx := context.Background()
	seedTrialUser(t, fs, past)

	if n := a.resetTrialsOnce(ctx); n != 0 {
		t.Fatalf("вернули триал вслепую: %d", n)
	}
	if u, _ := fs.GetUser(ctx, 555); u.TrialUsedAt == "" {
		t.Fatal("отметка снята без учётки в панели")
	}
}

// Отключённая в панели учётка — это наказание (админ или торрент-блокер).
// Возврат позвал бы человека письмом на нерабочую ссылку.
func TestTrialReset_DisabledInPanelSkipped(t *testing.T) {
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	resets := 0
	srv := statusPanel(t, past, "DISABLED", &resets)
	a, fs := snapApp(t, srv.URL)
	a.botCfg.Trial = model.TrialConfig{Enabled: true, Days: 3, TrafficGB: 10,
		ResetUnused: true, ResetUnusedPct: 5, ResetUnusedMax: 1}
	ctx := context.Background()
	seedTrialUser(t, fs, past)

	if n := a.resetTrialsOnce(ctx); n != 0 {
		t.Fatalf("отключённому в панели вернули триал: %d", n)
	}
	if resets != 0 {
		t.Fatalf("трафик обнулили зря: %d", resets)
	}
}

// Счётчик израсходованного в панели периодный: после границы периода «ноль
// потрачено» больше ничего не значит, и такого кандидата трогать нельзя.
func TestTrialReset_StaleCounterNotTrusted(t *testing.T) {
	long := time.Now().UTC().Add(-45 * 24 * time.Hour).Format(time.RFC3339)
	resets := 0
	// Стратегия MONTH: панель обнуляет счётчик каждый месяц, а подписка
	// кончилась 45 дней назад — «ноль потрачено» уже ничего не доказывает.
	srv := strategyPanel(t, long, "MONTH", &resets)
	a, fs := snapApp(t, srv.URL)
	a.botCfg.Trial = model.TrialConfig{Enabled: true, Days: 3, TrafficGB: 10,
		ResetUnused: true, ResetUnusedPct: 5, ResetUnusedMax: 1}
	ctx := context.Background()
	seedTrialUser(t, fs, long)

	if n := a.resetTrialsOnce(ctx); n != 0 {
		t.Fatalf("поверили счётчику за границей периода сброса: %d", n)
	}
	if resets != 0 {
		t.Fatalf("трафик обнулили зря: %d", resets)
	}
	// Тот же кандидат при NO_RESET возвращается: дело именно в стратегии.
	r2 := 0
	srv2 := strategyPanel(t, long, "NO_RESET", &r2)
	a2, fs2 := snapApp(t, srv2.URL)
	a2.botCfg.Trial = a.botCfg.Trial
	seedTrialUser(t, fs2, long)
	if n := a2.resetTrialsOnce(ctx); n != 1 {
		t.Fatalf("контроль: при NO_RESET возврат обязан сработать, got %d", n)
	}
}

func TestTrafficWindow(t *testing.T) {
	if trafficWindow("NO_RESET") != 0 {
		t.Fatal("при NO_RESET счётчику можно верить всегда")
	}
	if trafficWindow("DAY") != 24*time.Hour {
		t.Fatal("окно для DAY")
	}
	if trafficWindow("") != 31*24*time.Hour {
		t.Fatal("пустая стратегия — умолчание панели, берём месяц")
	}
}

// statusPanel — учётка с заданным статусом и нетронутым трафиком.
func statusPanel(t *testing.T, expireAt, status string, resets *int) *httptest.Server {
	return panelWith(t, expireAt, status, "NO_RESET", resets)
}

// strategyPanel — живая учётка с заданной стратегией сброса трафика.
func strategyPanel(t *testing.T, expireAt, strategy string, resets *int) *httptest.Server {
	return panelWith(t, expireAt, "", strategy, resets)
}

func panelWith(t *testing.T, expireAt, status, strategy string, resets *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/actions/reset-traffic") {
			*resets++
			_, _ = w.Write([]byte(`{"response":{"uuid":"u1"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"response": []map[string]any{{
				"uuid": "u1", "tag": "CHILLBOT", "username": "tg_555", "status": status,
				"expireAt": expireAt, "trafficLimitBytes": trialGB, "usedTrafficBytes": 0,
				"trafficLimitStrategy": strategy,
			}}})
			return
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTrialUnused_Threshold(t *testing.T) {
	cases := []struct {
		name  string
		limit int64
		used  int64
		pct   int
		want  bool
	}{
		{"нетронутый", trialGB, 0, 0, true},
		{"нетронутый безлимит", 0, 0, 50, true},
		{"тронутый безлимит", 0, 1, 50, false},
		{"ровно порог", trialGB, trialGB / 20, 5, true},
		{"на байт выше порога", trialGB, trialGB/20 + 1, 5, false},
		{"нулевой порог, потрачен байт", trialGB, 1, 0, false},
	}
	for _, c := range cases {
		if got := trialUnused(c.limit, c.used, c.pct); got != c.want {
			t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func seedTrialUser(t *testing.T, fs *fakeStore, expire string) {
	t.Helper()
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.SetTrialUsed(ctx, 555, expire)
	_ = fs.SetSubExpiry(ctx, 555, expire, "trial")
	_ = fs.AddPayment(ctx, &model.Payment{
		TelegramID: 555, Method: model.PayMethodTrial, Months: 0,
		Amount: "—", Status: model.PaymentPaid,
	})
}

package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// bcastPanel — панель со списком пользователей постранично. Первыми идут
// чужие живые учётки, чтобы нужные лежали на второй странице.
func bcastPanel(t *testing.T, users []map[string]any, fail bool) *remnawave.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/api/users" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		size, _ := strconv.Atoi(r.URL.Query().Get("size"))
		end := min(start+size, len(users))
		page := []map[string]any{}
		if start < len(users) {
			page = users[start:end]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"users": page, "total": len(users)}})
	}))
	t.Cleanup(srv.Close)
	return remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

// bcastSent — кому ушла рассылка «msg {id}» (админ не в счёт).
func bcastSent(fm *fakeMsg) []int64 {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	var out []int64
	for _, s := range fm.texts {
		if rest, ok := strings.CutPrefix(s, "msg "); ok {
			if id, err := strconv.ParseInt(rest, 10, 64); err == nil && id != planAdmin {
				out = append(out, id)
			}
		}
	}
	slices.Sort(out)
	return out
}

func bcastSegApp(t *testing.T, fail bool) (*App, *fakeMsg) {
	t.Helper()
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := fs.SavePlan(ctx, &model.Plan{Code: "vip", Name: "VIP"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	alive := now.Add(48 * time.Hour).Format(time.RFC3339)
	dead := now.Add(-48 * time.Hour).Format(time.RFC3339)
	snap := func(code string) *model.PlanSnapshot { return &model.PlanSnapshot{Code: code, Months: 1} }
	local := []struct {
		id   int64
		exp  string
		kind string
		snap *model.PlanSnapshot
	}{
		{301, alive, "trial", nil},
		{302, alive, "paid", snap("vip")},
		{303, alive, "paid", snap("base")},
		{304, dead, "paid", snap("vip")},   // продлили в панели руками
		{305, "", "", nil},                 // подписки не было
		{306, alive, "paid", nil},          // выдана вручную, тариф неизвестен
		{307, alive, "paid", snap("vip")},  // в панели срок уже снят
		{-5, alive, "paid", snap("vip")},   // аккаунт кабинета по почте
		{308, alive, "paid", snap("vip")},  // панель его не вернула
		{309, alive, "paid", snap("base")}, // в панели выключен
		{310, dead, "trial", nil},          // после триала выдали в панели руками
	}
	for _, u := range local {
		_ = fs.UpsertUser(ctx, u.id)
		fs.users[u.id].SubExpireAt = u.exp
		fs.users[u.id].NotifyKind = u.kind
		fs.users[u.id].Snapshot = u.snap
	}
	var pu []map[string]any
	for i := range 260 {
		pu = append(pu, map[string]any{"uuid": "f" + strconv.Itoa(i), "username": "x" + strconv.Itoa(i), "telegramId": 900000 + i, "expireAt": alive})
	}
	pu = append(pu,
		map[string]any{"uuid": "u301", "username": "tg_301", "telegramId": 301, "expireAt": alive},
		map[string]any{"uuid": "u302", "username": "tg_302", "telegramId": 302, "expireAt": alive},
		map[string]any{"uuid": "u302b", "username": "tg_302_b", "telegramId": 302, "expireAt": dead}, // вторая учётка, истекла
		map[string]any{"uuid": "u303", "username": "tg_303", "telegramId": 303, "expireAt": alive},
		map[string]any{"uuid": "u304", "username": "tg_304", "telegramId": 304, "expireAt": alive},
		map[string]any{"uuid": "u306", "username": "tg_306", "expireAt": alive},
		map[string]any{"uuid": "u307", "username": "tg_307", "telegramId": 307, "expireAt": dead},
		map[string]any{"uuid": "u309", "username": "tg_309", "telegramId": 309, "expireAt": alive, "status": "DISABLED"},
		map[string]any{"uuid": "u310", "username": "tg_310", "telegramId": 310, "expireAt": alive},
	)
	a.panel = bcastPanel(t, pu, fail)
	return a, fm
}

func TestBroadcastSegments_Send(t *testing.T) {
	all := []int64{301, 302, 303, 304, 305, 306, 307, 308, 309, 310}
	cases := []struct {
		name  string
		taps  []string
		want  []int64
		label string
	}{
		{"всем — как раньше", nil, all, "Всем"},
		{"триал", []string{"trial"}, []int64{301}, "🎁 Пробный период"},
		{"тариф — срок из панели", []string{"p:vip"}, []int64{302, 304, 308}, "VIP"},
		{"без подписки — срок и статус из панели", []string{"none"}, []int64{305, 307, 309}, "Без подписки"},
		{"несколько групп — каждому один раз", []string{"trial", "none", "p:base"}, []int64{301, 303, 305, 307, 309}, "🎁 Пробный период, Без подписки, Базовый"},
		{"снятая галочка", []string{"trial", "p:vip", "trial"}, []int64{302, 304, 308}, "VIP"},
		{"«Всем» сбрасывает выбор", []string{"trial", "all"}, all, "Всем"},
		{"старая кнопка чужого тарифа", []string{"p:x"}, all, "Всем"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, fm := bcastSegApp(t, false)
			planTap(t, a, "bc:new")
			a.handleMessage(context.Background(), msgText(planAdmin, "msg {id}"))
			for _, s := range tc.taps {
				planTap(t, a, "bc:sg:"+s)
			}
			planTap(t, a, "bc:send")
			waitFor(t, func() bool { return !a.bcastRunning.Load() })
			if got := bcastSent(fm); !slices.Equal(got, tc.want) {
				t.Fatalf("получатели %v, ждали %v\n%s", got, tc.want, fm.joined())
			}
			n := len(tc.want)
			report := fm.last()
			if !strings.Contains(report, "Кому: "+tc.label+"\n") || !strings.Contains(report, "Получателей: "+strconv.Itoa(n)+"\n") ||
				!strings.Contains(report, "Доставлено: "+strconv.Itoa(n)+"\n") {
				t.Fatalf("итог админу:\n%s", report)
			}
		})
	}
}

// Предпросмотр считает по базе бота и показывает выбор.
func TestBroadcastSegments_Preview(t *testing.T) {
	a, fm := bcastSegApp(t, false)
	planTap(t, a, "bc:new")
	a.handleMessage(context.Background(), msgText(planAdmin, "msg {id}"))
	if !strings.Contains(fm.last(), "Кому: Всем") || !hasCB(fm.allCallbackData(), "bc:sg:p:vip") {
		t.Fatalf("нет выбора получателей:\n%s\n%v", fm.last(), fm.allCallbackData())
	}
	planTap(t, a, "bc:sg:p:vip")
	planTap(t, a, "bc:sg:none")
	last := fm.last()
	// По базе: VIP — 302, 307 и 308, без подписки — 304, 305 и 310.
	if !strings.Contains(last, "получателей: 6") || !strings.Contains(last, "Кому: Без подписки, VIP") {
		t.Fatalf("предпросмотр:\n%s", last)
	}

	// Удалённый тариф снимается с выбора: кнопки у него больше нет.
	_ = a.store.DeletePlan(context.Background(), "vip")
	planTap(t, a, "bc:sg:trial")
	if last := fm.last(); !strings.Contains(last, "получателей: 4") || !strings.Contains(last, "Кому: 🎁 Пробный период, Без подписки\n") {
		t.Fatalf("удалённый тариф остался в выборе:\n%s", last)
	}

	// Новая рассылка начинается со «Всем».
	planTap(t, a, "bc:new")
	a.handleMessage(context.Background(), msgText(planAdmin, "msg {id}"))
	if !strings.Contains(fm.last(), "Кому: Всем") {
		t.Fatalf("выбор прошлой рассылки остался:\n%s", fm.last())
	}
}

// Панель не ответила — рассылка по группам не начинается вовсе.
func TestBroadcastSegments_PanelDown(t *testing.T) {
	a, fm := bcastSegApp(t, true)
	planTap(t, a, "bc:new")
	a.handleMessage(context.Background(), msgText(planAdmin, "msg {id}"))
	planTap(t, a, "bc:sg:none")
	planTap(t, a, "bc:send")
	waitFor(t, func() bool { return !a.bcastRunning.Load() })
	if got := bcastSent(fm); len(got) != 0 {
		t.Fatalf("ушло без сверки: %v", got)
	}
	if !strings.Contains(fm.joined(), "рассылка не начата") {
		t.Fatalf("админу не сказали:\n%s", fm.joined())
	}
}

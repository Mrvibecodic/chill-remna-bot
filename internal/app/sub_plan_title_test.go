package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// planTitlePanel — панель с одной учёткой заданного статуса и срока.
func planTitlePanel(t *testing.T, status, expire string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u-1","telegramId":1000000001,"status":"` + status +
				`","subscriptionUrl":"https://s/ex","expireAt":"` + expire + `"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Тариф текущей подписки виден клиенту и в чате, и в мини-аппе/ЛК — только
// у живой подписки.
func TestSubPlanTitle(t *testing.T) {
	const uid int64 = 1000000001
	const future, past = "2030-01-02T03:04:05Z", "2020-01-02T03:04:05Z"
	std := &model.Plan{Code: "std", Name: "Стандарт", Icon: "⚡", Enabled: true}
	for _, tc := range []struct {
		name   string
		status string
		expire string
		kind   string
		snap   *model.PlanSnapshot
		plan   *model.Plan
		want   string // в мини-аппе
		wantC  string // строка в чате; "" — строки нет
		screen string // что точно есть на экране чата
	}{
		{"переименованный тариф", "ACTIVE", future, "paid", &model.PlanSnapshot{Code: "std", Name: "Старое"}, std,
			"⚡ Стандарт", "🏷 Тариф: <b>⚡ Стандарт</b>", "активна до"},
		{"тариф удалён", "ACTIVE", future, "paid", &model.PlanSnapshot{Code: "old", Name: "Архив"}, nil,
			"Архив", "🏷 Тариф: <b>Архив</b>", "активна до"},
		{"«Базовый» ещё не в базе", "ACTIVE", future, "paid", &model.PlanSnapshot{Code: model.PlanCodeBase}, nil,
			"Базовый", "🏷 Тариф: <b>Базовый</b>", "активна до"},
		{"триал", "ACTIVE", future, "trial", &model.PlanSnapshot{Code: "std", Name: "Стандарт"}, std,
			"🎁 Пробный период", "🏷 Тариф: <b>🎁 Пробный период</b>", "активна до"},
		{"покупок не было", "ACTIVE", future, "paid", nil, nil, "", "", "активна до"},
		{"разметка в имени", "ACTIVE", future, "paid", &model.PlanSnapshot{Code: "x"},
			&model.Plan{Code: "x", Name: "<i>Про", Enabled: true},
			"<i>Про", "🏷 Тариф: <b>&lt;i&gt;Про</b>", "активна до"},
		{"истекла", "EXPIRED", past, "paid", &model.PlanSnapshot{Code: "std"}, std, "", "", "закончился"},
		{"срок прошёл, статус ещё старый", "ACTIVE", past, "paid", &model.PlanSnapshot{Code: "std"}, std, "", "", "закончился"},
		{"трафик исчерпан", "LIMITED", future, "paid", &model.PlanSnapshot{Code: "std"}, std, "", "", "исчерпан"},
		{"заблокирована", "DISABLED", future, "paid", &model.PlanSnapshot{Code: "std"}, std, "", "", "заблокирована"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fm := &fakeMsg{}
			fs := &fakeStore{}
			a := &App{
				cfg:    &config.Config{AdminID: 100, DataDir: t.TempDir()},
				log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				msg:    fm,
				wiz:    map[int64]*wizard{},
				ui:     map[int64]*uiState{},
				store:  fs,
				botCfg: fullyConfigured(),
				panel:  remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: planTitlePanel(t, tc.status, tc.expire), APIToken: "t"}),
			}
			_ = fs.UpsertUser(ctx, uid)
			_ = fs.SetSubExpiry(ctx, uid, tc.expire, tc.kind)
			_ = fs.SetUserSnapshot(ctx, uid, tc.snap)
			if tc.plan != nil {
				_ = fs.SavePlan(ctx, tc.plan)
			}
			if got := a.MiniSubscription(ctx, uid).PlanTitle; got != tc.want {
				t.Fatalf("мини-апп: %q, ждали %q", got, tc.want)
			}
			a.handleCallback(ctx, cb(uid, "menu:mysubs"))
			out := fm.joined()
			if !strings.Contains(out, tc.screen) {
				t.Fatalf("не тот экран подписки (ждали %q):\n%s", tc.screen, out)
			}
			if tc.wantC == "" {
				if strings.Contains(out, "Тариф:") {
					t.Fatalf("строки тарифа быть не должно:\n%s", out)
				}
				return
			}
			if !strings.Contains(out, tc.wantC) {
				t.Fatalf("в чате нет %q:\n%s", tc.wantC, out)
			}
		})
	}
}

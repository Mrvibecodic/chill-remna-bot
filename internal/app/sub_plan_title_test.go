package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// Тариф текущей подписки виден клиенту и в чате, и в мини-аппе/ЛК.
func TestSubPlanTitle(t *testing.T) {
	srv := devicesPanel(t, "", 0)
	defer srv.Close()
	const uid int64 = 1000000001
	for _, tc := range []struct {
		name  string
		kind  string
		snap  *model.PlanSnapshot
		plan  *model.Plan
		want  string // в мини-аппе
		wantC string // строка в чате; "" — строки нет
	}{
		{"переименованный тариф", "paid", &model.PlanSnapshot{Code: "std", Name: "Старое"},
			&model.Plan{Code: "std", Name: "Стандарт", Icon: "⚡", Enabled: true},
			"⚡ Стандарт", "🏷 Тариф: <b>⚡ Стандарт</b>"},
		{"тариф удалён", "paid", &model.PlanSnapshot{Code: "old", Name: "Архив"}, nil,
			"Архив", "🏷 Тариф: <b>Архив</b>"},
		{"триал", "trial", &model.PlanSnapshot{Code: "std", Name: "Стандарт"},
			&model.Plan{Code: "std", Name: "Стандарт", Enabled: true},
			"🎁 Пробный период", "🏷 Тариф: <b>🎁 Пробный период</b>"},
		{"покупок не было", "paid", nil, nil, "", ""},
		{"разметка в имени", "paid", &model.PlanSnapshot{Code: "x"},
			&model.Plan{Code: "x", Name: "<i>Про", Enabled: true},
			"<i>Про", "🏷 Тариф: <b>&lt;i&gt;Про</b>"},
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
				panel:  remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"}),
			}
			_ = fs.UpsertUser(ctx, uid)
			_ = fs.SetSubExpiry(ctx, uid, "2030-01-02T03:04:05Z", tc.kind)
			_ = fs.SetUserSnapshot(ctx, uid, tc.snap)
			if tc.plan != nil {
				_ = fs.SavePlan(ctx, tc.plan)
			}
			if got := a.MiniSubscription(ctx, uid).PlanTitle; got != tc.want {
				t.Fatalf("мини-апп: %q, ждали %q", got, tc.want)
			}
			a.handleCallback(ctx, cb(uid, "menu:mysubs"))
			out := fm.joined()
			if !strings.Contains(out, "активна до") {
				t.Fatalf("экран подписки не показан:\n%s", out)
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

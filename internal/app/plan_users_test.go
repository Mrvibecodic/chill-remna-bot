package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// «Тарифы и триал»: сколько людей с действующей подпиской на каждом тарифе.
func TestPayPlans_UsersByPlan(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	for _, p := range []*model.Plan{
		{Code: "prem01", Name: "Премиум", Enabled: true, Durations: []model.PlanDuration{{Months: 1, Base: "300"}}},
		{Code: "trf150", Name: "Трафик 150", Enabled: false, Kind: model.PlanKindTraffic, TrafficGB: 150, Traffic: &model.PlanTraffic{Price: "450"}},
	} {
		if err := fs.SavePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	const live, dead = "2099-01-01T00:00:00Z", "2020-01-01T00:00:00Z"
	for _, u := range []struct {
		id          int64
		exp, kind   string
		code        string
		banned      bool
		unreachable bool
	}{
		{1000000001, live, "paid", "prem01", false, false},
		{1000000002, live, "paid", "prem01", false, true},
		{-1000000003, live, "paid", "trf150", false, false},
		{1000000004, live, "trial", "", false, false},
		{1000000005, live, "paid", "gone01", false, false},
		{1000000006, live, "paid", "", false, false},
		{1000000007, dead, "paid", "prem01", false, false},
		{1000000008, live, "paid", "prem01", true, false},
	} {
		_ = fs.UpsertUser(ctx, u.id)
		_ = fs.SetSubExpiry(ctx, u.id, u.exp, u.kind)
		if u.code != "" {
			_ = fs.SetUserSnapshot(ctx, u.id, &model.PlanSnapshot{Code: u.code, Months: 1})
		}
		if u.banned {
			_ = fs.SetBlocked(ctx, u.id, true)
		}
		if u.unreachable {
			_ = fs.SetUnreachable(ctx, u.id, "2020-01-01T00:00:00Z")
		}
	}
	planTap(t, a, "menu:payplans")
	out := fm.last()
	for _, want := range []string{
		"👥 <b>С подпиской: 6</b>",
		"• Премиум — <b>2</b>",
		"• Трафик 150 — <b>1</b>",
		"🎁 Триал — <b>1</b>",
		"❔ Тариф не известен — <b>2</b>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("нет %q в сводке:\n%s", want, out)
		}
	}
	if strings.Contains(out, "%!") {
		t.Fatalf("битый шаблон:\n%s", out)
	}
}

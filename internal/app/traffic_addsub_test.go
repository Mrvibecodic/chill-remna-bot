package app

import (
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// Доп-подписка трафик-тарифа сбрасывается по своему периоду: основная не
// сбрасывается никогда. У срочных сделок стратегия остаётся как у основной.
func TestApplyAddSubSnapStrategy(t *testing.T) {
	cases := []struct {
		name string
		snap *model.PlanSnapshot
		want string
	}{
		{"срочный", &model.PlanSnapshot{Months: 1}, ""},
		{"трафик со своим периодом", &model.PlanSnapshot{Kind: model.PlanKindTraffic, AddSubStrategy: "DAY"}, "DAY"},
		{"трафик без периода", &model.PlanSnapshot{Kind: model.PlanKindTraffic}, model.DefaultAddSubStrategy},
		{"битый период", &model.PlanSnapshot{Kind: model.PlanKindTraffic, AddSubStrategy: "YEAR"}, model.DefaultAddSubStrategy},
	}
	for _, c := range cases {
		opt := remnawave.AddSubOptions{}
		applyAddSubSnap(&opt, c.snap, remnawave.LiveSquads{})
		if opt.Strategy != c.want {
			t.Errorf("%s: стратегия %q, ожидалась %q", c.name, opt.Strategy, c.want)
		}
	}
}

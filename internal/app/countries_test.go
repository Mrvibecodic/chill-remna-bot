package app

import (
	"context"
	"testing"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func TestSplitFlag(t *testing.T) {
	cases := []struct{ in, flag, name string }{
		{"🇩🇪 Германия", "🇩🇪", "Германия"},
		{"🇳🇱 Netherlands #1", "🇳🇱", "Netherlands"},
		{"  🇺🇸 USA-2 ", "🇺🇸", "USA"},
		{"Premium 🇫🇷 France (3)", "🇫🇷", "Premium France"},
		{"no flag here", "", ""},
	}
	for _, c := range cases {
		f, n := splitFlag(c.in)
		if f != c.flag || n != c.name {
			t.Fatalf("splitFlag(%q)=%q,%q want %q,%q", c.in, f, n, c.flag, c.name)
		}
	}
}

func TestPlanCountries_DedupAndFilter(t *testing.T) {
	a := &App{ui: map[int64]*uiState{}}
	a.botCfg = &model.BotConfig{
		Plan: model.SubscriptionPlan{ActiveInternalSquads: []string{"sq1"}},
	}
	a.botCfg.NormalizePricing()
	a.infraCache = &infraCacheEntry{
		fetchedAt: time.Now(),
		squads: []remnawave.SquadFull{
			{UUID: "sq1", InboundsCount: 3, InboundUUIDs: []string{"ib1", "ib2"}},
			{UUID: "sq2", InboundsCount: 1, InboundUUIDs: []string{"ib9"}},
		},
		hosts: []remnawave.Host{
			{Remark: "🇩🇪 Германия #1", InboundUUID: "ib1"},
			{Remark: "🇩🇪 Германия #2", InboundUUID: "ib2"},
			{Remark: "🇳🇱 Нидерланды", InboundUUID: "ib1"},
			{Remark: "🇺🇸 USA", InboundUUID: "ib1", Hidden: true},
			{Remark: "🇫🇷 France", InboundUUID: "ib1", ExcludedSquads: []string{"sq1"}},
			{Remark: "🇯🇵 Japan", InboundUUID: "ib9"},
		},
	}
	cs, inb := a.planCountries(context.Background(), 1)
	if len(cs) != 2 || cs[0].Code != "de" || cs[0].Name != "Германия" || cs[1].Code != "nl" {
		t.Fatalf("countries=%+v want DE,NL deduped", cs)
	}
	if cs[0].Flag != "🇩🇪" {
		t.Fatalf("flag=%q want 🇩🇪", cs[0].Flag)
	}
	if inb != 3 {
		t.Fatalf("inbounds=%d want 3", inb)
	}
}

// Панель 3.4 отдаёт доступ хоста объектом с режимом: список стран обязан его
// учитывать, а не показывать всё подряд.
func TestPlanCountries_SquadModes(t *testing.T) {
	a := &App{ui: map[int64]*uiState{}}
	a.botCfg = &model.BotConfig{
		Plan: model.SubscriptionPlan{ActiveInternalSquads: []string{"sq1"}},
	}
	a.botCfg.NormalizePricing()
	a.infraCache = &infraCacheEntry{
		fetchedAt: time.Now(),
		squads: []remnawave.SquadFull{
			{UUID: "sq1", InboundsCount: 1, InboundUUIDs: []string{"ib1"}},
		},
		hosts: []remnawave.Host{
			{Remark: "🇩🇪 Германия", InboundUUID: "ib1",
				SquadsMode: remnawave.SquadsModeExclude, Squads: []string{"sq1"}},
			{Remark: "🇳🇱 Нидерланды", InboundUUID: "ib1",
				SquadsMode: remnawave.SquadsModeExclude, Squads: []string{"sq9"}},
			{Remark: "🇫🇷 France", InboundUUID: "ib1",
				SquadsMode: remnawave.SquadsModeAllowOnly, Squads: []string{"sq9"}},
			{Remark: "🇯🇵 Japan", InboundUUID: "ib1",
				SquadsMode: remnawave.SquadsModeAllowOnly, Squads: []string{"sq1"}},
		},
	}
	cs, _ := a.planCountries(context.Background(), 1)
	if len(cs) != 2 || cs[0].Code != "nl" || cs[1].Code != "jp" {
		t.Fatalf("countries=%+v want NL,JP", cs)
	}
}

// Инбаунд от одного сквада тарифа, разрешение — от другого: панель такой хост
// не отдаст, и в списке стран его быть не должно.
func TestPlanCountries_InboundAndAccessSameSquad(t *testing.T) {
	a := &App{ui: map[int64]*uiState{}}
	a.botCfg = &model.BotConfig{
		Plan: model.SubscriptionPlan{ActiveInternalSquads: []string{"sq1", "sq2"}},
	}
	a.botCfg.NormalizePricing()
	a.infraCache = &infraCacheEntry{
		fetchedAt: time.Now(),
		squads: []remnawave.SquadFull{
			{UUID: "sq1", InboundsCount: 1, InboundUUIDs: []string{"ib1"}},
			{UUID: "sq2", InboundsCount: 1, InboundUUIDs: []string{"ib2"}},
		},
		hosts: []remnawave.Host{
			// Инбаунд есть только у sq1, а доступ разрешён только скваду sq2.
			{Remark: "🇩🇪 Германия", InboundUUID: "ib1",
				SquadsMode: remnawave.SquadsModeAllowOnly, Squads: []string{"sq2"}},
			// А этот хост совпадает по обоим признакам на sq2.
			{Remark: "🇳🇱 Нидерланды", InboundUUID: "ib2",
				SquadsMode: remnawave.SquadsModeAllowOnly, Squads: []string{"sq2"}},
		},
	}
	cs, _ := a.planCountries(context.Background(), 1)
	if len(cs) != 1 || cs[0].Code != "nl" {
		t.Fatalf("countries=%+v want только NL", cs)
	}
}

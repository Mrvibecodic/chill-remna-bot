package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// lastKBCallbacks — callback'и последней отправленной клавиатуры.
func (f *fakeMsg) lastKBCallbacks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, row := range f.lastKB {
		for _, b := range row {
			if b.CallbackData != "" {
				out = append(out, b.CallbackData)
			}
		}
	}
	return out
}

// lastKBLabels — подписи кнопок последней отправленной клавиатуры.
func (f *fakeMsg) lastKBLabels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, row := range f.lastKB {
		for _, b := range row {
			out = append(out, b.Text)
		}
	}
	return out
}

// Снятый с продажи срок (стёрта базовая цена) остаётся в тарифе ради своих
// переопределений, но в списке тарифов «сроков: N» его считать нельзя: админ
// снял 3 месяца, а список продолжал показывать два срока.
func TestPlansAdmin_ListCountsOnlyDurationsOnSale(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	label := func() string {
		t.Helper()
		planTap(t, a, "menu:plans")
		for _, l := range fm.lastKBLabels() {
			if strings.Contains(l, "Базовый") {
				return l
			}
		}
		t.Fatalf("в списке нет «Базового»: %v", fm.lastKBLabels())
		return ""
	}
	if l := label(); !strings.Contains(l, "сроков: 3") {
		t.Fatalf("до снятия срока: %q, ожидалось «сроков: 3»", l)
	}

	if err := a.setPlanPrice(ctx, "", 3, "base", ""); err != nil {
		t.Fatal(err)
	}
	p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	if d := p.Duration(3); d == nil || d.Base != "" {
		t.Fatalf("срок 3 мес должен остаться в тарифе без базовой цены: %+v", d)
	}
	if l := label(); !strings.Contains(l, "сроков: 2") {
		t.Fatalf("после снятия срока 3 мес: %q, ожидалось «сроков: 2»", l)
	}
}

// Тариф, у которого все сроки сняты с продажи, включить нельзя: витрина его
// не покажет, а в списке он стоял бы с ✅, как продающийся.
func TestPlansAdmin_CannotEnablePlanWithoutPricedDuration(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	planTap(t, a, "pln:newk:t")
	list, _ := fs.ListPlans(ctx)
	code := list[0].Code

	// Переопределение держит срок в тарифе и после того, как базовую цену
	// стёрли, — ровно случай «срок есть, но не продаётся».
	if err := a.setPlanPrice(ctx, code, 1, "base", "100"); err != nil {
		t.Fatal(err)
	}
	if err := a.setPlanPrice(ctx, code, 1, "p2p", "90"); err != nil {
		t.Fatal(err)
	}
	if err := a.setPlanPrice(ctx, code, 1, "base", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := fs.GetPlan(ctx, code); len(p.Durations) != 1 {
		t.Fatalf("срок без базовой цены должен остаться в тарифе: %+v", p.Durations)
	}

	planTap(t, a, "pln:toggle:"+code)
	if p, _ := fs.GetPlan(ctx, code); p.Enabled {
		t.Fatal("включён тариф без единого срока с ценой")
	}
	if !strings.Contains(fm.last(), "включать нечего") {
		t.Fatalf("причина отказа не показана: %q", fm.last())
	}

	// Вернули цену — включается.
	if err := a.setPlanPrice(ctx, code, 1, "base", "100"); err != nil {
		t.Fatal(err)
	}
	planTap(t, a, "pln:toggle:"+code)
	if p, _ := fs.GetPlan(ctx, code); !p.Enabled {
		t.Fatal("тариф со сроком с ценой не включился")
	}
}

func TestPlanDurationsOnSale(t *testing.T) {
	p := &model.Plan{Durations: []model.PlanDuration{
		{Months: 1, Base: "100"},
		{Months: 3, P2P: "250"}, // снят с продажи, переопределение осталось
		{Months: 6, Base: "500"},
		{Days: 7, Base: "50"}, // срок в днях витрина не продаёт
	}}
	if n := planDurationsOnSale(p); n != 2 {
		t.Fatalf("planDurationsOnSale = %d, want 2", n)
	}
}

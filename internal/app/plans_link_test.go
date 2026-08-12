package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// Карточка тарифа показывает условия (трафик, устройства) и значок: заданный в
// админке — как есть, без него — дефолтную коробку.
func TestPlanCard_TermsAndIcon(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)

	uid := int64(830)
	a.handleCallback(ctx, cb(uid, "plo:"+p.Code))
	last := fm.last()
	if !strings.Contains(last, "📦") {
		t.Fatalf("без значка карточка должна показывать коробку: %q", last)
	}
	if !strings.Contains(last, "Трафик: безлимит") {
		t.Fatalf("нет строки трафика: %q", last)
	}
	if !strings.Contains(last, "Устройства: до 7") {
		t.Fatalf("нет строки устройств: %q", last)
	}

	p.Icon = "💎"
	if err := fs.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	a.handleCallback(ctx, cb(uid, "plo:"+p.Code))
	last = fm.last()
	if !strings.Contains(last, "💎") {
		t.Fatalf("значок из админки не показан: %q", last)
	}
	if strings.Contains(last, "📦") {
		t.Fatalf("при заданном значке коробки быть не должно: %q", last)
	}
}

// Условия, различающиеся по срокам, выводятся с разбивкой; одинаковые — одной
// строкой; тариф без единого своего лимита устройств строку устройств прячет.
func TestPlanTermsText(t *testing.T) {
	gb, dev := 100, 5
	p := &model.Plan{
		Strategy: "MONTH", DeviceLimit: 7,
		Durations: []model.PlanDuration{
			{Months: 1, Base: "150", DeviceLimit: &dev},
			{Months: 12, Base: "1400", TrafficGB: &gb},
		},
	}
	got := planTermsText("ru", p)
	if !strings.Contains(got, "1 мес — безлимит") || !strings.Contains(got, "12 мес — 100 ГБ в месяц") {
		t.Fatalf("нет разбивки трафика по срокам: %q", got)
	}
	if !strings.Contains(got, "1 мес — до 5") || !strings.Contains(got, "12 мес — до 7") {
		t.Fatalf("нет разбивки устройств по срокам: %q", got)
	}

	p.Strategy = "NO_RESET"
	p.Durations[0].TrafficGB = &gb
	if got = planTermsText("ru", p); !strings.Contains(got, "Трафик: 100 ГБ на весь срок") {
		t.Fatalf("одинаковый трафик должен быть одной строкой со стратегией: %q", got)
	}

	// Сроки без своих лимитов устройств: дефолт панели, числа нет — строки нет.
	none := &model.Plan{Durations: []model.PlanDuration{{Months: 1, Base: "150"}}}
	if got = planTermsText("ru", none); strings.Contains(got, "Устройства") {
		t.Fatalf("без лимита устройств строки быть не должно: %q", got)
	}
	if !strings.Contains(got, "Трафик: безлимит") {
		t.Fatalf("трафик без лимита — безлимит: %q", got)
	}

	// Непродаваемых сроков нет — блока нет.
	if got = planTermsText("ru", &model.Plan{}); got != "" {
		t.Fatalf("пустой тариф не должен давать условий: %q", got)
	}
}

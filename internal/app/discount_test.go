package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func TestPlanDiscount(t *testing.T) {
	dur := func(m int, base string) model.PlanDuration { return model.PlanDuration{Months: m, Base: base} }
	cases := []struct {
		name   string
		ds     []model.PlanDuration
		months int
		want   int
	}{
		// 4 $ в месяц, 10 $ за три: 16,7 % — вниз до 16.
		{"вниз", []model.PlanDuration{dur(1, "4"), dur(3, "10")}, 3, 16},
		{"год", []model.PlanDuration{dur(1, "4"), dur(3, "10"), dur(12, "30")}, 12, 37},
		{"база без скидки", []model.PlanDuration{dur(1, "4"), dur(3, "10")}, 1, 0},
		{"дороже за месяц", []model.PlanDuration{dur(1, "4"), dur(3, "13")}, 3, 0},
		{"ровно по ставке", []model.PlanDuration{dur(1, "4"), dur(3, "12")}, 3, 0},
		{"меньше процента", []model.PlanDuration{dur(1, "100"), dur(3, "299")}, 3, 0},
		{"копейки", []model.PlanDuration{dur(1, "4.99"), dur(3, "12,99")}, 3, 13},
		// Нет срока «1 мес» — база самый короткий: 3 мес за 900 → 300 в месяц.
		{"без месяца", []model.PlanDuration{dur(12, "3000"), dur(3, "900")}, 12, 16},
		{"месяц не первым", []model.PlanDuration{dur(3, "10"), dur(1, "4")}, 3, 16},
		{"снятый с продажи месяц не база", []model.PlanDuration{dur(1, ""), dur(3, "900"), dur(6, "1500")}, 6, 16},
		{"нечисловая цена", []model.PlanDuration{dur(1, "4"), dur(3, "abc")}, 3, 0},
		{"нечисловая база", []model.PlanDuration{dur(1, "abc"), dur(3, "900"), dur(6, "1500")}, 6, 16},
	}
	for _, c := range cases {
		p := &model.Plan{Durations: c.ds}
		got := planDiscount(p, p.Duration(c.months))
		if got != c.want {
			t.Errorf("%s: скидка %d, ожидалась %d", c.name, got, c.want)
		}
	}
}

// Кнопки сроков в чате: у длинного срока — приписка скидки, у месяца — нет;
// убранный в «Текстах бота» текст скидку прячет, свой — меняет слово.
func TestStorefrontDiscountLabel(t *testing.T) {
	a, fm, p := storefrontApp(t)
	const uid = int64(1000000001)
	offer := func() string {
		n := len(fm.buttonLabels())
		a.handleCallback(context.Background(), cb(uid, "plo:"+p.Code))
		return strings.Join(fm.buttonLabels()[n:], "|")
	}

	got := offer()
	if !strings.Contains(got, "12 мес — 9900 ₽ (Скидка 16%)") {
		t.Fatalf("нет скидки у года: %q", got)
	}
	if strings.Contains(got, "1 мес — 990 ₽ (") {
		t.Fatalf("у месяца скидки быть не должно: %q", got)
	}

	if c, _ := i18n.Canonicalize("buy.discount", "выгода {скидка}%"); c != "выгода {discount}%" {
		t.Fatalf("русское имя переменной не распознано: %q", c)
	}
	if rej := i18n.SetOverrides(map[string]map[string]string{"ru": {"buy.discount": "выгода {discount}%"}}); rej != nil {
		t.Fatalf("свой текст не принят: %v", rej)
	}
	if got := offer(); !strings.Contains(got, "12 мес — 9900 ₽ выгода 16%") {
		t.Fatalf("свой текст скидки не применён: %q", got)
	}

	if rej := i18n.SetOverrides(map[string]map[string]string{"ru": {"buy.discount": ""}}); rej != nil {
		t.Fatalf("пустой текст не принят: %v", rej)
	}
	got = offer()
	if strings.Contains(got, "9900 ₽ ") || !strings.Contains(got, "12 мес — 9900 ₽") {
		t.Fatalf("убранная скидка осталась на кнопке: %q", got)
	}
}

// Мини-апп: скидка приходит готовой подписью; тумблер «Выгодно» снимает
// отметку, скидку не трогает.
func TestMiniPlansDiscountAndBestToggle(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(i18n.ResetOverrides)
	a, fs := planApp(t)
	vip := vipPlan(t, fs, model.PlanAvailAll)
	year := func() (best bool, disc string) {
		for _, p := range a.MiniPlans(ctx, 555).Plans {
			if p.Code != vip.Code {
				continue
			}
			for _, d := range p.Durations {
				if d.Months == 12 {
					return d.Best, d.Discount
				}
				if d.Discount != "" {
					t.Fatalf("скидка у срока %d: %q", d.Months, d.Discount)
				}
			}
		}
		t.Fatal("года VIP нет на витрине")
		return
	}
	if best, disc := year(); !best || disc != "(Скидка 16%)" {
		t.Fatalf("год: best=%v discount=%q", best, disc)
	}
	a.botCfg.WebUI.HideBest = true
	if best, disc := year(); best || disc != "(Скидка 16%)" {
		t.Fatalf("плашка выключена: best=%v discount=%q", best, disc)
	}
}

// Тумблер в «Дизайне мини-аппа и кабинета» переключает и сохраняет плашку.
func TestWebUIBestToggle(t *testing.T) {
	a, fm, _ := storefrontApp(t)
	planTap(t, a, cbWebUI+":best")
	if !a.botCfg.WebUI.HideBest {
		t.Fatal("плашка не выключилась")
	}
	if !strings.Contains(strings.Join(fm.buttonLabels(), "|"), "Плашка «Выгодно»: выкл") {
		t.Fatalf("тумблер не показывает состояние: %q", fm.buttonLabels())
	}
	planTap(t, a, cbWebUI+":best")
	if a.botCfg.WebUI.HideBest {
		t.Fatal("плашка не включилась обратно")
	}
}

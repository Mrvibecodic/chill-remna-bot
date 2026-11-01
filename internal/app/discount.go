package app

import (
	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// planDiscount — скидка срока d в процентах относительно цены месяца тарифа.
// База — самый короткий продаваемый срок (обычно «1 мес»). Процент
// округляется вниз: витрина не обещает больше, чем есть. 0 — скидки нет: это
// сама база, срок выходит дороже за месяц, скидка меньше 1 % или цена не
// разбирается числом.
func planDiscount(p *model.Plan, d *model.PlanDuration) int {
	if p == nil || d == nil || d.Months <= 0 {
		return 0
	}
	priceK, ok := rubToKopecks(d.Base)
	if !ok || priceK <= 0 {
		return 0
	}
	baseM, baseK := 0, int64(0)
	for i := range p.Durations {
		x := &p.Durations[i]
		if x.Months <= 0 || x.Base == "" {
			continue
		}
		k, ok := rubToKopecks(x.Base)
		if !ok || k <= 0 {
			continue
		}
		if baseM == 0 || x.Months < baseM {
			baseM, baseK = x.Months, k
		}
	}
	if baseM == 0 || d.Months <= baseM {
		return 0
	}
	// Цена срока по ставке базы — baseK·m/baseM; считаем без деления, чтобы
	// не терять копейки: (full − price)/full = (baseK·m − priceK·baseM)/(baseK·m).
	full := baseK * int64(d.Months)
	diff := full - priceK*int64(baseM)
	if diff <= 0 {
		return 0
	}
	return int(diff * 100 / full)
}

// discountLabel — подпись скидки срока для витрины; пусто — показывать нечего
// (нет скидки или админ убрал текст в «Текстах бота»).
func discountLabel(lang string, p *model.Plan, d *model.PlanDuration) string {
	pct := planDiscount(p, d)
	if pct < 1 {
		return ""
	}
	return i18n.T(lang, "buy.discount", pct)
}

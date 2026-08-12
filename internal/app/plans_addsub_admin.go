package app

import (
	"context"
	"html"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Экран «Доп-подписка» карточки тарифа: режим (наследовать/вкл/выкл) и свои
// название с описанием. Режим и тексты — оформление тарифа: пересборка
// «Базового» из сетки их сохраняет (basePlanFrom), from_config не снимается.
// Что именно продано покупателю, фиксирует снимок сделки в момент счёта.

// addSubModeToken/addSubModeFromToken — режим в callback-данных: пустую строку
// («наследовать») в callback не положить.
func addSubModeToken(mode string) string {
	if model.NormalizeAddSubMode(mode) == model.PlanAddSubInherit {
		return "inh"
	}
	return model.NormalizeAddSubMode(mode)
}

func addSubModeFromToken(tok string) string {
	if tok == "inh" {
		return model.PlanAddSubInherit
	}
	return model.NormalizeAddSubMode(tok)
}

// addSubModeName — короткое имя режима для кнопок и карточки.
func addSubModeName(lang, mode string) string {
	switch model.NormalizeAddSubMode(mode) {
	case model.PlanAddSubOn:
		return i18n.T(lang, "plans.as_on")
	case model.PlanAddSubOff:
		return i18n.T(lang, "plans.as_off")
	}
	return i18n.T(lang, "plans.as_inherit")
}

// showPlanAddSub — экран опции доп-подписки тарифа: pln:as:<код>.
func (a *App) showPlanAddSub(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p, err := a.planByCode(ctx, code)
	if err != nil || p == nil {
		a.planEditFailed(ctx, chatID, code, planGoneOr(err))
		return
	}
	_, infraOn, _, _, _ := a.addSubParams()

	name, desc := a.addSubTexts(lang, p)
	if desc == "" {
		desc = i18n.T(lang, "admin.none")
	}
	sold := i18n.T(lang, "plans.as_sold_no")
	if a.planAddSubOn(p) {
		sold = i18n.T(lang, "plans.as_sold_yes")
	}
	var b strings.Builder
	b.WriteString(i18n.T(lang, "plans.addsub_title",
		planTitleHTML(lang, p), addSubModeName(lang, p.AddSub), sold,
		html.EscapeString(name), html.EscapeString(desc)))
	if !infraOn {
		b.WriteString("\n\n")
		b.WriteString(i18n.T(lang, "plans.addsub_infra_off"))
	}

	cur := model.NormalizeAddSubMode(p.AddSub)
	var rows [][]models.InlineKeyboardButton
	for _, m := range []string{model.PlanAddSubInherit, model.PlanAddSubOn, model.PlanAddSubOff} {
		label := addSubModeName(lang, m)
		if m == cur {
			label = "✅ " + label
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "pln:asm:"+addSubModeToken(m)+":"+code)})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{
			btn(i18n.T(lang, "plans.btn_addsub_name"), "pln:asn:"+code),
			btn(i18n.T(lang, "plans.btn_addsub_desc"), "pln:asd:"+code),
		},
		navBack(lang, "pln:open:"+code),
	)
	a.sendPayKB(ctx, chatID, b.String(), rows)
}

// setPlanAddSubMode — выбор режима: pln:asm:<режим>:<код>.
func (a *App) setPlanAddSubMode(ctx context.Context, chatID int64, code, tok string) {
	mode := addSubModeFromToken(tok)
	if _, err := a.editPlan(ctx, code, func(p *model.Plan) error {
		p.AddSub = mode
		return nil
	}); err != nil {
		a.planEditFailed(ctx, chatID, code, err)
		return
	}
	a.showPlanAddSub(ctx, chatID, code)
}

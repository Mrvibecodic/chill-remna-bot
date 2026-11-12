package app

import (
	"context"
	"html"
	"strconv"
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
	b.WriteString("\n\n")
	b.WriteString(i18n.T(lang, "plans.addsub_terms",
		a.planAddSubTrafficLabel(lang, p), html.EscapeString(a.planAddSubSquadsLabel(ctx, lang, p))))
	if !infraOn {
		b.WriteString("\n\n")
		b.WriteString(i18n.T(lang, "plans.addsub_infra_off"))
	}
	if warn := a.planAddSubSquadsWarning(ctx, p); warn != "" {
		b.WriteString("\n\n")
		b.WriteString(i18n.T(lang, warn))
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
		[]models.InlineKeyboardButton{
			btn(i18n.T(lang, "plans.btn_addsub_gb"), "pln:ast:"+code),
			btn(i18n.T(lang, "plans.btn_addsub_squads"), "pln:asq:"+code),
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

// addSubMaxGB — верхняя граница трафика доп-подписки: больше в байтах не
// влезает разумно, а опечатка с лишними нулями переполнила бы int64.
const addSubMaxGB = 1_000_000

// planAddSubTrafficLabel — трафик доп-подписки тарифа для экрана: свой или
// «как в общих» с тем, что сейчас стоит в общих настройках.
func (a *App) planAddSubTrafficLabel(lang string, p *model.Plan) string {
	gbLabel := func(gb int) string {
		if gb <= 0 {
			return i18n.T(lang, "addsub.unlimited")
		}
		return strconv.Itoa(gb) + " GB"
	}
	if p.AddSubTrafficGB != nil && *p.AddSubTrafficGB >= 0 {
		return gbLabel(*p.AddSubTrafficGB)
	}
	a.mu.Lock()
	gb := 0
	if a.botCfg != nil {
		gb = a.botCfg.AddSub.TrafficGB
	}
	a.mu.Unlock()
	return i18n.T(lang, "plans.as_common", gbLabel(gb))
}

// planAddSubSquadsLabel — сквады доп-подписки тарифа для экрана.
func (a *App) planAddSubSquadsLabel(ctx context.Context, lang string, p *model.Plan) string {
	if len(p.AddSubSquads) > 0 {
		names, _ := a.squadNames(ctx, p.AddSubSquads, "")
		return names
	}
	a.mu.Lock()
	var common []string
	if a.botCfg != nil {
		common = append([]string(nil), a.botCfg.AddSub.InternalSquads...)
	}
	a.mu.Unlock()
	names, _ := a.squadNames(ctx, common, "")
	if names == "" {
		names = i18n.T(lang, "admin.none")
	}
	return i18n.T(lang, "plans.as_common", names)
}

// applyPlanAddSubGB принимает трафик доп-подписки тарифа: число ГБ (0 —
// безлимит) или прочерк — «как в общих».
func (a *App) applyPlanAddSubGB(ctx context.Context, chatID int64, text string) {
	ui := a.getUI(chatID)
	code := ui.planCode
	ui.adminInput = ""
	ui.planCode = ""
	lang := a.lang(chatID)
	v := strings.TrimSpace(text)
	var traffic *int
	if v != "-" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > addSubMaxGB {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.addsub_bad_gb", addSubMaxGB),
				[][]models.InlineKeyboardButton{navBack(lang, "pln:as:"+code)})
			return
		}
		traffic = &n
	}
	if _, err := a.editPlan(ctx, code, func(p *model.Plan) error {
		p.AddSubTrafficGB = traffic
		return nil
	}); err != nil {
		a.planEditFailed(ctx, chatID, code, err)
		return
	}
	a.showPlanAddSub(ctx, chatID, code)
}

// showPlanAddSubSquads — свои сквады доп-подписки тарифа: pln:asq:<код>.
// Ничего не отмечено — как в общих настройках.
func (a *App) showPlanAddSubSquads(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	a.getUI(chatID).planEdit = p.Code
	back := "pln:as:" + p.Code
	panel := a.panelClient()
	if panel == nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "squads.no_panel"),
			[][]models.InlineKeyboardButton{navBack(lang, back)})
		return
	}
	intSquads, err := panel.ListSquads(ctx)
	if err != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "squads.err", err.Error()),
			[][]models.InlineKeyboardButton{navBack(lang, back)})
		return
	}
	live := panel.LiveSquads(ctx)
	sel := p.AddSubSquads
	isSel := func(uuid string) bool {
		for _, u := range sel {
			if u == uuid {
				return true
			}
		}
		return false
	}
	h := planEditHash(p.Code)
	rows := make([][]models.InlineKeyboardButton, 0, len(intSquads)+3)
	for _, sq := range intSquads {
		mark := "⬜"
		if isSel(sq.UUID) {
			mark = "✅"
		}
		rows = append(rows, []models.InlineKeyboardButton{
			btn(mark+" 🏠 "+sq.Name, "plq:a:0:"+h+":"+sq.UUID),
		})
	}
	gone := goneSquadRows(lang, live, sel, "",
		func(u string) string { return "plq:a:0:" + h + ":" + u }, nil)
	rows = append(rows, gone...)
	if len(sel) > 0 {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "plans.btn_addsub_sq_common"), "pln:asqc:"+p.Code),
		})
	}
	rows = append(rows, navBack(lang, back))
	state := i18n.T(lang, "plans.as_sq_own")
	if len(sel) == 0 {
		state = i18n.T(lang, "plans.as_sq_common")
	}
	a.sendPayKB(ctx, chatID,
		i18n.T(lang, "plans.addsub_sq_title", planTitleHTML(lang, p), state)+goneSquadsHint(lang, gone), rows)
}

// togglePlanAddSubSquad переключает сквад доп-подписки тарифа; заодно из
// набора уходят сквады, удалённые в панели. Пустой набор — как в общих.
func (a *App) togglePlanAddSubSquad(ctx context.Context, code, uuid string) error {
	if uuid == "" {
		return nil
	}
	live := a.liveSquads(ctx)
	_, err := a.editPlan(ctx, code, func(p *model.Plan) error {
		next, _ := pruneGoneSquads(live, toggleString(p.AddSubSquads, uuid), "")
		p.AddSubSquads = next
		return nil
	})
	return err
}

// clearPlanAddSubSquads возвращает тарифу общие сквады доп-подписки.
func (a *App) clearPlanAddSubSquads(ctx context.Context, code string) error {
	_, err := a.editPlan(ctx, code, func(p *model.Plan) error {
		p.AddSubSquads = nil
		return nil
	})
	return err
}

// planAddSubSquadsWarning — ключ предупреждения о сквадах доп-подписки
// тарифа: свои сквады все удалены в панели (покупатели получат общие) или
// своих нет, а общие не выбраны (доп-подписка будет без серверов). Пусто —
// предупреждать не о чем.
func (a *App) planAddSubSquadsWarning(ctx context.Context, p *model.Plan) string {
	if !a.planAddSubOn(p) {
		return ""
	}
	if len(p.AddSubSquads) > 0 {
		if kept, _ := a.liveSquads(ctx).KeepInt(p.AddSubSquads); len(kept) == 0 {
			return "plans.addsub_sq_all_gone"
		}
		return ""
	}
	a.mu.Lock()
	common := 0
	if a.botCfg != nil {
		common = len(a.botCfg.AddSub.InternalSquads)
	}
	a.mu.Unlock()
	if common == 0 {
		return "plans.addsub_sq_none"
	}
	return ""
}

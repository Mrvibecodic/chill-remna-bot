package app

import (
	"context"
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/rsimport"
)

// Связывание планов remnashop с тарифами бота: по выбору админа людям
// с подпиской плана записывается тариф — так же, как его записывает покупка
// или выдача, только условия остаются те, что в панели сейчас.

// rsLinkTarget — выбранный для плана тариф: код, "" — не выбран, LinkSkip.
func (a *App) rsLinkTarget(chatID, planID int64) (string, bool) {
	a.rsMu.Lock()
	defer a.rsMu.Unlock()
	d := a.rsDump[chatID]
	if d == nil {
		return "", false
	}
	return d.Links[planID], true
}

// rsLinkSet запоминает выбор. false — дампа уже нет или такого плана в нём нет.
func (a *App) rsLinkSet(chatID, planID int64, code string) bool {
	a.rsMu.Lock()
	defer a.rsMu.Unlock()
	d := a.rsDump[chatID]
	if d == nil || rsPlanIndex(d, planID) < 0 {
		return false
	}
	if d.Links == nil {
		d.Links = map[int64]string{}
	}
	d.Links[planID] = code
	return true
}

func rsPlanIndex(d *rsimport.Data, planID int64) int {
	for i := range d.Plans {
		if d.Plans[i].ID == planID {
			return i
		}
	}
	return -1
}

// rsLinked — сколько планов связано с существующим тарифом.
func rsLinked(d *rsimport.Data, plans map[string]*model.Plan) int {
	n := 0
	for _, p := range d.Plans {
		if plans[d.Links[p.ID]] != nil {
			n++
		}
	}
	return n
}

// rsPlanMap — тарифы бота по коду. Ошибку отдаёт наверх: пустой список
// вместо неё выглядел бы как «тарифы не выбраны», и выбор админа молча
// пропадал бы.
func (a *App) rsPlanMap(ctx context.Context) (map[string]*model.Plan, error) {
	list, err := a.planList(ctx)
	if err != nil {
		a.log.Warn("rsimport: тарифы не прочитаны", "err", err)
		return nil, err
	}
	out := make(map[string]*model.Plan, len(list))
	for i := range list {
		out[list[i].Code] = &list[i]
	}
	return out, nil
}

// rsStorageFail — экран сбоя базы на шагах связывания.
func (a *App) rsStorageFail(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.sendSysKB(ctx, chatID, i18n.T(lang, "err.storage"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "rsimp.btn_back_import"), "rsimp:pv")},
	})
}

// rsPreviewPlans — строка и кнопка связывания на экране предпросмотра.
func (a *App) rsPreviewPlans(ctx context.Context, lang string, d *rsimport.Data) (string, []models.InlineKeyboardButton) {
	if len(d.Plans) == 0 {
		return "", nil
	}
	plans, err := a.rsPlanMap(ctx)
	if err != nil {
		return i18n.T(lang, "err.storage"), []models.InlineKeyboardButton{btn(i18n.T(lang, "rsimp.btn_links_plain"), "rsimp:lk")}
	}
	a.rsMu.Lock()
	n := rsLinked(d, plans)
	a.rsMu.Unlock()
	return i18n.T(lang, "rsimp.preview_plans", len(d.Plans), n),
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "rsimp.btn_links", n, len(d.Plans)), "rsimp:lk")}
}

// showRSLinks — планы remnashop и выбранные для них тарифы.
func (a *App) showRSLinks(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	d := a.rsDumpPeek(chatID)
	if d == nil {
		a.showRSImport(ctx, chatID)
		return
	}
	plans, err := a.rsPlanMap(ctx)
	if err != nil {
		a.rsStorageFail(ctx, chatID)
		return
	}
	var lines []string
	var rows [][]models.InlineKeyboardButton
	for _, p := range d.Plans {
		code, _ := a.rsLinkTarget(chatID, p.ID)
		target, mark := i18n.T(lang, "rsimp.link_unset"), "❓"
		switch {
		case code == rsimport.LinkSkip:
			target, mark = i18n.T(lang, "rsimp.link_skip"), "✖️"
		case plans[code] != nil:
			target, mark = html.EscapeString(planTitle(lang, plans[code])), "✅"
		}
		gone := ""
		if p.Deleted {
			gone = i18n.T(lang, "rsimp.link_deleted")
		}
		lines = append(lines, i18n.T(lang, "rsimp.link_line", escapeName(p.Name), gone, p.Subs, target))
		rows = append(rows, []models.InlineKeyboardButton{
			btn(rsBtnLabel(mark+" ", p.Name), "rsimp:lp:"+strconv.FormatInt(p.ID, 10)),
		})
	}
	if d.TrialSubs > 0 {
		lines = append(lines, i18n.T(lang, "rsimp.link_trial", d.TrialSubs))
	}
	if d.NoPlanSubs > 0 {
		lines = append(lines, i18n.T(lang, "rsimp.link_noplan", d.NoPlanSubs))
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "rsimp.btn_back_import"), "rsimp:pv")})
	a.sendSysKB(ctx, chatID, i18n.T(lang, "rsimp.links", strings.Join(lines, "\n")), rows)
}

// showRSLinkPick — выбор тарифа бота для одного плана remnashop.
func (a *App) showRSLinkPick(ctx context.Context, chatID, planID int64) {
	lang := a.lang(chatID)
	d := a.rsDumpPeek(chatID)
	if d == nil {
		a.showRSImport(ctx, chatID)
		return
	}
	i := rsPlanIndex(d, planID)
	if i < 0 {
		a.showRSLinks(ctx, chatID)
		return
	}
	p := d.Plans[i]
	list, err := a.planList(ctx)
	if err != nil {
		a.log.Warn("rsimport: тарифы не прочитаны", "err", err)
		a.rsStorageFail(ctx, chatID)
		return
	}
	cur, _ := a.rsLinkTarget(chatID, planID)
	id := strconv.FormatInt(planID, 10)
	t := p.Terms
	terms := a.rsTerms(ctx, lang, &model.PlanSnapshot{IntSquads: t.Squads, ExtSquad: t.ExtSquad, TrafficGB: t.TrafficGB, DeviceLimit: t.Devices})
	var items []string
	var row []models.InlineKeyboardButton
	var rows [][]models.InlineKeyboardButton
	for j := range list {
		bp := &list[j]
		title := planTitle(lang, bp)
		items = append(items, i18n.T(lang, "rsimp.pick_item", html.EscapeString(title),
			a.rsTerms(ctx, lang, &model.PlanSnapshot{IntSquads: bp.IntSquads, ExtSquad: bp.ExtSquad, TrafficGB: bp.TrafficGB, DeviceLimit: bp.DeviceLimit})))
		label := title
		if bp.Code == cur {
			label = "✅ " + label
		}
		row = append(row, btn(label, "rsimp:ls:"+id+":"+bp.Code))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	skip := i18n.T(lang, "rsimp.btn_skip")
	if cur == rsimport.LinkSkip {
		skip = "✅ " + skip
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(skip, "rsimp:ls:"+id+":"+rsimport.LinkSkip)},
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "rsimp:lk")},
	)
	a.sendSysKB(ctx, chatID, i18n.T(lang, "rsimp.pick", escapeName(p.Name), p.Subs, terms, strings.Join(items, "\n")), rows)
}

// rsTerms — условия одной строкой: трафик, устройства, сквады.
func (a *App) rsTerms(ctx context.Context, lang string, s *model.PlanSnapshot) string {
	return strings.Join(a.snapTermsLines(ctx, lang, s), " · ")
}

// onRSLink — нажатия на экранах связывания (lk, lp:<план>, ls:<план>:<тариф>).
func (a *App) onRSLink(ctx context.Context, chatID int64, val string) {
	switch {
	case val == "lk":
		a.showRSLinks(ctx, chatID)
	case strings.HasPrefix(val, "lp:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(val, "lp:"), 10, 64)
		if err != nil {
			a.showRSLinks(ctx, chatID)
			return
		}
		a.showRSLinkPick(ctx, chatID, id)
	case strings.HasPrefix(val, "ls:"):
		rest := strings.TrimPrefix(val, "ls:")
		idStr, code, ok := strings.Cut(rest, ":")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if !ok || err != nil {
			a.showRSLinks(ctx, chatID)
			return
		}
		if code != rsimport.LinkSkip {
			plans, err := a.rsPlanMap(ctx)
			if err != nil {
				a.rsStorageFail(ctx, chatID)
				return
			}
			// Тариф могли удалить, пока экран висел: старая кнопка выбор не меняет.
			if plans[code] == nil {
				a.showRSLinkPick(ctx, chatID, id)
				return
			}
		}
		a.rsLinkSet(chatID, id, code)
		a.showRSLinks(ctx, chatID)
	}
}

// rsLink — что связывание сделало с одним человеком.
type rsLink struct {
	linked, listed, failed bool
}

// rsLinkUser записывает тариф человеку, у которого его в боте ещё нет.
// Снимок — как у сделки, но без цены и срока: условия те, что в панели, и
// зачёт при смене тарифа переносит дни один к одному. Под замком выдачи:
// покупка, проведённая в этот момент, своё запишет сама и не будет затёрта.
func (a *App) rsLinkUser(ctx context.Context, u *rsimport.User, p *model.Plan) rsLink {
	lk := &a.finalizeUserLk[extLockIndex(strconv.FormatInt(u.TelegramID, 10))]
	lk.Lock()
	cur, err := a.store.GetUser(ctx, u.TelegramID)
	if err != nil {
		lk.Unlock()
		a.log.Warn("rsimport: пользователь не прочитан для тарифа", "tg_id", u.TelegramID, "err", err)
		return rsLink{failed: true}
	}
	if cur == nil || cur.Snapshot != nil {
		lk.Unlock()
		return rsLink{}
	}
	s := u.Sub
	err = a.store.SetUserSnapshot(ctx, u.TelegramID, &model.PlanSnapshot{
		Code:        p.Code,
		Name:        p.Name,
		TrafficGB:   s.TrafficGB,
		DeviceLimit: s.Devices,
		Strategy:    s.Strategy,
		IntSquads:   s.Squads,
		ExtSquad:    s.ExtSquad,
	})
	lk.Unlock()
	if err != nil {
		a.log.Warn("rsimport: тариф не записан", "tg_id", u.TelegramID, "plan", p.Code, "err", err)
		return rsLink{failed: true}
	}
	// Тариф по списку: как и выдача, связывание добавляет человека в список —
	// иначе свой тариф был бы ему закрыт.
	switch a.grantListAccess(ctx, u.TelegramID, p.Code) {
	case grantListAdded:
		return rsLink{linked: true, listed: true}
	case grantListFailed:
		return rsLink{linked: true, failed: true}
	}
	return rsLink{linked: true}
}

// rsBtnLabel — подпись кнопки: префикс и имя из дампа, обрезанное так, чтобы
// кнопка во всю ширину влезла в телефон. Имя приходит из чужой базы, и его
// длина ничем не ограничена.
func rsBtnLabel(prefix, name string) string {
	name = strings.TrimSpace(name)
	label := prefix + name
	if btnWidth(models.InlineKeyboardButton{Text: label}) <= rowCap(1) {
		return label
	}
	// Во всю ширину помещается меньше сорока букв: длиннее резать незачем.
	r := []rune(clipRunes(name, 64, ""))
	for len(r) > 0 {
		r = r[:len(r)-1]
		label = prefix + strings.TrimSpace(string(r)) + "…"
		if btnWidth(models.InlineKeyboardButton{Text: label}) <= rowCap(1) {
			break
		}
	}
	return label
}

// rsApplyLinks записывает тарифы по выбору админа. Пробные подписки и
// подписки без плана не связываются; купившим в боте тариф не меняется.
func (a *App) rsApplyLinks(ctx context.Context, d *rsimport.Data, rep *rsReport) {
	if len(d.Links) == 0 {
		return
	}
	plans, err := a.rsPlanMap(ctx)
	for i := range d.Users {
		u := &d.Users[i]
		if u.Sub == nil || u.Sub.Trial || u.Sub.PlanID == rsimport.NoPlan {
			continue
		}
		code := d.Links[u.Sub.PlanID]
		if err != nil {
			// Тарифы не прочитаны: кому тариф выбран и записался бы, тот не
			// связан по сбою, а не «не выбран».
			if code != "" && code != rsimport.LinkSkip {
				if cur, gerr := a.store.GetUser(ctx, u.TelegramID); gerr != nil || cur != nil && cur.Snapshot == nil {
					rep.errs++
				}
			}
			continue
		}
		p := plans[code]
		if p == nil {
			continue
		}
		r := a.rsLinkUser(ctx, u, p)
		if r.linked {
			rep.linked++
		}
		if r.listed {
			rep.listed++
		}
		if r.failed {
			rep.errs++
		}
	}
}

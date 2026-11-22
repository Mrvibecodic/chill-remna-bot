package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func (a *App) grantSnap(ctx context.Context, g *grantSel) (*model.PlanSnapshot, *model.Plan, error) {
	if g.pack == "" {
		return a.grantSnapshot(ctx, g.code, g.months, g.days)
	}
	owner, pk, err := a.trafficDeal(ctx, g.uid, g.code, g.pack)
	if err != nil {
		return nil, nil, err
	}
	if owner == nil {
		return nil, nil, errGrantPlanGone
	}
	return a.trafficSnapshotOf(owner, pk), owner, nil
}

func (a *App) grantPackRow(ctx context.Context, lang string, uid int64) []models.InlineKeyboardButton {
	owner := a.trafficOwner(ctx, uid)
	if owner == nil {
		return nil
	}
	for _, pk := range trafficTerms(owner).Packs {
		if packSells(&pk) {
			return []models.InlineKeyboardButton{btn(i18n.T(lang, "grant.btn_packs"), "usr:grk:"+strconv.FormatInt(uid, 10))}
		}
	}
	return nil
}

func (a *App) showGrantTrafficTerms(ctx context.Context, chatID, uid int64, p *model.Plan) {
	lang := a.lang(chatID)
	id := strconv.FormatInt(uid, 10)
	terms := ""
	if lines := a.snapTermsLines(ctx, lang, a.trafficSnapshotOf(p, nil)); len(lines) > 0 {
		terms = "\n\n" + strings.Join(lines, "\n")
	}
	a.sendUsrKB(ctx, chatID, i18n.T(lang, "grant.terms_title", planTitleHTML(lang, p), a.userLabelByID(ctx, uid), terms),
		[][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "grant.btn_eternal"), "usr:grt:"+id+":0")},
			{btn(i18n.T(lang, "grant.btn_other_plan"), "usr:gr:"+id), btn(i18n.T(lang, "btn.home"), "menu:home")},
		})
}

func (a *App) onGrantPack(ctx context.Context, chatID int64, arg string) {
	uidStr, packID, _ := strings.Cut(arg, ":")
	uid, _ := strconv.ParseInt(uidStr, 10, 64)
	if uid == 0 {
		return
	}
	lang := a.lang(chatID)
	if msg := a.grantRefusal(ctx, lang, uid); msg != "" {
		a.sendUsrKB(ctx, chatID, msg, [][]models.InlineKeyboardButton{navBack(lang, "usr:view:"+uidStr)})
		return
	}
	owner := a.trafficOwner(ctx, uid)
	if owner == nil {
		a.noticeThen(ctx, chatID, i18n.T(lang, "grant.plan_gone"), func() { a.showGrantPlans(ctx, chatID, uid) })
		return
	}
	if packID == "" {
		a.showGrantPacks(ctx, chatID, uid, owner)
		return
	}
	if !model.ValidPackID(packID) || !packSells(owner.Pack(packID)) {
		a.showGrantPacks(ctx, chatID, uid, owner)
		return
	}
	a.getUI(chatID).grant = &grantSel{uid: uid, code: owner.Code, eternal: true, pack: packID}
	a.showGrantConfirm(ctx, chatID, uid, "")
}

func (a *App) showGrantPacks(ctx context.Context, chatID, uid int64, owner *model.Plan) {
	lang := a.lang(chatID)
	id := strconv.FormatInt(uid, 10)
	var rows [][]models.InlineKeyboardButton
	for _, pk := range trafficTerms(owner).Packs {
		if packSells(&pk) {
			rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "grant.btn_pack", pk.GB), "usr:grk:"+id+":"+pk.ID)})
		}
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "grant.btn_other_plan"), "usr:gr:"+id), btn(i18n.T(lang, "btn.home"), "menu:home")})
	a.sendUsrKB(ctx, chatID, i18n.T(lang, "grant.packs_title", a.userLabelByID(ctx, uid), planTitleHTML(lang, owner), a.grantLimitNow(ctx, lang, uid, 0)), rows)
}

func (a *App) grantLimitNow(ctx context.Context, lang string, uid int64, add int) string {
	panel := a.panelClient()
	if panel == nil {
		return ""
	}
	pu, err := panel.FindByTelegramID(ctx, uid)
	if err != nil || pu == nil || pu.TrafficLimit <= 0 {
		return ""
	}
	if add <= 0 {
		return i18n.T(lang, "grant.limit_now", formatGB(pu.TrafficUsed), formatGB(pu.TrafficLimit))
	}
	return i18n.T(lang, "grant.limit_after", formatGB(pu.TrafficLimit), formatGB(pu.TrafficLimit+int64(add)*bytesPerGB), formatGB(pu.TrafficUsed))
}

func (a *App) showGrantConfirmTraffic(ctx context.Context, chatID int64, u *model.User, g *grantSel, snap *model.PlanSnapshot, p *model.Plan, note string) {
	lang := a.lang(chatID)
	id := strconv.FormatInt(u.TelegramID, 10)
	var b strings.Builder
	if note != "" {
		b.WriteString(note)
		b.WriteString("\n\n")
	}
	back := "usr:grp:" + id + ":" + planEditHash(g.code)
	if g.pack != "" {
		g.gb = snap.PackGB
		back = "usr:grk:" + id
		b.WriteString(i18n.T(lang, "grant.confirm_pack", a.userLabelFor(ctx, u), planTitleHTML(lang, p), snap.PackGB))
		if line := a.grantLimitNow(ctx, lang, u.TelegramID, snap.PackGB); line != "" {
			b.WriteString("\n")
			b.WriteString(line)
		}
		b.WriteString("\n\n")
		b.WriteString(i18n.T(lang, "grant.confirm_pack_note"))
	} else {
		b.WriteString(i18n.T(lang, "grant.confirm_title", a.userLabelFor(ctx, u), planTitleHTML(lang, p),
			i18n.T(lang, "grant.term_eternal"), a.grantNowLine(ctx, lang, u)))
		if lines := a.snapTermsLines(ctx, lang, snap); len(lines) > 0 {
			b.WriteString("\n\n")
			b.WriteString(strings.Join(lines, "\n"))
		}
		b.WriteString("\n\n")
		b.WriteString(i18n.T(lang, "grant.confirm_traffic"))
		for _, w := range a.grantWarnings(ctx, lang, u, p, snap) {
			b.WriteString("\n\n")
			b.WriteString(w)
		}
	}
	a.sendUsrKB(ctx, chatID, b.String(), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "grant.btn_ok"), "usr:grok:"+id+":"+g.nonce)},
		{btn(i18n.T(lang, "btn.back"), back), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

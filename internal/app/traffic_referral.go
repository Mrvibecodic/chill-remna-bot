package app

import (
	"context"
	"errors"
	"html"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func (a *App) trafficRefBonus(ctx context.Context, invitee int64, method, extID string, snap *model.PlanSnapshot) {
	if a.store == nil || !a.referralCfg().Enabled {
		return
	}
	ctx = context.WithoutCancel(ctx)
	p, err := a.planByCode(ctx, snap.Code)
	if err != nil || !p.IsTraffic() {
		return
	}
	t := trafficTerms(p)
	u, _ := a.store.GetUser(ctx, invitee)
	if u == nil || u.ReferredBy == 0 {
		return
	}
	name := escapeName(snapTitle(snap))
	if t.RefForInvitee() {
		if err := a.growTraffic(ctx, invitee, snap.Code, t.RefGB); err != nil {
			a.refGBFailed(ctx, method, extID, invitee, invitee, t.RefGB, err)
		} else {
			a.payLog(ctx, method, extID, invitee, "ref_gb", "приглашённому +%d GB", t.RefGB)
			a.notify(ctx, invitee, i18n.T(a.lang(invitee), "ref.gb_got_invitee", name, t.RefGB))
		}
	}
	if !t.RefForReferrer() {
		return
	}
	ref := u.ReferredBy
	err = a.growTraffic(ctx, ref, "", t.RefGB)
	switch {
	case err == nil:
		a.payLog(ctx, method, extID, invitee, "ref_gb", "пригласившему %d +%d GB", ref, t.RefGB)
		a.notify(ctx, ref, i18n.T(a.lang(ref), "ref.gb_got_referrer", name, t.RefGB))
	case errors.Is(err, errPackNotOwner), errors.Is(err, errNoPanelUser), errors.Is(err, errPackUnlimited):
		a.payLog(ctx, method, extID, invitee, "ref_gb", "пригласившему %d не начислено: %v", ref, err)
		a.notify(ctx, ref, i18n.T(a.lang(ref), "ref.gb_skipped", name, t.RefGB))
	default:
		a.refGBFailed(ctx, method, extID, invitee, ref, t.RefGB, err)
	}
}

func (a *App) growTraffic(ctx context.Context, tgID int64, code string, gb int) error {
	unlock, _ := a.lockDeal(ctx, tgID, "", "")
	defer unlock()
	panel, pu, want, err := a.trafficGrowth(ctx, tgID, code, gb)
	if err != nil {
		return err
	}
	if err := panel.SetTrafficLimit(ctx, pu.Ref, want); err != nil && !a.bonusTrafficApplied(tgID, want) {
		return err
	}
	a.invalidateSubCache(tgID)
	return nil
}

func (a *App) refGBFailed(ctx context.Context, method, extID string, invitee, to int64, gb int, err error) {
	a.payLog(ctx, method, extID, invitee, "ref_gb_error", "%d: +%d GB не начислено: %v", to, gb, err)
	a.notify(ctx, a.cfg.AdminID, i18n.T(a.lang(a.cfg.AdminID), "admin.ref_gb_failed", gb, a.userLabelByID(ctx, to), a.userLabelByID(ctx, invitee), html.EscapeString(err.Error())))
}

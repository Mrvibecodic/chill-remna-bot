package app

import (
	"context"
	"strconv"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/web"
)

func (a *App) miniTrafficTerms(ctx context.Context, tgID int64, p *model.Plan, pc miniPlanCtxT, pd web.MiniPlanDTO) web.MiniPlanDTO {
	pd.TrafficNote = stripHTMLTags(i18n.T(pc.lang, "buy.traffic_note"))
	if a.userHasSub(ctx, tgID) {
		pd.ReplaceNote = stripHTMLTags(i18n.T(pc.lang, "buy.traffic_replace"))
	}
	t := trafficTerms(p)
	g := a.squadGeo(ctx, p.IntSquads)
	dv := planDevShown(p, nil, pc.hwid, pc.hwidOK)
	d := web.MiniDurationDTO{
		Price:        t.Price,
		Currency:     planCurrencyOr(p, pc.fallbackCur),
		Stars:        t.Stars,
		TrafficGB:    p.TrafficGB,
		Devices:      dv.n,
		DevicesUnlim: dv.unlim,
		Preferred:    true,
	}
	for _, c := range g.countries {
		d.Countries = append(d.Countries, web.MiniCountryDTO{Flag: c.Flag, Code: c.Code, Name: c.label(pc.geoLang)})
	}
	if i18n.T(pc.lang, "buy.servers", "1") != "" {
		d.Servers = g.hosts
	}
	pd.Durations = []web.MiniDurationDTO{d}
	return pd
}

func (a *App) miniTrafficSale(ctx context.Context, tgID int64, p *model.Plan, months int, pack string) *sale {
	if months != 0 {
		return nil
	}
	if pack == "" {
		if model.NormalizeAvailability(p.Availability) == model.PlanAvailLink && !a.linkSaleAllowed(ctx, tgID, p.Code) {
			return nil
		}
	} else if !model.ValidPackID(pack) {
		return nil
	}
	s, err := a.trafficSaleFor(ctx, tgID, &model.PurchaseIntent{PlanCode: p.Code, PackID: pack})
	if err != nil {
		return nil
	}
	return s
}

func (a *App) miniBalanceDiscr(ctx context.Context, tgID int64, s *sale) func() string {
	return func() string {
		if s.traffic() {
			if _, total, err := a.store.ListUserPayments(ctx, tgID, 1, 0); err == nil {
				return "n:" + strconv.Itoa(total)
			}
			return ""
		}
		if u, uerr := a.store.GetUser(ctx, tgID); uerr == nil && u != nil {
			return "exp:" + u.SubExpireAt
		}
		return "none"
	}
}

func (a *App) MiniTraffic(ctx context.Context, tgID int64) web.MiniTrafficDTO {
	lang := a.lang(tgID)
	owner := a.trafficOwner(ctx, tgID)
	if owner == nil {
		key := "traffic.not_owner"
		if a.onTrafficPlan(ctx, tgID) {
			key = "traffic.plan_gone"
		}
		return web.MiniTrafficDTO{Notice: stripHTMLTags(i18n.T(lang, key))}
	}
	dto := web.MiniTrafficDTO{Owner: true, Plan: planTitle(lang, owner), Code: owner.Code}
	if panel := a.panelClient(); panel != nil {
		if s, ok, err := panel.SubscriptionSnapshot(ctx, tgID); err == nil && ok && s.TrafficLimit > 0 {
			dto.LimitOK, dto.Used, dto.Limit = true, s.TrafficUsed, s.TrafficLimit
		}
	}
	cur := planCurrencyOr(owner, a.pricing().Currency)
	for _, pk := range trafficTerms(owner).Packs {
		if !packSells(&pk) {
			continue
		}
		dto.Packs = append(dto.Packs, web.MiniPackDTO{ID: pk.ID, GB: pk.GB, Price: pk.Price, Currency: cur, Stars: pk.Stars})
	}
	if len(dto.Packs) == 0 {
		dto.Notice = stripHTMLTags(i18n.T(lang, "mini.no_packs"))
	}
	return dto
}

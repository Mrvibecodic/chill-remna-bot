package app

import (
	"context"
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

const (
	cbTraffic      = "tfk"
	cbTrafficPacks = cbTraffic + ":pk"
)

func (a *App) trafficPriceLabel(p *model.Plan, price string, stars int) string {
	if strings.TrimSpace(price) != "" {
		return price + curSuffix(planCurrencyOr(p, a.pricing().Currency))
	}
	if stars > 0 {
		return strconv.Itoa(stars) + " ⭐"
	}
	return ""
}

func (a *App) onTraffic(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	switch action {
	case "pb":
		a.onTrafficPack(ctx, chatID, arg)
	case "buy":
		a.onTrafficBuy(ctx, chatID, arg)
	default:
		a.showTrafficPacks(ctx, chatID)
	}
}

func (a *App) showTrafficPacks(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	owner := a.trafficOwner(ctx, chatID)
	if owner == nil {
		key := "traffic.not_owner"
		if a.onTrafficPlan(ctx, chatID) {
			key = "traffic.plan_gone"
		}
		a.sendKB(ctx, chatID, i18n.T(lang, key), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.tariffs"), "menu:buy")},
			homeRow(lang),
		})
		return
	}
	left := a.trafficLeftText(ctx, chatID, lang, "traffic.left")
	var rows [][]models.InlineKeyboardButton
	for _, pk := range trafficTerms(owner).Packs {
		if !packSells(&pk) {
			continue
		}
		label := i18n.T(lang, "traffic.pack_btn", pk.GB, a.trafficPriceLabel(owner, pk.Price, pk.Stars))
		rows = append(rows, []models.InlineKeyboardButton{btn(label, cbTraffic+":pb:"+pk.ID)})
	}
	text := i18n.T(lang, "traffic.packs", left)
	if len(rows) == 0 {
		text = i18n.T(lang, "traffic.no_packs", left)
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:mysubs"), btn(i18n.T(lang, "btn.home"), "menu:home")})
	a.sendKBSection(ctx, chatID, assets.SectionBuySubscription, text, rows)
}

func (a *App) trafficLeftText(ctx context.Context, chatID int64, lang, key string) string {
	panel := a.panelClient()
	if panel == nil {
		return ""
	}
	s, ok, err := panel.SubscriptionSnapshot(ctx, chatID)
	if err != nil || !ok || s.TrafficLimit <= 0 {
		return ""
	}
	return i18n.T(lang, key, formatGB(max(s.TrafficLimit-s.TrafficUsed, 0)), formatGB(s.TrafficLimit))
}

func (a *App) onTrafficPack(ctx context.Context, chatID int64, id string) {
	lang := a.lang(chatID)
	var owner *model.Plan
	var pk *model.PlanPack
	if o := a.trafficOwner(ctx, chatID); o != nil {
		owner, pk, _ = a.trafficDeal(ctx, chatID, o.Code, id)
	}
	if owner == nil || pk == nil {
		a.noticeThen(ctx, chatID, i18n.T(lang, "traffic.pack_gone"), func() { a.showTrafficPacks(ctx, chatID) })
		return
	}
	s := &sale{Plan: owner, Pack: pk}
	if err := a.setBuyIntentTraffic(ctx, chatID, owner.Code, pk.ID, a.saleBase(s)); err != nil {
		a.log.Warn("намерение покупки пакета не сохранено", "err", err, "user", chatID)
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	a.showMethodsSale(ctx, chatID, s)
}

func (a *App) onTrafficBuy(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	if a.planLinkThrottled(chatID) || a.planLinkGlobalBlocked(ctx, chatID) {
		a.log.Warn("кнопка тарифа: лимит попыток", "user", chatID)
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.offer_gone"))
		return
	}
	if !model.ValidPlanCode(code) {
		a.planLinkFail(chatID)
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.offer_gone"))
		return
	}
	p, err := a.planByCode(ctx, code)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	if p == nil {
		a.planLinkFail(chatID)
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.offer_gone"))
		return
	}
	if !p.IsTraffic() || !p.Enabled || !trafficPlanSells(p) || !a.planAccessibleFor(ctx, p, chatID) {
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.offer_gone"))
		return
	}
	if a.trialLockNotice(ctx, chatID) {
		return
	}
	if a.legalRequired(ctx, chatID) {
		a.getUI(chatID).pendingPlanOffer = p.Code
		a.askLegal(ctx, chatID)
		return
	}
	s := &sale{Plan: p}
	if err := a.setBuyIntentTraffic(ctx, chatID, p.Code, "", a.saleBase(s)); err != nil {
		a.log.Warn("намерение покупки не сохранено", "err", err, "user", chatID)
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	a.showMethodsSale(ctx, chatID, s)
}

func (a *App) showTrafficOffer(ctx context.Context, chatID int64, p *model.Plan, view offerView) {
	lang := a.lang(chatID)
	t := trafficTerms(p)
	price := a.trafficPriceLabel(p, t.Price, t.Stars)
	if !trafficPlanSells(p) || price == "" {
		a.sendKB(ctx, chatID, i18n.T(lang, "buy.no_plans"), [][]models.InlineKeyboardButton{homeRow(lang)})
		return
	}
	rows := [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "buy.btn_traffic", price), cbTraffic+":buy:"+p.Code)}}
	if view.switchPlan {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "buy.btn_switch_plan"), "menu:buy")})
	}
	if view.promo {
		rows = append(rows, promoRow(lang))
	}
	if view.backToList {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:buy"), btn(i18n.T(lang, "btn.home"), "menu:home")})
	} else {
		rows = append(rows, homeRow(lang))
	}

	var b strings.Builder
	if view.note != "" {
		b.WriteString(view.note)
		b.WriteString("\n\n")
	}
	b.WriteString(i18n.T(lang, "plans.offer_title", planCardTitleHTML(lang, p)))
	if desc := strings.TrimSpace(p.Description); desc != "" {
		b.WriteString("\n\n")
		b.WriteString(html.EscapeString(desc))
	}
	g := a.squadGeo(ctx, p.IntSquads)
	servers := ""
	if g.hosts > 0 {
		servers = strconv.Itoa(g.hosts)
	}
	if line := geoLines(lang, g.countries, servers); line != "" {
		b.WriteString("\n\n")
		b.WriteString(line)
	}
	var terms []string
	if line := i18n.T(lang, "buy.traffic", i18n.T(lang, "buy.tr_traffic", p.TrafficGB)); line != "" {
		terms = append(terms, line)
	}
	if line := i18n.T(lang, "buy.term_eternal"); line != "" {
		terms = append(terms, line)
	}
	def, defOK := a.hwidDefaults(ctx)
	if dv := planDevShown(p, nil, def, defOK); dv.any() {
		if line := i18n.T(lang, "buy.devices", devicesValue(lang, dv)); line != "" {
			terms = append(terms, line)
		}
	}
	if len(terms) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(terms, "\n"))
	}
	if a.planAddSubOn(p) {
		name, desc := a.addSubTexts(lang, p)
		b.WriteString("\n\n")
		b.WriteString(i18n.T(lang, "buy.addsub_line", html.EscapeString(name)))
		if desc != "" {
			b.WriteString("\n")
			b.WriteString(html.EscapeString(desc))
		}
		if at := a.addSubTermsText(ctx, lang, p); at != "" {
			b.WriteString("\n")
			b.WriteString(at)
		}
	}
	if note := i18n.T(lang, "buy.traffic_note"); note != "" {
		b.WriteString("\n\n")
		b.WriteString(note)
	}
	if a.userHasSub(ctx, chatID) {
		if warn := i18n.T(lang, "buy.traffic_replace"); warn != "" {
			b.WriteString("\n\n")
			b.WriteString(warn)
		}
	}
	a.sendKBSection(ctx, chatID, assets.SectionBuySubscription, b.String(), rows)
}

func (a *App) buyTrafficRow(ctx context.Context, chatID int64) []models.InlineKeyboardButton {
	if a.trafficOwner(ctx, chatID) == nil || !a.userHasSub(ctx, chatID) {
		return nil
	}
	return []models.InlineKeyboardButton{btn(i18n.T(a.lang(chatID), "btn.buy_traffic"), cbTrafficPacks)}
}

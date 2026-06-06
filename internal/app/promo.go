package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func (a *App) showPromoUser(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.sendKBSection(ctx, chatID, assets.SectionPromoCode, i18n.T(lang, "promo.user_title"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "promo.btn_enter"), "pr:enter")},
		backHomeRow(lang),
	})
}

func (a *App) onPromoUser(ctx context.Context, chatID int64, val string) {
	if val == "enter" {
		a.getUI(chatID).awaitPromo = true
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "promo.ask"), "menu:promo")
	}
}

func (a *App) applyPromo(ctx context.Context, chatID int64, raw string) {
	lang := a.lang(chatID)
	code := strings.ToUpper(strings.TrimSpace(raw))
	if code == "" || a.store == nil {
		a.notify(ctx, chatID, i18n.T(lang, "promo.not_found"))
		return
	}
	p, _ := a.store.GetPromo(ctx, code)
	if p == nil {
		a.notify(ctx, chatID, i18n.T(lang, "promo.not_found"))
		return
	}
	if p.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, p.ExpiresAt); err == nil && time.Now().UTC().After(t) {
			a.notify(ctx, chatID, i18n.T(lang, "promo.expired"))
			return
		}
	}
	if p.MaxUses > 0 && p.Used >= p.MaxUses {
		a.notify(ctx, chatID, i18n.T(lang, "promo.exhausted"))
		return
	}
	if done, _ := a.store.PromoRedeemedBy(ctx, code, chatID); done {
		a.notify(ctx, chatID, i18n.T(lang, "promo.already"))
		return
	}
	switch p.Kind {
	case model.PromoKindDays:
		ok, found := a.addReferralDays(ctx, chatID, p.Value)
		if !found {
			a.notify(ctx, chatID, i18n.T(lang, "promo.need_sub"))
			return
		}
		if !ok {
			a.notify(ctx, chatID, i18n.T(lang, "promo.grant_fail"))
			return
		}
		_ = a.store.RedeemPromo(ctx, code, chatID)
		a.notify(ctx, chatID, i18n.T(lang, "promo.ok_days", p.Value))
	default:
		if err := a.store.AddBalance(ctx, chatID, int64(p.Value)*100); err != nil {
			a.notify(ctx, chatID, i18n.T(lang, "promo.grant_fail"))
			return
		}
		_ = a.store.RedeemPromo(ctx, code, chatID)
		a.notify(ctx, chatID, i18n.T(lang, "promo.ok_balance", p.Value))
	}
}

func (a *App) showPromoAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	var promos []model.PromoCode
	if a.store != nil {
		promos, _ = a.store.ListPromos(ctx)
	}
	var lines []string
	rows := [][]models.InlineKeyboardButton{}
	for _, p := range promos {
		kind := i18n.T(lang, "promoadm.kind_balance")
		if p.Kind == model.PromoKindDays {
			kind = i18n.T(lang, "promoadm.kind_days")
		}
		limit := "∞"
		if p.MaxUses > 0 {
			limit = strconv.Itoa(p.MaxUses)
		}
		exp := i18n.T(lang, "promoadm.no_expiry")
		if p.ExpiresAt != "" {
			exp = formatExpire(p.ExpiresAt, lang)
		}
		lines = append(lines, fmt.Sprintf("<code>%s</code> — %s %d · %d/%s · %s", p.Code, kind, p.Value, p.Used, limit, exp))
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "promoadm.btn_del", p.Code), "pr:del:"+p.Code)})
	}
	body := i18n.T(lang, "promoadm.empty")
	if len(lines) > 0 {
		body = strings.Join(lines, "\n")
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "promoadm.btn_add"), "pr:add")})
	rows = append(rows, navBack(lang, "menu:marketing"))
	a.sendKBSection(ctx, chatID, assets.SectionPromoCode, i18n.T(lang, "promoadm.title", body), rows)
}

func (a *App) onPromoAdmin(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	switch {
	case val == "add":
		a.getUI(chatID).adminInput = "promo_create"
		a.askInput(ctx, chatID, i18n.T(lang, "promoadm.ask_create"), "menu:promoadmin")
	case strings.HasPrefix(val, "del:"):
		if a.store != nil {
			_ = a.store.DeletePromo(ctx, strings.TrimPrefix(val, "del:"))
		}
		a.showPromoAdmin(ctx, chatID)
	}
}

func (a *App) createPromoFromText(ctx context.Context, chatID int64, text string) {
	lang := a.lang(chatID)
	f := strings.Fields(text)
	if len(f) < 3 {
		a.sendHome(ctx, chatID, i18n.T(lang, "promoadm.bad_format"))
		return
	}
	kind := strings.ToLower(f[1])
	if kind != model.PromoKindBalance && kind != model.PromoKindDays {
		a.sendHome(ctx, chatID, i18n.T(lang, "promoadm.bad_format"))
		return
	}
	value, _ := strconv.Atoi(f[2])
	if value <= 0 {
		a.sendHome(ctx, chatID, i18n.T(lang, "promoadm.bad_format"))
		return
	}
	maxUses := 0
	if len(f) >= 4 {
		maxUses, _ = strconv.Atoi(f[3])
	}
	expires := ""
	if len(f) >= 5 {
		if d, _ := strconv.Atoi(f[4]); d > 0 {
			expires = time.Now().UTC().Add(time.Duration(d) * 24 * time.Hour).Format(time.RFC3339)
		}
	}
	if a.store != nil {
		_ = a.store.CreatePromo(ctx, &model.PromoCode{Code: strings.ToUpper(f[0]), Kind: kind, Value: value, MaxUses: maxUses, ExpiresAt: expires})
	}
	a.showPromoAdmin(ctx, chatID)
}

package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func (a *App) botUsername(ctx context.Context) string {
	a.botUserMu.Lock()
	u := a.botUser
	a.botUserMu.Unlock()
	if u != "" {
		return u
	}
	if a.b == nil {
		return ""
	}
	me, err := a.b.GetMe(ctx)
	if err != nil || me == nil || me.Username == "" {
		return ""
	}
	a.botUserMu.Lock()
	a.botUser = me.Username
	a.botUserMu.Unlock()
	return me.Username
}

func (a *App) referralLink(ctx context.Context, chatID int64) string {
	u := a.botUsername(ctx)
	if u == "" {
		return ""
	}
	return fmt.Sprintf("https://t.me/%s?start=ref_%d", u, chatID)
}

func (a *App) referralCfg() model.ReferralConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.ReferralConfig{}
	}
	a.botCfg.NormalizeReferral()
	return a.botCfg.Referral
}

func (a *App) bindReferrer(ctx context.Context, chatID int64, payload string) {
	if a.store == nil || !a.referralCfg().Enabled {
		return
	}
	rest, ok := strings.CutPrefix(payload, "ref_")
	if !ok {
		return
	}
	ref, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
	if err != nil || ref == 0 || ref == chatID {
		return
	}
	if u, _ := a.store.GetUser(ctx, chatID); u != nil {
		return
	}
	if rr, _ := a.store.GetUser(ctx, ref); rr == nil {
		return
	}
	_ = a.store.UpsertUser(ctx, chatID)
	if err := a.store.SetReferredBy(ctx, chatID, ref); err != nil {
		return
	}
	if !a.referralCfg().OnFirstPay {
		a.payReferralBonus(ctx, chatID)
	}
}

func (a *App) grantReferralBonus(ctx context.Context, telegramID int64) {
	cfg := a.referralCfg()
	if !cfg.Enabled || !cfg.OnFirstPay {
		return
	}
	a.payReferralBonus(ctx, telegramID)
}

func (a *App) payReferralBonus(ctx context.Context, telegramID int64) {
	if a.store == nil {
		return
	}
	u, _ := a.store.GetUser(ctx, telegramID)
	if u == nil || u.ReferredBy == 0 || u.RefBonusPaid {
		return
	}
	cfg := a.referralCfg()
	if !cfg.Enabled || cfg.BonusValue <= 0 {
		return
	}
	ref := u.ReferredBy
	switch cfg.BonusKind {
	case model.ReferralBonusDays:
		if !a.addReferralDays(ctx, ref, cfg.BonusValue) {
			return
		}
		a.notify(ctx, ref, i18n.T(a.lang(ref), "ref.bonus_days", cfg.BonusValue))
	default:
		if err := a.store.AddBalance(ctx, ref, int64(cfg.BonusValue)*100); err != nil {
			return
		}
		a.notify(ctx, ref, i18n.T(a.lang(ref), "ref.bonus_balance", cfg.BonusValue))
	}
	_ = a.store.SetRefBonusPaid(ctx, telegramID)
}

func (a *App) addReferralDays(ctx context.Context, ref int64, days int) bool {
	a.mu.Lock()
	panel := a.panel
	limits := remnawave.UserLimits{}
	if a.botCfg != nil {
		limits.InternalSquads = a.botCfg.Plan.ActiveInternalSquads
		limits.ExternalSquad = a.botCfg.Plan.ExternalSquadUUID
		limits.Strategy = a.botCfg.Pricing.ResetStrategy()
	}
	a.mu.Unlock()
	if panel == nil {
		return false
	}
	_, expireAt, err := panel.CreateOrUpdateUserDays(ctx, ref, days, limits)
	if err != nil {
		return false
	}
	a.invalidateSubCache(ref)
	if a.store != nil {
		_ = a.store.SetSubExpiry(ctx, ref, expireAt, "paid")
	}
	return true
}

func (a *App) showReferral(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if !a.referralCfg().Enabled {
		a.sendKB(ctx, chatID, i18n.T(lang, "ref.disabled"), [][]models.InlineKeyboardButton{backHomeRow(lang)})
		return
	}
	cfg := a.referralCfg()
	link := a.referralLink(ctx, chatID)
	count := 0
	if a.store != nil {
		count, _ = a.store.CountReferrals(ctx, chatID)
	}
	bonus := i18n.T(lang, "ref.bonus_balance_n", cfg.BonusValue)
	if cfg.BonusKind == model.ReferralBonusDays {
		bonus = i18n.T(lang, "ref.bonus_days_n", cfg.BonusValue)
	}
	when := i18n.T(lang, "ref.when_pay")
	if !cfg.OnFirstPay {
		when = i18n.T(lang, "ref.when_reg")
	}
	text := i18n.T(lang, "ref.user", bonus, when, count, link)
	a.sendKBSection(ctx, chatID, assets.SectionReferral, text, [][]models.InlineKeyboardButton{backHomeRow(lang)})
}

func (a *App) showReferralAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.referralCfg()
	mark := func(b bool) string {
		if b {
			return "✅"
		}
		return "❌"
	}
	kind := i18n.T(lang, "refadm.kind_balance")
	if cfg.BonusKind == model.ReferralBonusDays {
		kind = i18n.T(lang, "refadm.kind_days")
	}
	when := i18n.T(lang, "refadm.when_pay")
	if !cfg.OnFirstPay {
		when = i18n.T(lang, "refadm.when_reg")
	}
	text := i18n.T(lang, "refadm.title", mark(cfg.Enabled), kind, cfg.BonusValue, when)
	a.sendKBSection(ctx, chatID, assets.SectionReferral, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "refadm.btn_toggle"), "ref:toggle")},
		{btn(i18n.T(lang, "refadm.btn_kind"), "ref:kind"), btn(i18n.T(lang, "refadm.btn_value"), "ref:value")},
		{btn(i18n.T(lang, "refadm.btn_when"), "ref:when")},
		navBack(lang, "menu:manage"),
	})
}

func (a *App) onReferralAdmin(ctx context.Context, chatID int64, val string) {
	switch val {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.NormalizeReferral()
			a.botCfg.Referral.Enabled = !a.botCfg.Referral.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showReferralAdmin(ctx, chatID)
	case "kind":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.NormalizeReferral()
			if a.botCfg.Referral.BonusKind == model.ReferralBonusDays {
				a.botCfg.Referral.BonusKind = model.ReferralBonusBalance
			} else {
				a.botCfg.Referral.BonusKind = model.ReferralBonusDays
			}
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showReferralAdmin(ctx, chatID)
	case "when":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.NormalizeReferral()
			a.botCfg.Referral.OnFirstPay = !a.botCfg.Referral.OnFirstPay
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showReferralAdmin(ctx, chatID)
	case "value":
		a.getUI(chatID).adminInput = "ref_value"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "refadm.ask_value"), "menu:refadmin")
	}
}

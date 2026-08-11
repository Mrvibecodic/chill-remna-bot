package app

import (
	"context"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

func (a *App) termsRequired(ctx context.Context, chatID int64) (string, bool) {
	a.mu.Lock()
	text := ""
	if a.botCfg != nil {
		text = a.botCfg.Contact.TermsText
	}
	a.mu.Unlock()
	if text == "" || a.store == nil {
		return "", false
	}
	u, err := a.store.GetUser(ctx, chatID)
	if err != nil || u == nil {

		return "", false
	}
	if u.TermsAcceptedAt != "" {
		return "", false
	}
	return text, true
}

func (a *App) askTerms(ctx context.Context, chatID int64, text string) {
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, i18n.T(lang, "terms.intro")+"\n\n"+text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "terms.btn_accept"), "terms:accept")},
		{btn(i18n.T(lang, "terms.btn_decline"), "terms:decline")},
	})
}

func (a *App) onTerms(ctx context.Context, chatID int64, val, firstName, username string) {
	switch val {
	case "accept":
		if a.store != nil {
			_ = a.store.SetTermsAccepted(ctx, chatID, time.Now().UTC().Format(time.RFC3339))
		}
		// Пришедшего по ссылке на тариф условия перехватили на его экране —
		// после согласия возвращаем туда же, а не на витрину «Базового», где
		// скрытого тарифа нет. Доступность перепроверит openPlanLink.
		ui := a.getUI(chatID)
		if code := ui.pendingPlanOffer; code != "" {
			ui.pendingPlanOffer = ""
			a.openPlanLink(ctx, chatID, code)
			return
		}
		a.showPlans(ctx, chatID)
	case "decline":
		isAdmin := chatID == a.cfg.AdminID
		a.getUI(chatID).pendingPlanOffer = ""
		a.enterHome(ctx, chatID, isAdmin, firstName, username)
	}
}

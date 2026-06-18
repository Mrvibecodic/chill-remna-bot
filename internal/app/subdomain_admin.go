package app

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

func (a *App) showSubdomain(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cur := a.subOverride()
	statusKey := "subdomain.off"
	if cur != "" {
		statusKey = "subdomain.on"
	}
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "subdomain.btn_change"), "subd:edit")},
	}
	if cur != "" {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "subdomain.btn_clear"), "subd:clear"),
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:system"),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	})

	display := cur
	if display == "" {
		display = i18n.T(lang, "admin.none")
	}
	a.sendSysKB(ctx, chatID, i18n.T(lang, "subdomain.title",
		i18n.T(lang, statusKey), display), rows)
}

func (a *App) askSubdomain(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.getUI(chatID).adminInput = "subdomain"
	a.getUI(chatID).priceMonths = 0
	a.sendKB(ctx, chatID, i18n.T(lang, "subdomain.ask"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.cancel"), "subd:cancel")},
	})
}

func (a *App) setSubdomain(ctx context.Context, chatID int64, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "-" || raw == "—" {
		raw = ""
	}

	host := extractHost(raw)
	if host == "" && raw != "" {
		host = raw
	}
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.SubscriptionDomain = host
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.getUI(chatID).adminInput = ""
	a.showSubdomain(ctx, chatID)
}

func (a *App) onSubdomain(ctx context.Context, chatID int64, val string) {
	switch val {
	case "edit":
		a.askSubdomain(ctx, chatID)
	case "clear":
		a.setSubdomain(ctx, chatID, "")
	case "cancel":
		a.getUI(chatID).adminInput = ""
		a.showSubdomain(ctx, chatID)
	}
}

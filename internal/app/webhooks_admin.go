package app

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

func (a *App) showWebhooksAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)

	a.mu.Lock()
	enabled := false
	addr := ":8080"
	base := ""
	rwSecret := ""
	domain := ""
	tls := false
	if a.botCfg != nil {
		enabled = a.botCfg.Webhook.Enabled
		if a.botCfg.Webhook.ListenAddr != "" {
			addr = a.botCfg.Webhook.ListenAddr
		}
		base = a.botCfg.Webhook.PublicBaseURL
		rwSecret = a.botCfg.Webhook.RemnawaveSecret
		domain = a.botCfg.Webhook.Domain
		tls = a.botCfg.Webhook.TLS
	}
	a.mu.Unlock()
	if tls && domain != "" {
		base = "https://" + domain
	}

	status := i18n.T(lang, "admin.off")
	if enabled {
		status = i18n.T(lang, "admin.on")
	}
	baseDisp := base
	if baseDisp == "" {
		baseDisp = i18n.T(lang, "admin.none")
	}
	secretDisp := i18n.T(lang, "admin.no")
	if rwSecret != "" {
		secretDisp = i18n.T(lang, "admin.yes")
	}

	urls := i18n.T(lang, "admin.none")
	if base != "" {
		urls = "<code>" + base + "/webhook/yookassa</code>\n" +
			"<code>" + base + "/webhook/cryptobot</code>\n" +
			"<code>" + base + "/webhook/remnawave</code>\n" +
			"<code>" + base + "/healthz</code>"
	}

	text := i18n.T(lang, "admin.webhooks_title", status, addr, baseDisp, secretDisp) + "\n\n" + urls

	pubLabel := i18n.T(lang, "wh.public_off")
	if tls {
		pubLabel = i18n.T(lang, "wh.public_on")
	}
	domainDisp := domain
	if domainDisp == "" {
		domainDisp = i18n.T(lang, "admin.none")
	}
	text += "\n\n" + i18n.T(lang, "wh.tls_block", pubLabel, domainDisp)
	a.sendKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "admin.btn_toggle"), "wh:toggle"), btn(i18n.T(lang, "admin.wh_btn_secret"), "wh:secret")},
		{btn(i18n.T(lang, "wh.btn_public"), "wh:public"), btn(i18n.T(lang, "wh.btn_domain"), "wh:domain")},
		{btn(i18n.T(lang, "wh.btn_apply"), "wh:apply")},
		{btn(i18n.T(lang, "admin.wh_btn_addr"), "wh:addr"), btn(i18n.T(lang, "admin.wh_btn_base"), "wh:base")},
		{btn(i18n.T(lang, "btn.back"), "menu:manage"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onWebhooksAdmin(ctx context.Context, chatID int64, val string) {
	action, _, _ := strings.Cut(val, ":")
	lang := a.lang(chatID)
	switch action {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Webhook.Enabled = !a.botCfg.Webhook.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showWebhooksAdmin(ctx, chatID)
	case "addr":
		a.getUI(chatID).adminInput = "wh_addr"
		a.askInput(ctx, chatID, i18n.T(lang, "admin.wh_ask_addr"), "menu:webhooks")
	case "base":
		a.getUI(chatID).adminInput = "wh_base"
		a.askInput(ctx, chatID, i18n.T(lang, "admin.wh_ask_base"), "menu:webhooks")
	case "secret":
		a.getUI(chatID).adminInput = "wh_secret"
		a.askInput(ctx, chatID, i18n.T(lang, "admin.wh_ask_secret"), "menu:webhooks")
	case "public":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Webhook.TLS = !a.botCfg.Webhook.TLS
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showWebhooksAdmin(ctx, chatID)
	case "domain":
		a.getUI(chatID).adminInput = "wh_domain"
		a.askInput(ctx, chatID, i18n.T(lang, "wh.ask_domain"), "menu:webhooks")
	case "apply":
		a.applyWebhookServer(ctx, chatID)
	}
}

func (a *App) applyWebhookServer(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	tls := a.botCfg != nil && a.botCfg.Webhook.TLS
	domain := ""
	if a.botCfg != nil {
		domain = a.botCfg.Webhook.Domain
	}
	a.mu.Unlock()
	if !tls || domain == "" {
		a.send(ctx, chatID, i18n.T(lang, "wh.apply_need_domain"))
		return
	}
	if a.ctl == nil || !a.ctl.Available() {
		a.send(ctx, chatID, i18n.T(lang, "wh.apply_unavailable"))
		return
	}
	a.send(ctx, chatID, i18n.T(lang, "wh.applying"))
	if err := a.ctl.PublishWebhookPorts(ctx); err != nil {
		a.send(ctx, chatID, i18n.T(lang, "wh.apply_fail", err.Error()))
		return
	}
}

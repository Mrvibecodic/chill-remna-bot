package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

func (a *App) webhookListenPort() string {
	addr := ":8080"
	a.mu.Lock()
	if a.botCfg != nil && a.botCfg.Webhook.ListenAddr != "" {
		addr = a.botCfg.Webhook.ListenAddr
	}
	a.mu.Unlock()
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i+1:]
	}
	return "8080"
}

func (a *App) selfContainerName() string {
	if a.ctl != nil {
		if n := a.ctl.SelfContainer(); n != "" {
			return n
		}
	}
	return "remnabot"
}

func (a *App) showWebhooksAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)

	a.mu.Lock()
	base := ""
	rwSecret := ""
	domain := ""
	tls := false
	if a.botCfg != nil {
		base = a.botCfg.Webhook.PublicBaseURL
		rwSecret = a.botCfg.Webhook.RemnawaveSecret
		domain = a.botCfg.Webhook.Domain
		tls = a.botCfg.Webhook.TLS
	}
	a.mu.Unlock()
	if tls && domain != "" {
		base = "https://" + domain
	}

	secretDisp := i18n.T(lang, "admin.no")
	if rwSecret != "" {
		secretDisp = i18n.T(lang, "admin.yes")
	}
	pubLabel := i18n.T(lang, "wh.public_off")
	if tls {
		pubLabel = i18n.T(lang, "wh.public_on")
	}
	domainDisp := domain
	if domainDisp == "" {
		domainDisp = i18n.T(lang, "admin.none")
	}

	urls := ""
	if base != "" {
		urls = "\n\n" + i18n.T(lang, "wh.urls",
			base+"/webhook/yookassa", base+"/webhook/cryptobot",
			base+"/webhook/platega", base+"/webhook/tribute")
	}

	text := i18n.T(lang, "wh.screen", a.selfContainerName(), a.webhookListenPort(), pubLabel, domainDisp, secretDisp) + urls

	a.sendSysKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "wh.btn_guide"), "wh:guide")},
		{btn(i18n.T(lang, "wh.btn_public"), "wh:public"), btn(i18n.T(lang, "wh.btn_domain"), "wh:domain")},
		{btn(i18n.T(lang, "wh.btn_apply"), "wh:apply")},
		{btn(i18n.T(lang, "wh.btn_base"), "wh:base"), btn(i18n.T(lang, "admin.wh_btn_secret"), "wh:secret")},
		{btn(i18n.T(lang, "btn.back"), "menu:system"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) showWebhookGuide(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	c := a.selfContainerName()
	p := a.webhookListenPort()
	caddy := fmt.Sprintf("handle /webhook/* {\n    reverse_proxy %s:%s\n}", c, p)
	nginx := fmt.Sprintf("location /webhook/ {\n    proxy_pass http://%s:%s;\n    proxy_set_header Host $host;\n}", c, p)
	text := i18n.T(lang, "wh.guide_intro", c, p) +
		"\n\n<b>Caddy</b>\n<pre>" + caddy + "</pre>" +
		"\n<b>nginx</b>\n<pre>" + nginx + "</pre>\n\n" +
		i18n.T(lang, "wh.guide_after")
	a.sendKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.back"), "menu:webhooks"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onWebhooksAdmin(ctx context.Context, chatID int64, val string) {
	action, _, _ := strings.Cut(val, ":")
	lang := a.lang(chatID)
	switch action {
	case "guide":
		a.showWebhookGuide(ctx, chatID)
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
		a.sendHome(ctx, chatID, i18n.T(lang, "wh.apply_need_domain"))
		return
	}
	if a.ctl == nil || !a.ctl.Available() {
		a.sendHome(ctx, chatID, i18n.T(lang, "wh.apply_unavailable"))
		return
	}
	msgID := a.msg.SendKB(ctx, chatID, a.applyPremium(i18n.T(lang, "wh.applying")), nil)
	marker := filepath.Join(a.cfg.DataDir, "webhook.pending")
	_ = os.WriteFile(marker, []byte(strconv.FormatInt(chatID, 10)+":"+strconv.Itoa(msgID)), 0o600)
	if err := a.ctl.PublishWebhookPorts(ctx); err != nil {
		_ = os.Remove(marker)
		a.sendHome(ctx, chatID, i18n.T(lang, "wh.apply_fail", err.Error()))
		return
	}
}

func (a *App) cleanupWebhookApplyMsg(ctx context.Context) {
	marker := filepath.Join(a.cfg.DataDir, "webhook.pending")
	data, err := os.ReadFile(marker)
	if err != nil {
		return
	}
	_ = os.Remove(marker)
	parts := strings.SplitN(strings.TrimSpace(string(data)), ":", 2)
	if len(parts) != 2 {
		return
	}
	chatID, _ := strconv.ParseInt(parts[0], 10, 64)
	msgID, _ := strconv.Atoi(parts[1])
	if chatID != 0 && msgID != 0 && a.msg != nil {
		a.msg.Delete(ctx, chatID, msgID)
	}
	if chatID != 0 {
		a.sendHome(ctx, chatID, i18n.T(a.lang(chatID), "wh.applied"))
	}
}

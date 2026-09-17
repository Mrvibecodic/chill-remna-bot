package app

import (
	"context"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"golang.org/x/net/idna"

	"remnabot/internal/i18n"
)

// showSubdomain рисует экран домена подписки. note — итог последнего действия:
// отдельным сообщением его затёрла бы перерисовка экрана.
func (a *App) showSubdomain(ctx context.Context, chatID int64, note string) {
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
		btn(i18n.T(lang, "btn.back"), "menu:panelauth"),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	})

	display := i18n.T(lang, "admin.none")
	if cur != "" {
		display = html.EscapeString(cur)
	}
	text := i18n.T(lang, "subdomain.title", i18n.T(lang, statusKey), display)
	if note != "" {
		text = note + "\n\n" + text
	}
	a.sendSysKB(ctx, chatID, text, rows)
}

func (a *App) askSubdomain(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.getUI(chatID).adminInput = "subdomain"
	a.getUI(chatID).priceMonths = 0
	a.sendKB(ctx, chatID, i18n.T(lang, "subdomain.ask"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.cancel"), "subd:cancel")},
	})
}

// subHostRe — имя хоста с необязательным портом. Домен подставляется в ссылку
// подписки как есть, так что мусор здесь ломал бы ссылки всем пользователям.
var subHostRe = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)*(:([1-9][0-9]{0,4}))?$`)

// asciiHost переводит кириллический домен в punycode (порт сохраняется):
// ссылка подписки уходит в клиенты, которые IDN сами не понимают.
func asciiHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	name, port := h, ""
	if i := strings.LastIndex(h, ":"); i >= 0 {
		name, port = h[:i], h[i:]
	}
	name = strings.TrimSuffix(name, ".")
	if a, err := idna.Lookup.ToASCII(name); err == nil {
		name = a
	}
	return name + port
}

// validPort — порт после двоеточия, если он есть, в пределах 1–65535.
func validPort(host string) bool {
	i := strings.LastIndex(host, ":")
	if i < 0 {
		return true
	}
	n, err := strconv.Atoi(host[i+1:])
	return err == nil && n >= 1 && n <= 65535
}

func (a *App) setSubdomain(ctx context.Context, chatID int64, raw string) {
	lang := a.lang(chatID)
	a.getUI(chatID).adminInput = ""
	raw = strings.TrimSpace(raw)
	if raw == "-" || raw == "—" {
		raw = ""
	}

	host := asciiHost(extractHost(raw))
	if raw != "" && (len(host) > 253 || !subHostRe.MatchString(host) || !validPort(host)) {
		a.showSubdomain(ctx, chatID, i18n.T(lang, "subdomain.bad"))
		return
	}
	a.mu.Lock()
	if a.botCfg == nil {
		a.mu.Unlock()
		a.showSubdomain(ctx, chatID, "")
		return
	}
	prev := a.botCfg.SubscriptionDomain
	a.botCfg.SubscriptionDomain = host
	a.mu.Unlock()
	if err := a.saveBotConfig(ctx); err != nil {
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.SubscriptionDomain = prev
		}
		a.mu.Unlock()
		a.showSubdomain(ctx, chatID, i18n.T(lang, "panelauth.save_fail", shortErr(err)))
		return
	}
	a.showSubdomain(ctx, chatID, "")
}

func (a *App) onSubdomain(ctx context.Context, chatID int64, val string) {
	switch val {
	case "edit":
		a.askSubdomain(ctx, chatID)
	case "clear":
		a.setSubdomain(ctx, chatID, "")
	case "cancel":
		a.getUI(chatID).adminInput = ""
		a.showSubdomain(ctx, chatID, "")
	}
}

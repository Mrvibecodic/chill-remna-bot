package app

import (
	"context"
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

const nameMaxLen = 48

var suspiciousNamePatterns = []string{
	"admin", "админ",
	"support", "saport", "сапорт", "саппорт", "поддерж", "помощ",
	"verif", "вериф",
	"official", "офиц",
	"moder", "модер",
	"remnawave",
	"security", "безопасн",
	"refund", "возврат",
}

func stripHTMLTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return html.UnescapeString(b.String())
}

func escapeName(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > nameMaxLen {
		s = string(r[:nameMaxLen]) + "…"
	}
	return html.EscapeString(s)
}

func suspiciousName(parts ...string) string {
	s := strings.ToLower(strings.Join(parts, " "))
	for _, p := range suspiciousNamePatterns {
		if strings.Contains(s, p) {
			return p
		}
	}
	return ""
}

func (a *App) guardNewUser(ctx context.Context, chatID int64, firstName, username string) bool {
	if a.store == nil {
		return false
	}
	pat := suspiciousName(username, firstName)
	if pat == "" {
		if bn := a.botUsername(ctx); bn != "" && suspiciousName(bn) == "" &&
			strings.Contains(strings.ToLower(username+" "+firstName), strings.ToLower(bn)) {
			pat = bn
		}
		if pat == "" {
			return false
		}
	}
	_ = a.store.SetBlocked(ctx, chatID, true)
	a.log.Warn("guard: suspicious registration auto-blocked", "tg_id", chatID, "username", username, "first_name", firstName, "pattern", pat)

	lang := a.lang(chatID)
	var rows [][]models.InlineKeyboardButton
	if sup := a.supportURL(); sup != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(lang, "btn.support"), URL: sup}})
	}
	a.msg.SendKB(ctx, chatID, a.applyPremium(i18n.T(lang, "guard.user_blocked")), rows)

	alang := a.lang(a.cfg.AdminID)
	id := strconv.FormatInt(chatID, 10)
	a.notifyKB(ctx, a.cfg.AdminID,
		i18n.T(alang, "guard.suspicious", a.userLabelByID(ctx, chatID), pat),
		[][]models.InlineKeyboardButton{{
			btn(i18n.T(alang, "btn.unblock"), "usr:unblock:"+id),
			btn(i18n.T(alang, "guard.btn_card"), "usr:view:"+id),
		}})
	return true
}

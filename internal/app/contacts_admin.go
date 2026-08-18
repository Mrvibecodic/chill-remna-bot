package app

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func (a *App) showContacts(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	var c struct{ G, S, T string }
	if a.botCfg != nil {
		c.G = a.botCfg.Contact.GroupURL
		c.S = a.botCfg.Contact.SupportURL
		c.T = a.botCfg.Contact.TermsText
	}
	a.mu.Unlock()
	display := func(v string) string {
		if v == "" {
			return i18n.T(lang, "admin.none")
		}
		return v
	}
	legalStatus := i18n.T(lang, "contacts.legal_off")
	if names := a.legalNames(lang); names != "" {
		legalStatus = i18n.T(lang, "contacts.legal_on", names)
	}
	body := i18n.T(lang, "contacts.title", display(c.G), display(c.S), legalStatus)

	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "contacts.btn_group"), "ctc:group"), btn(i18n.T(lang, "contacts.btn_support"), "ctc:support")},
		{btn(i18n.T(lang, "contacts.btn_legal"), "leg:open")},
	}

	if c.G != "" || c.S != "" || c.T != "" || a.legalCfg().Any() {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "contacts.btn_clear"), "ctc:clear"),
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:iface"),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	})
	// Render on the parent Interface banner (like the sibling screens) so
	// navigating to/from Contacts edits the caption in place instead of
	// sending a new bannerless message.
	a.sendIfaceKB(ctx, chatID, body, rows)
}

func (a *App) onContacts(ctx context.Context, chatID int64, val string) {
	ui := a.getUI(chatID)
	lang := a.lang(chatID)
	cancel := [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.cancel"), "ctc:cancel")}}
	switch val {
	case "group":
		ui.adminInput = "ctc_group"
		a.sendKB(ctx, chatID, i18n.T(lang, "contacts.ask_group"), cancel)
	case "support":
		ui.adminInput = "ctc_support"
		a.sendKB(ctx, chatID, i18n.T(lang, "contacts.ask_support"), cancel)
	case "clear":

		a.mu.Lock()
		if a.botCfg != nil {
			// Документы живут на своём экране и своей кнопкой не чистятся; в
			// легаси-поле остаётся зеркало соглашения (см. NormalizeLegal) —
			// обнулить его здесь значит стереть сам документ.
			a.botCfg.Contact = model.ContactConfig{TermsText: a.botCfg.Legal.Terms.Text}
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showContacts(ctx, chatID)
	case "cancel":
		ui.adminInput = ""
		a.showContacts(ctx, chatID)
	default:
		// Кнопка со старого экрана, оставшегося в переписке (например,
		// «Изменить текст соглашения» до появления документов).
		a.showContacts(ctx, chatID)
	}
}

func (a *App) setContact(ctx context.Context, chatID int64, field, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "-" || raw == "—" {
		raw = ""
	}
	a.mu.Lock()
	if a.botCfg != nil {
		switch field {
		case "group":
			a.botCfg.Contact.GroupURL = normalizeContactURL(raw)
		case "support":
			a.botCfg.Contact.SupportURL = normalizeContactURL(raw)
		}
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.getUI(chatID).adminInput = ""
	a.showContacts(ctx, chatID)
}

// normalizeContactURL turns admin input for the Group/Support buttons into a
// value Telegram accepts as an inline-button URL. Plain links, @usernames,
// bare usernames and t.me/... (with or without scheme) all become a usable
// https/tg link; an empty value clears the button.
func normalizeContactURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	low := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(low, "https://"), strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "tg://"):
		return raw
	case strings.HasPrefix(raw, "@"):
		return "https://t.me/" + strings.TrimPrefix(raw, "@")
	case strings.HasPrefix(low, "t.me/"), strings.HasPrefix(low, "telegram.me/"),
		strings.HasPrefix(low, "telegram.dog/"), strings.HasPrefix(low, "www."):
		return "https://" + raw
	case !strings.ContainsAny(raw, "/. :"):
		// Bare username like "my_channel".
		return "https://t.me/" + raw
	case strings.Contains(raw, "."):
		// Looks like a domain without a scheme.
		return "https://" + raw
	default:
		return raw
	}
}

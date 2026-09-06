package app

import (
	"context"
	"net"
	"net/url"
	"strings"
	"unicode"

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
	val, ok := normalizeContactURL(raw)
	if !ok {
		// Не сохраняем и оставляем ожидание ввода: битый адрес в этом поле
		// оставляет без экрана всех пользователей сразу.
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "contacts.bad_url"))
		return
	}
	a.mu.Lock()
	if a.botCfg != nil {
		switch field {
		case "group":
			a.botCfg.Contact.GroupURL = val
		case "support":
			a.botCfg.Contact.SupportURL = val
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
// ok=false — ввод адресом кнопки быть не может.
//
// Раньше последняя ветка возвращала введённое как есть, и любая строка
// становилась адресом. Telegram отвергает сообщение с таким адресом ЦЕЛИКОМ, а
// кнопки контактов подмешиваются в главное меню, в «Мои подписки» и в
// сообщение об активной подписке — то есть одна опечатка в поле «Группа»
// оставляла без экрана всех пользователей сразу. Причём админ этого не видел:
// в его меню контактных кнопок нет.
func normalizeContactURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	low := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(low, "https://"), strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "tg://"):
		return raw, validButtonURL(raw)
	case strings.HasPrefix(raw, "@"):
		return checkedURL("https://t.me/" + strings.TrimPrefix(raw, "@"))
	case strings.HasPrefix(low, "t.me/"), strings.HasPrefix(low, "telegram.me/"),
		strings.HasPrefix(low, "telegram.dog/"), strings.HasPrefix(low, "www."):
		return checkedURL("https://" + raw)
	case !strings.ContainsAny(raw, "/. :"):
		// Bare username like "my_channel".
		return checkedURL("https://t.me/" + raw)
	case strings.Contains(raw, "."):
		// Looks like a domain without a scheme.
		return checkedURL("https://" + raw)
	default:
		// Осмысленного адреса из этого не выйдет — раньше сюда попадала любая
		// фраза вроде «пишите в личку».
		return "", false
	}
}

func checkedURL(u string) (string, bool) {
	if !validButtonURL(u) {
		return "", false
	}
	return u, true
}

// validButtonURL — Telegram примет такой адрес в кнопке.
//
// Проверяем то же, на чём он спотыкается: схему, непустой хост, отсутствие
// пробелов и переводов строк.
// hasInvisible — есть ли в строке пробельные, управляющие или невидимые
// символы. Проверять только " \t\r\n" мало: адрес с нулевой шириной внутри
// (его легко получить копированием) выглядит рабочим, а Telegram отвергает
// такую кнопку вместе со ВСЕМ сообщением — меню перестаёт открываться, и
// админ этого не видит.
func hasInvisible(raw string) bool {
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) ||
			unicode.In(r, unicode.Cf, unicode.Zs, unicode.Zl, unicode.Zp) {
			return true
		}
	}
	return false
}

func validButtonURL(raw string) bool {
	if raw == "" {
		return false
	}
	if hasInvisible(raw) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "http":
		if u.Host == "" {
			return false
		}
		// Точка в имени — признак настоящего домена, но не единственный
		// допустимый адрес: локальный хост и IP-литерал (в том числе IPv6 в
		// скобках) тоже рабочие, и молча выкидывать такую кнопку нельзя.
		host := u.Hostname()
		return strings.Contains(host, ".") || strings.EqualFold(host, "localhost") ||
			net.ParseIP(host) != nil
	case "tg":
		// tg://resolve?domain=x — хоста в привычном смысле нет.
		return u.Opaque != "" || u.Host != ""
	}
	return false
}

package app

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-telegram/bot/models"

	"golang.org/x/net/idna"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Админский экран документов: тексты и ссылки соглашения и политики
// конфиденциальности плюс тумблеры мест показа.

// showLegalAdmin — экран «Документы» в разделе «Интерфейс → Контакты».
func (a *App) showLegalAdmin(ctx context.Context, chatID int64) {
	a.showLegalAdminView(ctx, chatID, false)
}

// showLegalAdminView рисует тот же экран; confirm добавляет подтверждение
// сброса согласий — действие необратимое и задевает всех пользователей.
func (a *App) showLegalAdminView(ctx context.Context, chatID int64, confirm bool) {
	lang := a.lang(chatID)
	cfg := a.legalCfg()
	state := func(d model.LegalDoc) string {
		switch {
		case d.Text != "" && d.URL != "":
			return i18n.T(lang, "legal.state_both")
		case d.Text != "":
			return i18n.T(lang, "legal.state_text")
		case d.URL != "":
			return i18n.T(lang, "legal.state_url")
		default:
			return i18n.T(lang, "legal.state_none")
		}
	}
	body := i18n.T(lang, "legal.title", state(cfg.Terms), state(cfg.Privacy))
	if !cfg.Any() {
		body += i18n.T(lang, "legal.title_empty")
	}
	// Каждая кнопка — отдельной строкой: подписи длинные («Согласие перед
	// покупкой: ✅»), и в паре на телефоне Telegram обрезает их многоточием.
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "legal.btn_terms_text"), "leg:tt")},
		{btn(i18n.T(lang, "legal.btn_terms_url"), "leg:tu")},
		{btn(i18n.T(lang, "legal.btn_privacy_text"), "leg:pt")},
		{btn(i18n.T(lang, "legal.btn_privacy_url"), "leg:pu")},
		{btn(toggleLabel(i18n.T(lang, "legal.btn_in_menu"), cfg.InMenu), "leg:menu")},
		{btn(toggleLabel(i18n.T(lang, "legal.btn_gate_buy"), cfg.GateBuy), "leg:buy")},
		{btn(toggleLabel(i18n.T(lang, "legal.btn_gate_start"), cfg.GateStart), "leg:start")},
		{btn(toggleLabel(i18n.T(lang, "legal.btn_on_pay"), cfg.OnPay), "leg:pay")},
	}
	if cfg.ConsentRequired() {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "legal.btn_reset"), "leg:reset")})
	}
	if confirm {
		body += i18n.T(lang, "legal.reset_confirm")
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "legal.btn_reset_ok"), "leg:resetok"),
			btn(i18n.T(lang, "btn.cancel"), "leg:open"),
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:contacts"),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	})
	a.sendIfaceKB(ctx, chatID, body, rows)
}

// toggleLabel — подпись тумблера со значком состояния.
func toggleLabel(text string, on bool) string {
	if on {
		return text + ": ✅"
	}
	return text + ": ❌"
}

func (a *App) onLegalAdmin(ctx context.Context, chatID int64, val string) {
	ui := a.getUI(chatID)
	lang := a.lang(chatID)
	cancel := [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.cancel"), "leg:cancel")}}
	ask := func(input, key string) {
		ui.adminInput = input
		a.sendKB(ctx, chatID, i18n.T(lang, key), cancel)
	}
	switch val {
	case "tt":
		ask("leg_terms_text", "legal.ask_text")
	case "tu":
		ask("leg_terms_url", "legal.ask_url")
	case "pt":
		ask("leg_privacy_text", "legal.ask_text")
	case "pu":
		ask("leg_privacy_url", "legal.ask_url")
	case "menu", "buy", "start", "pay":
		a.toggleLegalPlace(ctx, chatID, val)
	case "reset":
		a.showLegalAdminView(ctx, chatID, true)
	case "resetok":
		a.resetLegalConsent(ctx, chatID)
	case "cancel":
		ui.adminInput = ""
		a.showLegalAdmin(ctx, chatID)
	default:
		a.showLegalAdmin(ctx, chatID)
	}
}

// toggleLegalPlace включает/выключает место показа документов.
func (a *App) toggleLegalPlace(ctx context.Context, chatID int64, place string) {
	a.mu.Lock()
	if a.botCfg != nil {
		switch place {
		case "menu":
			a.botCfg.Legal.InMenu = !a.botCfg.Legal.InMenu
		case "buy":
			a.botCfg.Legal.GateBuy = !a.botCfg.Legal.GateBuy
		case "start":
			a.botCfg.Legal.GateStart = !a.botCfg.Legal.GateStart
		case "pay":
			a.botCfg.Legal.OnPay = !a.botCfg.Legal.OnPay
		}
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showLegalAdmin(ctx, chatID)
}

// setLegalDoc записывает текст или ссылку документа.
//
// Ссылка проверяется: кнопку с невалидным URL Telegram не примет, и экран
// документов молча остался бы без кнопки.
func (a *App) setLegalDoc(ctx context.Context, chatID int64, kind, field, raw string) {
	lang := a.lang(chatID)
	raw = strings.TrimSpace(raw)
	if raw == "-" || raw == "—" {
		raw = ""
	}
	if field == "url" && raw != "" {
		norm, ok := normalizeDocURL(raw)
		if !ok {
			a.getUI(chatID).adminInput = ""
			a.sendKB(ctx, chatID, i18n.T(lang, "legal.bad_url"), [][]models.InlineKeyboardButton{
				{btn(i18n.T(lang, "btn.back"), "leg:cancel")},
			})
			return
		}
		raw = norm
	}
	a.mu.Lock()
	if a.botCfg != nil {
		first := !a.botCfg.Legal.Any()
		doc := &a.botCfg.Legal.Terms
		if kind == model.LegalPrivacy {
			doc = &a.botCfg.Legal.Privacy
		}
		if field == "url" {
			doc.URL = raw
		} else {
			doc.Text = raw
		}
		// Показ включает ТОЛЬКО первый заданный документ: иначе оператор задаёт
		// текст и не понимает, почему его никто не видит. Правка текста, когда
		// показ осознанно выключен, тумблеры уже не трогает.
		if raw != "" && first {
			a.botCfg.Legal.InMenu = true
			a.botCfg.Legal.GateBuy = true
		}
		// Легаси-зеркало для отката на старый образ (см. NormalizeLegal).
		a.botCfg.NormalizeLegal()
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.getUI(chatID).adminInput = ""
	a.showLegalAdmin(ctx, chatID)
}

// resetLegalConsent снимает согласие со ВСЕХ пользователей: документы
// изменились — согласие на прошлую редакцию больше не считается.
func (a *App) resetLegalConsent(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if a.store == nil {
		a.notify(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	if err := a.store.ResetTermsAccepted(ctx); err != nil {
		a.log.Warn("сброс согласий не выполнен", "err", err)
		a.notify(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	a.notify(ctx, chatID, i18n.T(lang, "legal.reset_done"))
	a.showLegalAdmin(ctx, chatID)
}

// normalizeDocURL приводит ссылку на документ к http(s)-виду. Второе значение
// false — ссылка не годится для кнопки.
//
// Проверка строгая намеренно: ссылку из кнопки Telegram разбирает сам, и на
// кривом адресе ОТКАЗЫВАЕТ ВСЕМУ сообщению. Экран согласия при входе тогда не
// уходит вовсе, а бот молча перестаёт открываться — поэтому мусор отбивается
// здесь, а не «покажем без кнопки».
func normalizeDocURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	low := strings.ToLower(raw)
	if !strings.HasPrefix(low, "https://") && !strings.HasPrefix(low, "http://") {
		if strings.Contains(raw, "://") || !strings.Contains(raw, ".") {
			return "", false
		}
		raw = "https://" + raw
	}
	// Управляющие символы, пробелы, кавычки и угловые скобки в ссылке кнопки
	// не место: Telegram такую кнопку отвергает.
	for _, r := range raw {
		if r <= ' ' || r == 0x7f || r == '"' || r == '\'' || r == '<' || r == '>' || r == '\\' || unicode.IsSpace(r) {
			return "", false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	host := strings.TrimSuffix(u.Hostname(), ".")
	// Кириллический домен Telegram примет, а клиент по процент-кодированному
	// хосту никуда не пойдёт — переводим в punycode.
	if ascii, ierr := idna.Lookup.ToASCII(host); ierr == nil && ascii != "" {
		host = ascii
	}
	isIP := net.ParseIP(host) != nil
	if !isIP && (!strings.Contains(host, ".") || strings.HasPrefix(host, ".") || strings.Contains(host, "..")) {
		return "", false
	}
	port := u.Port()
	if port != "" {
		n, perr := strconv.Atoi(port)
		if perr != nil || n < 1 || n > 65535 {
			return "", false
		}
	}
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if port != "" {
		u.Host += ":" + port
	}
	out := u.String()
	// Кнопку с непомерной ссылкой Telegram отвергает вместе со всем
	// сообщением — а это экран согласия, без которого бот не открывается.
	if len(out) > 2000 {
		return "", false
	}
	return out, true
}

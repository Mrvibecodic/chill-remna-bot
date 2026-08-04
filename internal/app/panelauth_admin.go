package app

import (
	"context"
	"html"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// Экран «Доступ к панели»: X-Api-Key аддона «Caddy with security» и кука прокси
// установщика eGames. Оба задаются в мастере при установке, но панель могут
// закрыть уже после — или ключ сменят в портале, — поэтому их нужно править и
// потом, не проходя мастер заново.

// panelSecrets — то, что показываем на экране, снято под замком разом.
type panelSecrets struct {
	apiKey  string
	cookie  string
	fromEnv bool
}

func (a *App) panelSecrets() panelSecrets {
	s := panelSecrets{fromEnv: a.caddyKeyFromEnv()}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg != nil {
		s.apiKey = a.botCfg.Panel.APIKey
		s.cookie = a.botCfg.Panel.Cookie
	}
	return s
}

// maskSecret показывает, что значение на месте, не раскрывая его: в чате
// история команд подчищается, но экран может попасть на скриншот. Короткие
// значения закрываем целиком — у них «две буквы с краёв» это почти весь
// секрет. Результат идёт в сообщение с parse_mode=HTML, поэтому экранируется:
// иначе ключ с «<» или «&» не замаскировался бы, а сорвал разбор — экран
// вообще не открылся бы.
func maskSecret(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) < 12 {
		return strings.Repeat("•", 8)
	}
	return html.EscapeString(string(r[:2])) + strings.Repeat("•", 8) + html.EscapeString(string(r[len(r)-2:]))
}

// showPanelAuth рисует экран. note — результат последнего действия (проверка
// связи, ошибка сохранения): отдельным сообщением его показывать нельзя, его
// тут же затрёт перерисовка экрана, поэтому он живёт в самом экране.
func (a *App) showPanelAuth(ctx context.Context, chatID int64, note string) {
	lang := a.lang(chatID)
	s := a.panelSecrets()

	keyState := i18n.T(lang, "panelauth.key_none")
	switch {
	case s.fromEnv && s.apiKey != "":
		keyState = i18n.T(lang, "panelauth.key_env_stale")
	case s.fromEnv:
		keyState = i18n.T(lang, "panelauth.key_env")
	case s.apiKey != "":
		keyState = i18n.T(lang, "panelauth.key_set", maskSecret(s.apiKey))
	}
	cookieState := i18n.T(lang, "panelauth.cookie_none")
	if s.cookie != "" {
		cookieState = i18n.T(lang, "panelauth.cookie_set", maskSecret(s.cookie))
	}

	var rows [][]models.InlineKeyboardButton
	var keyRow []models.InlineKeyboardButton
	// Пока ключ приходит из CADDY_AUTH_API_TOKEN, вводить его здесь нельзя:
	// переменная всё равно перекроет сохранённое, вышло бы молчаливое враньё.
	// А вот убрать давний ключ из БД можно и нужно — иначе он воскреснет в тот
	// день, когда переменную уберут.
	if !s.fromEnv {
		keyRow = append(keyRow, btn(i18n.T(lang, "panelauth.btn_key"), "pauth:key"))
	}
	if s.apiKey != "" {
		keyRow = append(keyRow, btn(i18n.T(lang, "panelauth.btn_key_clear"), "pauth:keyclear"))
	}
	if len(keyRow) > 0 {
		rows = append(rows, keyRow)
	}

	cookieRow := []models.InlineKeyboardButton{btn(i18n.T(lang, "panelauth.btn_cookie"), "pauth:cookie")}
	if s.cookie != "" {
		cookieRow = append(cookieRow, btn(i18n.T(lang, "panelauth.btn_cookie_clear"), "pauth:cookieclear"))
	}
	rows = append(rows,
		cookieRow,
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "panelauth.btn_check"), "pauth:check")},
		[]models.InlineKeyboardButton{
			btn(i18n.T(lang, "btn.back"), "menu:system"),
			btn(i18n.T(lang, "btn.home"), "menu:home"),
		})

	text := i18n.T(lang, "panelauth.title", keyState, cookieState)
	if note != "" {
		text = trimNote(note) + "\n\n" + text
	}
	a.sendSysKB(ctx, chatID, text, rows)
}

// trimNote укорачивает длинное пояснение: вместе с экраном оно уезжает в
// подпись к баннеру, а слишком длинная подпись роняет экран в простой текст
// (см. sendKBSection) — картинка раздела тогда пропадает.
func trimNote(s string) string {
	const max = 400
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func (a *App) onPanelAuth(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	switch val {
	case "key":
		if a.caddyKeyFromEnv() {
			a.showPanelAuth(ctx, chatID, "")
			return
		}
		a.getUI(chatID).adminInput = "panel_apikey"
		a.askInput(ctx, chatID, i18n.T(lang, "panelauth.ask_key"), "menu:panelauth")
	case "cookie":
		a.getUI(chatID).adminInput = "panel_cookie"
		a.askInput(ctx, chatID, i18n.T(lang, "panelauth.ask_cookie"), "menu:panelauth")
	case "keyclear":
		a.setPanelSecret(ctx, chatID, "panel_apikey", "")
	case "cookieclear":
		a.setPanelSecret(ctx, chatID, "panel_cookie", "")
	case "check":
		a.showPanelAuth(ctx, chatID, a.panelAuthNote(ctx, chatID))
	}
}

// clearPanelInput снимает ожидание секрета, если админ ушёл с экрана, не нажав
// «Отмена». Без этого состояние жило бы до конца сессии, и первое же случайное
// сообщение — ID пользователя, заметка — молча уехало бы в ключ панели и сразу
// применилось к живому клиенту, то есть отрезало бы бота от панели.
func (a *App) clearPanelInput(chatID int64) {
	ui := a.getUI(chatID)
	switch ui.adminInput {
	case "panel_apikey", "panel_cookie":
		ui.adminInput = ""
		ui.inputBack = ""
	}
}

// setPanelSecret сохраняет ключ или куку и сразу пересобирает клиента панели:
// иначе бот продолжил бы ходить со старым секретом до перезапуска.
func (a *App) setPanelSecret(ctx context.Context, chatID int64, field, text string) {
	lang := a.lang(chatID)
	text = strings.TrimSpace(text)
	if text == "-" || text == "—" {
		text = ""
	}
	a.getUI(chatID).adminInput = ""

	a.mu.Lock()
	if a.botCfg == nil {
		a.mu.Unlock()
		a.showPanelAuth(ctx, chatID, "")
		return
	}
	var prev string
	switch field {
	case "panel_apikey":
		// Ввод нового ключа при активной переменной запрещён, а очистка —
		// разрешена: она как раз убирает то, что переменная перекрывает.
		if a.caddyKeyFromEnv() && text != "" {
			a.mu.Unlock()
			a.showPanelAuth(ctx, chatID, "")
			return
		}
		prev, a.botCfg.Panel.APIKey = a.botCfg.Panel.APIKey, text
	case "panel_cookie":
		prev, a.botCfg.Panel.Cookie = a.botCfg.Panel.Cookie, text
	default:
		a.mu.Unlock()
		a.showPanelAuth(ctx, chatID, "")
		return
	}
	panelCfg := a.botCfg.Panel
	a.mu.Unlock()

	if err := a.saveBotConfig(ctx); err != nil {
		// Откатываем: иначе экран показывал бы новое значение, БД хранила бы
		// старое, а бот ходил бы с третьим.
		a.mu.Lock()
		if a.botCfg != nil {
			switch field {
			case "panel_apikey":
				a.botCfg.Panel.APIKey = prev
			case "panel_cookie":
				a.botCfg.Panel.Cookie = prev
			}
		}
		a.mu.Unlock()
		a.showPanelAuth(ctx, chatID, i18n.T(lang, "panelauth.save_fail", html.EscapeString(err.Error())))
		return
	}

	a.mu.Lock()
	old := a.panel
	client := remnawave.New(a.panelWithEnv(panelCfg))
	// Лог API живёт внутри клиента: без переноса история отказов, ради которой
	// админ сюда и пришёл, обнулилась бы ровно в момент починки.
	if old != nil {
		client.ImportLogs(old.Logs())
	}
	a.panel = client
	a.mu.Unlock()

	a.showPanelAuth(ctx, chatID, a.panelAuthNote(ctx, chatID))
}

// panelAuthNote дёргает панель и возвращает строку с вердиктом: секрет без
// проверки связи бесполезен — человек должен сразу увидеть, подошёл он или нет.
// Текст ошибки экранируется: в него попадает кусок ответа сервера, а там может
// оказаться HTML — тогда Telegram не разобрал бы сообщение целиком.
func (a *App) panelAuthNote(ctx context.Context, chatID int64) string {
	lang := a.lang(chatID)
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if panel == nil {
		return ""
	}
	if err := panel.Health(ctx); err != nil {
		return i18n.T(lang, "panelauth.check_fail", html.EscapeString(err.Error()))
	}
	count, err := panel.SystemStats(ctx)
	if err != nil {
		return i18n.T(lang, "panelauth.check_fail", html.EscapeString(err.Error()))
	}
	return i18n.T(lang, "panelauth.check_ok", count)
}

// panelAuthRelevant — экран нужен не всем: локальная панель в общей docker-сети
// защиты не имеет, там ни ключ, ни кука ни при чём.
func (a *App) panelAuthRelevant() bool {
	if a.caddyKeyFromEnv() {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return false
	}
	return a.botCfg.Panel.Mode == model.ModeRemote ||
		a.botCfg.Panel.APIKey != "" || a.botCfg.Panel.Cookie != ""
}

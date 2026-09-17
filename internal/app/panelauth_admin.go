package app

import (
	"context"
	"errors"
	"html"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"golang.org/x/net/idna"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// Экран «Подключение к панели»: адрес и API-token панели, X-Api-Key аддона
// «Caddy with security», кука прокси установщика eGames и домен подписки. Всё
// это задаётся в мастере при установке, но меняется и потом — переезд панели,
// перевыпуск токена, закрытый прокси, — поэтому правится здесь, без мастера.

// panelSecrets — то, что показываем на экране, снято под замком разом.
type panelSecrets struct {
	baseURL string
	token   string
	local   bool
	apiKey  string
	cookie  string
	fromEnv bool
}

func (a *App) panelSecrets() panelSecrets {
	s := panelSecrets{fromEnv: a.caddyKeyFromEnv()}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg != nil {
		p := a.botCfg.Panel
		s.baseURL, s.token, s.local = p.BaseURL, p.APIToken, p.Mode == model.ModeLocal
		s.apiKey, s.cookie = p.APIKey, p.Cookie
	}
	return s
}

// panelPending — адрес или токен, которые не прошли проверку связи. Хранится
// до ухода с экрана, чтобы админ мог сохранить значение без проверки.
type panelPending struct {
	field string
	value string
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
	ui := a.getUI(chatID)

	addrState := i18n.T(lang, "panelauth.key_none")
	switch {
	case s.local:
		addrState = i18n.T(lang, "panelauth.addr_local", remnawave.LocalBaseURL)
	case s.baseURL != "":
		addrState = i18n.T(lang, "panelauth.addr_set", html.EscapeString(s.baseURL))
	}
	tokenState := i18n.T(lang, "panelauth.key_none")
	if s.token != "" {
		tokenState = i18n.T(lang, "panelauth.key_set", maskSecret(s.token))
	}
	subState := i18n.T(lang, "admin.none")
	if d := a.subOverride(); d != "" {
		subState = "<code>" + html.EscapeString(d) + "</code>"
	}

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
	if ui.panelPending != nil {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "panelauth.btn_force"), "pauth:force"),
			btn(i18n.T(lang, "panelauth.btn_drop"), "pauth:drop"),
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "panelauth.btn_url"), "pauth:url"),
		btn(i18n.T(lang, "panelauth.btn_token"), "pauth:token"),
	})
	// Ключ и кука нужны только там, где панель чем-то закрыта: локальная панель
	// в общей docker-сети защиты не имеет.
	relevant := a.panelAuthRelevant()
	var keyRow []models.InlineKeyboardButton
	// Пока ключ приходит из CADDY_AUTH_API_TOKEN, вводить его здесь нельзя:
	// переменная всё равно перекроет сохранённое, вышло бы молчаливое враньё.
	// А вот убрать давний ключ из БД можно и нужно — иначе он воскреснет в тот
	// день, когда переменную уберут.
	if !s.fromEnv && relevant {
		keyRow = append(keyRow, btn(i18n.T(lang, "panelauth.btn_key"), "pauth:key"))
	}
	if s.apiKey != "" {
		keyRow = append(keyRow, btn(i18n.T(lang, "panelauth.btn_key_clear"), "pauth:keyclear"))
	}
	if len(keyRow) > 0 {
		rows = append(rows, keyRow)
	}

	var cookieRow []models.InlineKeyboardButton
	if relevant {
		cookieRow = append(cookieRow, btn(i18n.T(lang, "panelauth.btn_cookie"), "pauth:cookie"))
	}
	if s.cookie != "" {
		cookieRow = append(cookieRow, btn(i18n.T(lang, "panelauth.btn_cookie_clear"), "pauth:cookieclear"))
	}
	if len(cookieRow) > 0 {
		rows = append(rows, cookieRow)
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "panelauth.btn_sub"), "menu:subdomain")},
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "panelauth.btn_check"), "pauth:check")},
		[]models.InlineKeyboardButton{
			btn(i18n.T(lang, "btn.back"), "menu:system"),
			btn(i18n.T(lang, "btn.home"), "menu:home"),
		})

	text := i18n.T(lang, "panelauth.title", addrState, tokenState, keyState, cookieState, subState)
	if note != "" {
		text = trimNote(note) + "\n\n" + text
	}
	a.sendSysKB(ctx, chatID, text, rows)
}

// trimNote укорачивает длинное пояснение: вместе с экраном оно уезжает в
// подпись к баннеру, а слишком длинная подпись роняет экран в простой текст
// (см. sendKBSection) — картинка раздела тогда пропадает.
func trimNote(s string) string {
	return cutHTML(s, 400)
}

// cutHTML укорачивает готовый HTML-текст до max рун, не разрезая сущность
// вроде «&lt;»: обрывок «&l» Telegram не разберёт и отвергнет сообщение целиком.
// Теги в укорачиваемых текстах только в начале шаблонов, поэтому режется хвост.
func cutHTML(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	r = r[:max]
	for i := len(r) - 1; i >= 0 && i >= len(r)-10; i-- {
		if r[i] == ';' {
			break
		}
		if r[i] == '&' {
			r = r[:i]
			break
		}
	}
	return string(r) + "…"
}

func (a *App) onPanelAuth(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	switch val {
	case "url":
		a.getUI(chatID).panelPending = nil
		a.getUI(chatID).adminInput = "panel_url"
		a.askInput(ctx, chatID, i18n.T(lang, "panelauth.ask_url", remnawave.LocalBaseURL), "menu:panelauth")
	case "token":
		a.getUI(chatID).panelPending = nil
		a.getUI(chatID).adminInput = "panel_token"
		a.askInput(ctx, chatID, i18n.T(lang, "panelauth.ask_token"), "menu:panelauth")
	case "force":
		a.forcePanelConn(ctx, chatID)
	case "drop":
		a.getUI(chatID).panelPending = nil
		a.showPanelAuth(ctx, chatID, "")
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
	// Возврат на главную закрывает и ожидание картинки баннера раздела: иначе
	// оно живёт до отмены и перехватывает следующий админский текст.
	ui.awaitSectionBanner = ""
	// Непроверенный адрес или токен живёт только до ухода с экрана.
	ui.panelPending = nil
	switch ui.adminInput {
	case "panel_apikey", "panel_cookie", "panel_url", "panel_token":
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
		a.showPanelAuth(ctx, chatID, i18n.T(lang, "panelauth.save_fail", shortErr(err)))
		return
	}
	a.rebuildPanel()
	a.showPanelAuth(ctx, chatID, a.panelAuthNote(ctx, chatID))
}

// rebuildPanel пересобирает клиента панели из текущего конфига: иначе бот
// ходил бы со старыми адресом и секретами до перезапуска.
func (a *App) rebuildPanel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	old := a.panel
	client := a.newPanel(a.botCfg.Panel)
	// Лог API живёт внутри клиента: без переноса история отказов, ради которой
	// админ сюда и пришёл, обнулилась бы ровно в момент починки.
	if old != nil {
		client.ImportLogs(old.Logs())
	}
	a.panel = client
}

// shortErr — текст ошибки для экрана: экранирован (в нём бывает кусок ответа
// сервера, в том числе HTML-страница) и укорочен уже ПОСЛЕ экранирования —
// иначе «<a href=…>» раздувается в разы, и вместе с экраном текст не влезает.
func shortErr(err error) string {
	return cutHTML(html.EscapeString(err.Error()), 200)
}

// normalizePanelURL приводит введённый адрес к базовому URL панели. Без схемы
// подставляется https; «/api» и слэш в конце отрезаются — клиент сам
// дописывает /api/…. Параметры, фрагмент и логин в адресе не принимаются: они
// молча потерялись бы или утекли бы в лог.
func normalizePanelURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return "", false
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.Opaque != "" || strings.HasSuffix(u.Host, ":") {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	name := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if name == "" {
		return "", false
	}
	// Кириллический домен — в punycode: так его отдаёт панель в ссылках, и
	// только так сработает сверка хоста для ключа Caddy.
	if a, err := idna.Lookup.ToASCII(name); err == nil {
		name = a
	} else if net.ParseIP(name) == nil {
		return "", false
	}
	host := name
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", false
		}
		host = net.JoinHostPort(name, p)
	} else if strings.Contains(name, ":") {
		host = "[" + name + "]"
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	path = strings.TrimSuffix(path, "/api")
	path = strings.TrimRight(path, "/")
	// Локальная панель в общей docker-сети отвечает только по http, и схему
	// к ней часто не пишут — адрес узнаём по имени контейнера.
	if host == "remnawave:3000" && path == "" {
		return remnawave.LocalBaseURL, true
	}
	return scheme + "://" + host + path, true
}

// panelHost — имя хоста панели для сравнения «тот же сервер или другой».
func panelHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

// normalizePanelToken чистит вставленный токен: пробелы по краям и префикс
// «Bearer », который часто копируют вместе с ним.
func normalizePanelToken(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if len(s) > 7 && strings.EqualFold(s[:7], "bearer ") {
		s = strings.TrimSpace(s[7:])
	}
	if s == "" || s == "-" || s == "—" || strings.EqualFold(s, "bearer") || strings.ContainsAny(s, " \t\r\n") {
		return "", false
	}
	return s, true
}

// applyConnField кладёт адрес или токен в конфиг панели. Адрес заодно задаёт
// режим: в локальном клиент ходит только на docker-адрес и введённый URL
// игнорирует, так что без смены режима правка адреса ничего бы не меняла.
//
// Ключ и кука — секреты прокси прежнего сервера: на другой хост их не
// отправляем ни при проверке, ни потом, поэтому при смене хоста они снимаются.
func applyConnField(p *model.PanelConfig, field, value string) {
	switch field {
	case "panel_url":
		if panelHost(effectiveBase(*p)) != panelHost(value) {
			p.APIKey, p.Cookie = "", ""
		}
		p.BaseURL = value
		if value == remnawave.LocalBaseURL {
			p.Mode = model.ModeLocal
		} else {
			p.Mode = model.ModeRemote
		}
	case "panel_token":
		p.APIToken = value
	}
}

// effectiveBase — адрес, по которому клиент ходит на самом деле: в локальном
// режиме введённый BaseURL не используется.
func effectiveBase(p model.PanelConfig) string {
	if p.Mode == model.ModeLocal {
		return remnawave.LocalBaseURL
	}
	return p.BaseURL
}

// panelVerifyTimeout — потолок проверки связи. Обновления Telegram бот
// разбирает по одному, и пока идёт проверка, остальные ждут (а подтверждение
// оплаты звёздами Telegram ждёт не дольше 10 с). Живая панель отвечает за
// доли секунды; медленную можно сохранить без проверки.
const panelVerifyTimeout = 8 * time.Second

// setPanelConn меняет адрес или токен панели. В отличие от ключа и куки,
// значение сперва проверяется на живой панели: неверный токен или адрес
// отрезают бота от панели целиком. Не прошло — ничего не пишем и предлагаем
// сохранить без проверки (переезд, когда адрес и токен меняют по очереди).
func (a *App) setPanelConn(ctx context.Context, chatID int64, field, text string) {
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	ui.adminInput = ""
	ui.panelPending = nil

	var value string
	var ok bool
	switch field {
	case "panel_url":
		if value, ok = normalizePanelURL(text); !ok {
			a.showPanelAuth(ctx, chatID, i18n.T(lang, "panelauth.url_bad"))
			return
		}
	case "panel_token":
		if value, ok = normalizePanelToken(text); !ok {
			a.showPanelAuth(ctx, chatID, i18n.T(lang, "panelauth.token_bad"))
			return
		}
	default:
		a.showPanelAuth(ctx, chatID, "")
		return
	}

	a.mu.Lock()
	if a.botCfg == nil {
		a.mu.Unlock()
		a.showPanelAuth(ctx, chatID, "")
		return
	}
	cand := a.botCfg.Panel
	a.mu.Unlock()
	applyConnField(&cand, field, value)
	a.joinPanelNetwork(ctx, cand)

	a.sendKB(ctx, chatID, i18n.T(lang, "panelauth.checking"), nil)
	vctx, cancel := context.WithTimeout(ctx, panelVerifyTimeout)
	count, err := checkPanel(vctx, a.newPanel(cand))
	cancel()
	if err != nil {
		ui.panelPending = &panelPending{field: field, value: value}
		a.showPanelAuth(ctx, chatID, i18n.T(lang, "panelauth.conn_fail", shortErr(err)))
		return
	}
	if err := a.commitPanelConn(ctx, field, value); err != nil {
		a.showPanelAuth(ctx, chatID, i18n.T(lang, "panelauth.save_fail", shortErr(err)))
		return
	}
	a.showPanelAuth(ctx, chatID, i18n.T(lang, "panelauth.conn_ok", count))
}

// forcePanelConn сохраняет значение, не прошедшее проверку, — по явной просьбе.
func (a *App) forcePanelConn(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	p := ui.panelPending
	ui.panelPending = nil
	if p == nil {
		a.showPanelAuth(ctx, chatID, "")
		return
	}
	a.mu.Lock()
	cand := model.PanelConfig{}
	if a.botCfg != nil {
		cand = a.botCfg.Panel
	}
	a.mu.Unlock()
	applyConnField(&cand, p.field, p.value)
	a.joinPanelNetwork(ctx, cand)
	if err := a.commitPanelConn(ctx, p.field, p.value); err != nil {
		a.showPanelAuth(ctx, chatID, i18n.T(lang, "panelauth.save_fail", shortErr(err)))
		return
	}
	a.showPanelAuth(ctx, chatID, i18n.T(lang, "panelauth.saved_unchecked"))
}

// joinPanelNetwork подключает бота к docker-сети панели, если адрес указывает
// на локальную панель: без этого имя remnawave не разрешится.
func (a *App) joinPanelNetwork(ctx context.Context, p model.PanelConfig) {
	if p.Mode != model.ModeLocal || a.ctl == nil || !a.ctl.Available() {
		return
	}
	if err := a.ctl.ConnectPanelNetwork(ctx); err != nil {
		a.log.Warn("подключение к сети панели", "err", err)
	}
}

func checkPanel(ctx context.Context, c *remnawave.Client) (int, error) {
	if err := c.Health(ctx); err != nil {
		return 0, err
	}
	return c.SystemStats(ctx)
}

var errNotConfigured = errors.New("бот не настроен")

// commitPanelConn пишет адрес или токен в конфиг и базу и пересобирает
// клиента. На ошибке записи откатывает только своё поле: остальное за это
// время могли поменять.
func (a *App) commitPanelConn(ctx context.Context, field, value string) error {
	a.mu.Lock()
	if a.botCfg == nil {
		a.mu.Unlock()
		return errNotConfigured
	}
	prev := a.botCfg.Panel
	applyConnField(&a.botCfg.Panel, field, value)
	a.mu.Unlock()

	if err := a.saveBotConfig(ctx); err != nil {
		a.mu.Lock()
		if a.botCfg != nil {
			switch field {
			case "panel_url":
				a.botCfg.Panel.BaseURL, a.botCfg.Panel.Mode = prev.BaseURL, prev.Mode
				a.botCfg.Panel.APIKey, a.botCfg.Panel.Cookie = prev.APIKey, prev.Cookie
			case "panel_token":
				a.botCfg.Panel.APIToken = prev.APIToken
			}
		}
		a.mu.Unlock()
		return err
	}
	a.rebuildPanel()
	return nil
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
	vctx, cancel := context.WithTimeout(ctx, panelVerifyTimeout)
	defer cancel()
	count, err := checkPanel(vctx, panel)
	if err != nil {
		return i18n.T(lang, "panelauth.check_fail", shortErr(err))
	}
	return i18n.T(lang, "panelauth.check_ok", count)
}

// panelAuthRelevant — нужны ли кнопки ключа и куки: локальная панель в общей
// docker-сети защиты не имеет, там ни ключ, ни кука ни при чём.
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

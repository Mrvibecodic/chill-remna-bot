package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"remnabot/internal/remnawave"
	"remnabot/internal/web"
)

// appConfigTTL bounds how long the subscription page's app-config is cached.
const appConfigTTL = 5 * time.Minute

// appConfigFailTTL — на сколько запоминается неудача. Короче appConfigTTL:
// страница подписки могла лечь на минуту, и заставлять человека ждать
// починки пять минут незачем.
const appConfigFailTTL = time.Minute

// connectUA is a browser-like User-Agent so a WAF/Cloudflare in front of the
// subscription page does not reject the fetch as a bot.
const connectUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// appConfigPaths are the known locations of the apps config on a Remnawave
// subscription page, newest/most-specific first. Different subscription-page
// builds use different filenames (the "v2" custom pages use .app-config-v2.json).
var appConfigPaths = []string{
	"/assets/.app-config-v2.json",
	"/assets/app-config.json",
	"/assets/app-config-v2.json",
}

// acLocalized is a {lang: text} map from the app-config.
type acLocalized map[string]string

// --- standard Remnawave app-config.json (platforms.<os> = []app) ---

type acButton struct {
	ButtonLink string      `json:"buttonLink"`
	ButtonText acLocalized `json:"buttonText"`
}

type acStep struct {
	Buttons     []acButton  `json:"buttons"`
	Description acLocalized `json:"description"`
}

type acApp struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	IsFeatured          bool   `json:"isFeatured"`
	URLScheme           string `json:"urlScheme"`
	InstallationStep    acStep `json:"installationStep"`
	AddSubscriptionStep acStep `json:"addSubscriptionStep"`
}

type appConfig struct {
	Platforms map[string][]acApp `json:"platforms"`
}

// --- v2 app-config (platforms.<os>.apps[].blocks[].buttons[]) ---

type acV2Button struct {
	Link string      `json:"link"`
	Type string      `json:"type"`
	Text acLocalized `json:"text"`
}

type acV2Block struct {
	Title       acLocalized  `json:"title"`
	Description acLocalized  `json:"description"`
	Buttons     []acV2Button `json:"buttons"`
}

type acV2App struct {
	Name     string      `json:"name"`
	Featured bool        `json:"featured"`
	Blocks   []acV2Block `json:"blocks"`
}

// acFlexName is a platform's display name. Older subscription pages wrote it as
// a plain string, the current schema writes a {lang: text} map — a struct field
// typed as one of them makes json.Unmarshal fail on the other and drops the
// whole config, so accept both.
type acFlexName struct {
	Plain string
	Loc   acLocalized
}

func (n *acFlexName) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	// Имя платформы косметическое: что бы в нём ни лежало, ронять из-за него
	// весь конфиг (и оставлять человека без списка приложений) нельзя.
	switch b[0] {
	case '"':
		_ = json.Unmarshal(b, &n.Plain)
	case '{':
		_ = json.Unmarshal(b, &n.Loc)
	}
	return nil
}

// pick returns the display name in the user's language.
func (n acFlexName) pick(lang string) string {
	if n.Plain != "" {
		return n.Plain
	}
	return localize(n.Loc, lang)
}

type acV2Platform struct {
	DisplayName acFlexName `json:"displayName"`
	Apps        []acV2App  `json:"apps"`
}

type appConfigV2 struct {
	Platforms map[string]acV2Platform `json:"platforms"`
}

// platformOrder is the display order for known platform keys; unknown keys are
// appended afterwards (sorted) so any future platform still shows up.
var platformOrder = []string{"android", "ios", "windows", "macos", "linux", "androidtv", "appletv", "tv", "harmony"}

// platformLabels maps a platform key to a human label (used when the config
// carries no displayName of its own).
var platformLabels = map[string]string{
	"android":   "Android",
	"ios":       "iPhone / iOS",
	"windows":   "Windows",
	"macos":     "macOS",
	"linux":     "Linux",
	"androidtv": "Android TV",
	"appletv":   "Apple TV",
	"tv":        "TV",
	"harmony":   "HarmonyOS",
}

func platformLabel(key, displayName string) string {
	if strings.TrimSpace(displayName) != "" {
		return displayName
	}
	if l, ok := platformLabels[strings.ToLower(key)]; ok {
		return l
	}
	if key == "" {
		return ""
	}
	return strings.ToUpper(key[:1]) + key[1:]
}

// orderedPlatformKeys returns the platform keys present, known ones first (per
// platformOrder) then any extras alphabetically. Matching ignores case: the
// config spells them androidTV/appleTV while platformOrder is lowercase, and a
// case-sensitive compare would push those platforms to the end of the list.
func orderedPlatformKeys(present map[string]bool) []string {
	byLower := map[string][]string{}
	for k := range present {
		lk := strings.ToLower(k)
		byLower[lk] = append(byLower[lk], k)
	}
	for _, ks := range byLower {
		sort.Strings(ks)
	}
	var out []string
	seen := map[string]bool{}
	for _, k := range platformOrder {
		for _, orig := range byLower[k] {
			out = append(out, orig)
			seen[orig] = true
		}
	}
	var extra []string
	for k := range present {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

type connectCacheEntry struct {
	base      string
	v2        *appConfigV2
	std       *appConfig
	fetchedAt time.Time
}

// localize picks the user's language, falling back to en, then ru, then any.
func localize(m acLocalized, lang string) string {
	if m == nil {
		return ""
	}
	if v := m[lang]; v != "" {
		return v
	}
	if v := m["en"]; v != "" {
		return v
	}
	if v := m["ru"]; v != "" {
		return v
	}
	for _, v := range m {
		if v != "" {
			return v
		}
	}
	return ""
}

// buildDeeplink combines a standard app's urlScheme with the subscription URL:
// query-style schemes (ending in "=") get the URL percent-encoded; path-style
// schemes get it appended raw.
func buildDeeplink(scheme, subURL string) string {
	if scheme == "" || subURL == "" {
		return ""
	}
	if strings.HasSuffix(scheme, "=") {
		return scheme + url.QueryEscape(subURL)
	}
	return scheme + subURL
}

// substituteV2 fills a v2 link template's placeholders with the user's values,
// mirroring how the subscription page builds the link (raw substitution).
// Подстановка идёт одним проходом: подставленное значение шаблоном не считается.
func substituteV2(tmpl, subURL, username string) string {
	return strings.NewReplacer("{{SUBSCRIPTION_LINK}}", subURL, "{{USERNAME}}", username).Replace(tmpl)
}

// appConfigBase returns scheme://host of the subscription URL — the root the
// app-config paths are resolved against.
func appConfigBase(subURL string) string {
	u, err := url.Parse(subURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// tryFetchParse GETs one candidate URL and parses it as either the v2 or the
// standard schema. ok is true only when at least one iOS/Android app is found.
// setConnectHeaders makes the request look like a real in-page browser fetch.
func setConnectHeaders(req *http.Request, base string) {
	req.Header.Set("User-Agent", connectUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	req.Header.Set("Referer", base+"/")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
}

var errTooManyRedirects = errors.New("слишком много редиректов")

func (a *App) tryFetchParse(ctx context.Context, client *http.Client, base, path, panelBase string) (*appConfigV2, *appConfig, bool) {
	full := base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, nil, false
	}
	setConnectHeaders(req, base)
	if k := a.caddyKeyFor(full, panelBase); k != "" {
		req.Header.Set("X-Api-Key", k)
	}
	resp, err := client.Do(req)
	if err != nil {
		a.log.Warn("miniapp connect: app-config fetch error", "url", full, "err", err)
		return nil, nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		a.log.Warn("miniapp connect: app-config non-200", "url", full, "status", resp.StatusCode)
		return nil, nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		a.log.Warn("miniapp connect: app-config read error", "url", full, "err", err)
		return nil, nil, false
	}
	if t := bytes.TrimSpace(body); len(t) == 0 || t[0] == '<' {
		a.log.Warn("miniapp connect: app-config not JSON (empty/HTML)", "url", full, "bytes", len(body))
		return nil, nil, false
	}
	var v2 appConfigV2
	errV2 := json.Unmarshal(body, &v2)
	if errV2 == nil && v2AppCount(&v2) > 0 {
		a.log.Info("miniapp connect: app-config loaded (v2)", "url", full, "platforms", len(v2.Platforms), "apps", v2AppCount(&v2))
		return &v2, nil, true
	}
	var std appConfig
	errStd := json.Unmarshal(body, &std)
	if errStd == nil && stdAppCount(&std) > 0 {
		a.log.Info("miniapp connect: app-config loaded (standard)", "url", full, "platforms", len(std.Platforms), "apps", stdAppCount(&std))
		return nil, &std, true
	}
	// Обе схемы мимо. Тексты ошибок разбора обязательны в логе: без них
	// «нет приложений» одинаково выглядит и когда конфиг чужого формата, и
	// когда одно поле разъехалось по типу и уронило разбор целиком.
	a.log.Warn("miniapp connect: app-config parsed but has no apps",
		"url", full, "bytes", len(body), "v2_err", errV2, "std_err", errStd)
	return nil, nil, false
}

// fetchAppConfig returns the parsed config for the subscription host, trying
// the known paths, cached for appConfigTTL with stale-on-error fallback.
func (a *App) fetchAppConfig(ctx context.Context, base, subURL string) *connectCacheEntry {
	a.connectMu.Lock()
	ce := a.connectCache
	failAt := a.connectFail[base+"|"+subURL]
	a.connectMu.Unlock()
	if ce != nil && ce.base == base && time.Since(ce.fetchedAt) < appConfigTTL {
		return ce
	}
	// Отрицательный ответ тоже кэшируем. Пока этого не было, каждое нажатие
	// «Подключить» на лежащей странице подписки честно ждало полный обход всех
	// путей — по 4 секунды на каждый, — и так у каждого человека.
	//
	// Протухшая положительная запись отдаётся тут же: она лучше и пустоты, и
	// четырёх секунд ожидания. Раньше проверка отказа стояла до неё и на
	// боте, который хоть раз видел конфиг, не срабатывала никогда.
	if !failAt.IsZero() && time.Since(failAt) < appConfigFailTTL {
		if ce != nil && ce.base == base {
			return ce
		}
		return nil
	}

	// Subscription pages gate the app-config behind a session cookie the server
	// sets when the page is loaded (HttpOnly), returning 404 to cookieless
	// requests. So prime a cookie jar by loading the user's sub page first, then
	// fetch the asset with that cookie — exactly what a real browser does.
	jar, _ := cookiejar.New(nil)
	panelBase := a.panelBaseURL()
	client := &http.Client{
		Timeout: 4 * time.Second,
		Jar:     jar,
		// Этот клиент, в отличие от панельного, по редиректам ходит (страницы
		// подписки их любят). Go при смене хоста снимает только Authorization и
		// Cookie, а произвольные заголовки тащит дальше — X-Api-Key от Caddy так
		// уехал бы на чужой домен. Снимаем его сами на каждом переходе, который
		// уводит с панели.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errTooManyRedirects
			}
			if req.Header.Get("X-Api-Key") != "" && a.caddyKeyFor(req.URL.String(), panelBase) == "" {
				req.Header.Del("X-Api-Key")
			}
			return nil
		},
	}
	if subURL != "" {
		if req, err := http.NewRequestWithContext(ctx, http.MethodGet, subURL, nil); err == nil {
			// Full browser *navigation* headers: the subscription middleware returns
			// 404 to anything that doesn't look like a real browser visit.
			req.Header.Set("User-Agent", connectUA)
			req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
			req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
			req.Header.Set("Sec-Fetch-Dest", "document")
			req.Header.Set("Sec-Fetch-Mode", "navigate")
			req.Header.Set("Sec-Fetch-Site", "none")
			req.Header.Set("Sec-Fetch-User", "?1")
			req.Header.Set("Upgrade-Insecure-Requests", "1")
			if k := a.caddyKeyFor(subURL, panelBase); k != "" {
				req.Header.Set("X-Api-Key", k)
			}
			if resp, err := client.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
				_ = resp.Body.Close()
			}
		}
	}

	for _, p := range appConfigPaths {
		// Ждать нечего, если время запроса уже вышло: следующий путь всё равно
		// упрётся в тот же дедлайн, а человек тем временем смотрит в пустоту.
		if ctx.Err() != nil {
			break
		}
		v2, std, ok := a.tryFetchParse(ctx, client, base, p, panelBase)
		if ok {
			ne := &connectCacheEntry{base: base, v2: v2, std: std, fetchedAt: time.Now()}
			a.connectMu.Lock()
			a.connectCache = ne
			delete(a.connectFail, base+"|"+subURL)
			a.connectMu.Unlock()
			return ne
		}
	}
	a.log.Warn("miniapp connect: no app-config found on subscription host", "base", base)
	// Оборванный запрос (человек закрыл мини-апп) — не отказ страницы:
	// запомнить его значило бы на минуту сказать «приложений нет» всем
	// остальным, у кого страница работает.
	if ctx.Err() == nil {
		a.connectMu.Lock()
		if a.connectFail == nil {
			a.connectFail = map[string]time.Time{}
		}
		now := time.Now()
		// Карта живёт минуту — подметаем её здесь же, чтобы не копить по
		// записи на каждую ссылку подписки.
		for k, t := range a.connectFail {
			if now.Sub(t) > appConfigFailTTL {
				delete(a.connectFail, k)
			}
		}
		a.connectFail[base+"|"+subURL] = now
		a.connectMu.Unlock()
	}
	if ce != nil && ce.base == base {
		return ce
	}
	return nil
}

// subpageOffFor — на сколько бот перестаёт спрашивать панель про конфиг
// приложений после того, как она ответила «такого API нет».
const subpageOffFor = time.Hour

// subpageRetryIn — пауза после временной осечки панели (нет прав у токена,
// панель недоступна): фолбэк на страницу подписки работает, а долбить панель
// на каждый заход незачем.
const subpageRetryIn = 10 * time.Minute

// shortUUIDFromSub достаёт короткий идентификатор подписки из её ссылки —
// на случай, если панель не положила его в карточку пользователя.
func shortUUIDFromSub(subURL string) string {
	u, err := url.Parse(subURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if p := strings.TrimSpace(parts[i]); p != "" {
			return p
		}
	}
	return ""
}

// panelCfgCache — разобранные конфиги страницы подписки из панели. Ключ —
// UUID конфига: панель умеет держать несколько и назначать разным подпискам
// разные, поэтому одним слотом тут не обойтись — иначе человек увидит чужой
// набор приложений.
type panelCfgCache struct {
	fetchedAt time.Time
	byUUID    map[string]*connectCacheEntry
	firstUUID string
}

// fetchPanelAppConfig берёт конфиг приложений из панели (3.0.0+). nil означает
// «этим путём не вышло» — вызывающий идёт на саму страницу подписки.
func (a *App) fetchPanelAppConfig(ctx context.Context, panel *remnawave.Client, shortUUID, subURL string) *connectCacheEntry {
	if panel == nil {
		return nil
	}
	if shortUUID == "" {
		shortUUID = shortUUIDFromSub(subURL)
	}

	a.connectMu.Lock()
	pc := a.panelCfgs
	off := a.subpageOffUntil
	a.connectMu.Unlock()

	// Кэш проверяем ДО паузы: осечка панели не повод прятать конфиг, который
	// уже разобран и лежит рядом.
	if pc == nil || time.Since(pc.fetchedAt) >= appConfigTTL {
		if !off.IsZero() && time.Now().Before(off) {
			return nil
		}
		var err error
		if pc, err = a.loadPanelCfgs(ctx, panel); err != nil {
			return nil
		}
	}
	if pc == nil || len(pc.byUUID) == 0 {
		return nil
	}

	// Какой конфиг назначен именно этой подписке. Маршрут необязательный: если
	// панель на него не отвечает, берём первый (он же дефолтный).
	uuid := ""
	if len(pc.byUUID) > 1 {
		if u, err := panel.SubpageConfigUUIDFor(ctx, shortUUID); err == nil {
			uuid = u
		}
	}
	if ce := pc.byUUID[uuid]; ce != nil {
		return ce
	}
	return pc.byUUID[pc.firstUUID]
}

// loadPanelCfgs тянет и разбирает все конфиги страницы подписки разом: их
// единицы, а список отдаётся одним запросом.
func (a *App) loadPanelCfgs(ctx context.Context, panel *remnawave.Client) (*panelCfgCache, error) {
	list, err := panel.SubpageConfigs(ctx)
	if err != nil {
		// Отмена запроса (человек закрыл мини-апп) — не повод объявлять панель
		// неработающей для всех остальных.
		if ctx.Err() != nil {
			return nil, err
		}
		// Нет такого API (панель до 3.0.0) — молча и надолго. Прочее (нет прав
		// у токена, панель прилегла) — с записью в лог и ненадолго, но тоже с
		// паузой: иначе каждый заход в «Подключить» бьёт в панель впустую.
		pause := subpageOffFor
		if !remnawave.ErrNoSubpageAPI(err) {
			pause = subpageRetryIn
			a.log.Warn("miniapp connect: конфиг приложений из панели", "err", err)
		}
		a.connectMu.Lock()
		a.subpageOffUntil = time.Now().Add(pause)
		a.connectMu.Unlock()
		return nil, err
	}

	pc := &panelCfgCache{fetchedAt: time.Now(), byUUID: map[string]*connectCacheEntry{}}
	for i := range list {
		cfg := &list[i]
		ce := parsePanelCfg(cfg)
		if ce == nil {
			if len(cfg.Config) > 0 {
				a.log.Warn("miniapp connect: конфиг из панели без приложений", "config", cfg.Name, "bytes", len(cfg.Config))
			}
			continue
		}
		pc.byUUID[cfg.UUID] = ce
		if pc.firstUUID == "" {
			pc.firstUUID = cfg.UUID
			a.log.Info("miniapp connect: конфиг приложений получен из панели", "config", cfg.Name, "apps", ceAppCount(ce))
		}
	}
	if len(pc.byUUID) == 0 {
		// Панель API отдала, но приложений в ней нет — не долбим её в цикле.
		a.connectMu.Lock()
		a.subpageOffUntil = time.Now().Add(subpageRetryIn)
		a.connectMu.Unlock()
		return nil, errNoPanelApps
	}
	a.connectMu.Lock()
	a.panelCfgs = pc
	a.subpageOffUntil = time.Time{}
	a.connectMu.Unlock()
	return pc, nil
}

var errNoPanelApps = errors.New("в панели нет конфигов страницы подписки с приложениями")

// parsePanelCfg разбирает сырой конфиг панели любой из двух схем.
func parsePanelCfg(cfg *remnawave.SubpageConfig) *connectCacheEntry {
	if cfg == nil || len(cfg.Config) == 0 {
		return nil
	}
	ce := &connectCacheEntry{base: "panel:" + cfg.UUID, fetchedAt: time.Now()}
	var v2 appConfigV2
	if json.Unmarshal(cfg.Config, &v2) == nil && v2AppCount(&v2) > 0 {
		ce.v2 = &v2
		return ce
	}
	var std appConfig
	if json.Unmarshal(cfg.Config, &std) == nil && stdAppCount(&std) > 0 {
		ce.std = &std
		return ce
	}
	return nil
}

func ceAppCount(ce *connectCacheEntry) int {
	switch {
	case ce == nil:
		return 0
	case ce.v2 != nil:
		return v2AppCount(ce.v2)
	case ce.std != nil:
		return stdAppCount(ce.std)
	}
	return 0
}

// acBuildStd maps standard-schema apps to DTOs, featured-first.
func acBuildStd(apps []acApp, subURL, lang string) []web.MiniConnectAppDTO {
	var featured, rest []web.MiniConnectAppDTO
	for _, ap := range apps {
		dto := web.MiniConnectAppDTO{
			Name:     ap.Name,
			Featured: ap.IsFeatured,
			AddDesc:  localize(ap.AddSubscriptionStep.Description, lang),
		}
		dto.Deeplink, _ = acLink(buildDeeplink(ap.URLScheme, subURL))
		for _, b := range ap.InstallationStep.Buttons {
			if link, ok := acLink(b.ButtonLink); ok {
				dto.Installs = append(dto.Installs, web.MiniConnectButtonDTO{Text: localize(b.ButtonText, lang), URL: link})
			}
		}
		if dto.Deeplink == "" && len(dto.Installs) == 0 {
			continue
		}
		if ap.IsFeatured {
			featured = append(featured, dto)
		} else {
			rest = append(rest, dto)
		}
	}
	return append(featured, rest...)
}

var (
	acBreakRe = regexp.MustCompile(`(?i)<br\s*/?>`)
	acTagRe   = regexp.MustCompile(`</?[a-zA-Z][^<>]*>`)
)

// acText — текст блока простым текстом. Страница подписки выводит название и
// описание блока как HTML, поэтому в конфиге встречается разметка: перенос
// строки остаётся переносом, остальные теги убираются.
func acText(s string) string {
	s = acBreakRe.ReplaceAllString(s, "\n")
	return strings.TrimSpace(html.UnescapeString(acTagRe.ReplaceAllString(s, "")))
}

// acLink — ссылка кнопки в том виде, в каком её прочтёт браузер: он отбрасывает
// пробелы и управляющие символы по краям, а табуляцию и переводы строки — в
// любом месте. Прочие пробельные символы по краям убираются тоже: страница
// может обрезать ссылку сама. Пустая ссылка и ссылка со схемой, исполняющей
// код в странице, не годятся.
func acLink(link string) (string, bool) {
	s := strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, link)
	s = strings.TrimFunc(s, func(r rune) bool { return r <= ' ' || r == '\ufeff' || unicode.IsSpace(r) })
	if s == "" {
		return "", false
	}
	switch strings.ToLower(acScheme(s)) {
	case "javascript", "data", "vbscript", "file":
		return "", false
	}
	return s, true
}

// acScheme — схема ссылки: буква, за ней буквы, цифры, «+», «-», «.» до
// двоеточия. У ссылки без схемы — пусто.
func acScheme(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ':':
			return s[:i]
		case c|0x20 >= 'a' && c|0x20 <= 'z':
		case i > 0 && strings.IndexByte("0123456789+-.", c) >= 0:
		default:
			return ""
		}
	}
	return ""
}

// acCryptKeys — шаблоны, которые страница подписки заменяет зашифрованной
// ссылкой. Бот такие ссылки не строит: кнопка с ними не показывается.
var acCryptKeys = []string{"{{HAPP_CRYPT3_LINK}}", "{{HAPP_CRYPT4_LINK}}", "{{INCY_CRYPT1_LINK}}"}

// Виды кнопок блока.
const (
	acBtnAdd  = iota // добавить подписку в приложение
	acBtnLink        // внешняя ссылка
	acBtnCopy        // скопировать ссылку
)

// acBtnKinds — виды кнопок по их типу в конфиге страницы подписки.
var acBtnKinds = map[string]int{"subscriptionLink": acBtnAdd, "external": acBtnLink, "copyButton": acBtnCopy}

// acBtn — кнопка блока после разбора: вид известен, ссылка подставлена и
// проверена.
type acBtn struct {
	kind int
	text string
	url  string
}

// acBlk — блок инструкции после разбора. Всё, что решается о блоке дальше,
// решается по этому виду, а не по исходному конфигу.
type acBlk struct {
	title, text string
	btns        []acBtn
	// lost — у блока была кнопка, которая в разбор не попала.
	lost bool
}

func (b acBlk) has(kind int) bool {
	for _, x := range b.btns {
		if x.kind == kind {
			return true
		}
	}
	return false
}

// acParseBlocks разбирает блоки приложения. Название и описание страница
// подписки выводит как HTML, подпись кнопки — как текст. Кнопка неизвестного
// вида или с негодной ссылкой в разбор не попадает.
func acParseBlocks(blocks []acV2Block, subURL, username, lang string) []acBlk {
	out := make([]acBlk, 0, len(blocks))
	for _, bl := range blocks {
		blk := acBlk{title: acText(localize(bl.Title, lang)), text: acText(localize(bl.Description, lang))}
		for _, b := range bl.Buttons {
			kind, known := acBtnKinds[b.Type]
			link, ok := acLink(substituteV2(b.Link, subURL, username))
			if !known || !ok || acHasCryptKey(b.Link) {
				blk.lost = true
				continue
			}
			blk.btns = append(blk.btns, acBtn{kind: kind, text: strings.TrimSpace(localize(b.Text, lang)), url: link})
		}
		out = append(out, blk)
	}
	return out
}

func acHasCryptKey(link string) bool {
	for _, k := range acCryptKeys {
		if strings.Contains(link, k) {
			return true
		}
	}
	return false
}

// acIsSection — блок i показывается отдельным разделом («если подписка не
// добавилась», «подключение и использование» и подобные). Раздел — блок с
// названием и содержимым. Блок, от которого не осталось ни одной его кнопки,
// содержимым не считается: его текст говорит о кнопке, которой нет. С кнопкой
// добавления (блок add) разделы — всё после неё. Без неё кнопки-ссылки
// приложения остаются на виду, а разделами идут только блоки без таких кнопок
// после первого: текст и копирование ссылки.
func acIsSection(bl acBlk, i, add int) bool {
	if bl.title == "" || (len(bl.btns) == 0 && (bl.text == "" || bl.lost)) {
		return false
	}
	if add >= 0 {
		return i > add
	}
	return i > 0 && !bl.has(acBtnLink)
}

// acBuildV2 maps v2-schema apps to DTOs: кнопка добавления подписки — deeplink,
// внешние ссылки — кнопки установки, копирование — отдельные кнопки, блоки
// после добавления подписки — разделы (More).
func acBuildV2(apps []acV2App, subURL, username, lang string) []web.MiniConnectAppDTO {
	var featured, rest []web.MiniConnectAppDTO
	for _, ap := range apps {
		dto := web.MiniConnectAppDTO{Name: ap.Name, Featured: ap.Featured}
		blocks := acParseBlocks(ap.Blocks, subURL, username, lang)
		add := -1
		for i, bl := range blocks {
			if bl.has(acBtnAdd) {
				add = i
				break
			}
		}
		for i, bl := range blocks {
			if acIsSection(bl, i, add) {
				more := web.MiniConnectBlockDTO{Title: bl.title, Text: bl.text}
				for _, b := range bl.btns {
					more.Buttons = append(more.Buttons, web.MiniConnectButtonDTO{Text: b.text, URL: b.url, Copy: b.kind == acBtnCopy})
				}
				dto.More = append(dto.More, more)
				continue
			}
			for _, b := range bl.btns {
				switch b.kind {
				case acBtnAdd:
					if dto.Deeplink == "" {
						dto.Deeplink, dto.AddDesc = b.url, bl.text
					}
				case acBtnLink:
					dto.Installs = append(dto.Installs, web.MiniConnectButtonDTO{Text: b.text, URL: b.url})
				case acBtnCopy:
					dto.Copies = append(dto.Copies, web.MiniConnectButtonDTO{Text: b.text, URL: b.url, Copy: true})
				}
			}
		}
		if dto.Deeplink == "" && len(dto.Installs) == 0 {
			continue
		}
		if ap.Featured {
			featured = append(featured, dto)
		} else {
			rest = append(rest, dto)
		}
	}
	return append(featured, rest...)
}

// MiniConnect returns install apps + deeplinks for the user's subscription,
// sourced live from their subscription page's app-config (iOS + Android only).
func (a *App) MiniConnect(ctx context.Context, tgID int64) web.MiniConnectDTO {
	var dto web.MiniConnectDTO
	panel := a.panelClient()
	if panel == nil {
		return dto
	}
	u, err := panel.FindByTelegramID(ctx, tgID)
	if err != nil || u == nil || u.SubscriptionURL == "" {
		return dto
	}
	subURL := a.rewriteSub(u.SubscriptionURL)
	dto.SubURL = subURL
	dto.Username = u.Username
	// Панель 3.x хранит конфиг страницы подписки у себя и отдаёт его по токену.
	// Это основной источник: не нужны ни cookie-сессия страницы, ни обход её
	// защит. На панелях без такого API остаётся поход на саму страницу.
	ce := a.fetchPanelAppConfig(ctx, panel, u.ShortUUID, subURL)
	if ce == nil {
		base := appConfigBase(subURL)
		if base == "" {
			return dto
		}
		ce = a.fetchAppConfig(ctx, base, subURL)
	}
	if ce == nil {
		return dto
	}
	lang := a.lang(tgID)
	switch {
	case ce.v2 != nil:
		dto.Platforms = buildV2Platforms(ce.v2, subURL, u.Username, lang)
	case ce.std != nil:
		dto.Platforms = buildStdPlatforms(ce.std, subURL, lang)
	}
	// Keep the legacy mobile fields populated so the Telegram mini-app (iOS +
	// Android only) works unchanged; the web cabinet uses dto.Platforms (all).
	for _, p := range dto.Platforms {
		// Ключ платформы приходит как есть: страницы пишут и "ios", и "iOS".
		switch strings.ToLower(p.Key) {
		case "ios":
			dto.IOS = p.Apps
		case "android":
			dto.Android = p.Apps
		}
	}
	return dto

}

func v2AppCount(c *appConfigV2) int {
	n := 0
	for _, p := range c.Platforms {
		n += len(p.Apps)
	}
	return n
}

func stdAppCount(c *appConfig) int {
	n := 0
	for _, apps := range c.Platforms {
		n += len(apps)
	}
	return n
}

// buildV2Platforms builds the ordered, non-empty platform list from a v2 config.
func buildV2Platforms(c *appConfigV2, subURL, username, lang string) []web.MiniConnectPlatformDTO {
	present := map[string]bool{}
	for k := range c.Platforms {
		present[k] = true
	}
	var out []web.MiniConnectPlatformDTO
	for _, k := range orderedPlatformKeys(present) {
		p := c.Platforms[k]
		apps := acBuildV2(p.Apps, subURL, username, lang)
		if len(apps) == 0 {
			continue
		}
		out = append(out, web.MiniConnectPlatformDTO{Key: k, Label: platformLabel(k, p.DisplayName.pick(lang)), Apps: apps})
	}
	return out
}

// buildStdPlatforms builds the ordered, non-empty platform list from a standard config.
func buildStdPlatforms(c *appConfig, subURL, lang string) []web.MiniConnectPlatformDTO {
	present := map[string]bool{}
	for k := range c.Platforms {
		present[k] = true
	}
	var out []web.MiniConnectPlatformDTO
	for _, k := range orderedPlatformKeys(present) {
		apps := acBuildStd(c.Platforms[k], subURL, lang)
		if len(apps) == 0 {
			continue
		}
		out = append(out, web.MiniConnectPlatformDTO{Key: k, Label: platformLabel(k, ""), Apps: apps})
	}
	return out
}

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"

	"remnabot/internal/web"
)

// appConfigTTL bounds how long the subscription page's app-config is cached.
const appConfigTTL = 5 * time.Minute

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
	switch b[0] {
	case '"':
		return json.Unmarshal(b, &n.Plain)
	case '{':
		return json.Unmarshal(b, &n.Loc)
	}
	// Anything else (number, array) isn't a name — ignore it rather than fail
	// the whole config over a cosmetic field.
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
func substituteV2(tmpl, subURL, username string) string {
	s := strings.ReplaceAll(tmpl, "{{SUBSCRIPTION_LINK}}", subURL)
	s = strings.ReplaceAll(s, "{{USERNAME}}", username)
	return s
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
	a.connectMu.Unlock()
	if ce != nil && ce.base == base && time.Since(ce.fetchedAt) < appConfigTTL {
		return ce
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
		v2, std, ok := a.tryFetchParse(ctx, client, base, p, panelBase)
		if ok {
			ne := &connectCacheEntry{base: base, v2: v2, std: std, fetchedAt: time.Now()}
			a.connectMu.Lock()
			a.connectCache = ne
			a.connectMu.Unlock()
			return ne
		}
	}
	a.log.Warn("miniapp connect: no app-config found on subscription host", "base", base)
	if ce != nil && ce.base == base {
		return ce
	}
	return nil
}

// acBuildStd maps standard-schema apps to DTOs, featured-first.
func acBuildStd(apps []acApp, subURL, lang string) []web.MiniConnectAppDTO {
	var featured, rest []web.MiniConnectAppDTO
	for _, ap := range apps {
		dto := web.MiniConnectAppDTO{
			Name:     ap.Name,
			Featured: ap.IsFeatured,
			Deeplink: buildDeeplink(ap.URLScheme, subURL),
			AddDesc:  localize(ap.AddSubscriptionStep.Description, lang),
		}
		for _, b := range ap.InstallationStep.Buttons {
			if b.ButtonLink == "" {
				continue
			}
			dto.Installs = append(dto.Installs, web.MiniConnectButtonDTO{Text: localize(b.ButtonText, lang), URL: b.ButtonLink})
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

// acBuildV2 maps v2-schema apps to DTOs: the subscriptionLink button becomes the
// deeplink (placeholders substituted), external buttons become install links.
func acBuildV2(apps []acV2App, subURL, username, lang string) []web.MiniConnectAppDTO {
	var featured, rest []web.MiniConnectAppDTO
	for _, ap := range apps {
		dto := web.MiniConnectAppDTO{Name: ap.Name, Featured: ap.Featured}
		for _, bl := range ap.Blocks {
			for _, b := range bl.Buttons {
				switch b.Type {
				case "subscriptionLink":
					if dto.Deeplink == "" && b.Link != "" {
						dto.Deeplink = substituteV2(b.Link, subURL, username)
						if dto.AddDesc == "" {
							dto.AddDesc = localize(bl.Description, lang)
						}
					}
				case "external":
					if b.Link != "" {
						dto.Installs = append(dto.Installs, web.MiniConnectButtonDTO{
							Text: localize(b.Text, lang),
							URL:  substituteV2(b.Link, subURL, username),
						})
					}
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
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
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
	base := appConfigBase(subURL)
	if base == "" {
		return dto
	}
	ce := a.fetchAppConfig(ctx, base, subURL)
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
		switch p.Key {
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

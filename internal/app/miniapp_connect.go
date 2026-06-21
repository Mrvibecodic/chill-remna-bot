package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"remnabot/internal/web"
)

// appConfigTTL bounds how long the subscription page's app-config is cached.
const appConfigTTL = 6 * time.Hour

// connectHTTP fetches the public app-config from the subscription page host.
var connectHTTP = &http.Client{Timeout: 4 * time.Second}

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
	Platforms struct {
		IOS     []acApp `json:"ios"`
		Android []acApp `json:"android"`
	} `json:"platforms"`
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

type acV2Platform struct {
	Apps []acV2App `json:"apps"`
}

type appConfigV2 struct {
	Platforms struct {
		IOS     acV2Platform `json:"ios"`
		Android acV2Platform `json:"android"`
	} `json:"platforms"`
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
func (a *App) tryFetchParse(ctx context.Context, base, path string) (*appConfigV2, *appConfig, bool) {
	full := base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, nil, false
	}
	// Full browser-like headers: some subscription hosts only serve the asset to
	// requests that look like a real browser navigation from the page itself.
	req.Header.Set("User-Agent", connectUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")
	req.Header.Set("Referer", base+"/")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := connectHTTP.Do(req)
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
	if json.Unmarshal(body, &v2) == nil && (len(v2.Platforms.IOS.Apps) > 0 || len(v2.Platforms.Android.Apps) > 0) {
		a.log.Info("miniapp connect: app-config loaded (v2)", "url", full, "ios", len(v2.Platforms.IOS.Apps), "android", len(v2.Platforms.Android.Apps))
		return &v2, nil, true
	}
	var std appConfig
	if json.Unmarshal(body, &std) == nil && (len(std.Platforms.IOS) > 0 || len(std.Platforms.Android) > 0) {
		a.log.Info("miniapp connect: app-config loaded (standard)", "url", full, "ios", len(std.Platforms.IOS), "android", len(std.Platforms.Android))
		return nil, &std, true
	}
	a.log.Warn("miniapp connect: app-config parsed but has no apps", "url", full, "bytes", len(body))
	return nil, nil, false
}

// fetchAppConfig returns the parsed config for the subscription host, trying
// the known paths, cached for appConfigTTL with stale-on-error fallback.
func (a *App) fetchAppConfig(ctx context.Context, base string) *connectCacheEntry {
	a.connectMu.Lock()
	ce := a.connectCache
	a.connectMu.Unlock()
	if ce != nil && ce.base == base && time.Since(ce.fetchedAt) < appConfigTTL {
		return ce
	}
	for _, p := range appConfigPaths {
		v2, std, ok := a.tryFetchParse(ctx, base, p)
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
	ce := a.fetchAppConfig(ctx, base)
	if ce == nil {
		return dto
	}
	lang := a.lang(tgID)
	switch {
	case ce.v2 != nil:
		dto.Android = acBuildV2(ce.v2.Platforms.Android.Apps, subURL, u.Username, lang)
		dto.IOS = acBuildV2(ce.v2.Platforms.IOS.Apps, subURL, u.Username, lang)
	case ce.std != nil:
		dto.Android = acBuildStd(ce.std.Platforms.Android, subURL, lang)
		dto.IOS = acBuildStd(ce.std.Platforms.IOS, subURL, lang)
	}
	return dto
}

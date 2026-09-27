package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/web"
)

// Дизайн мини-аппа и веб-кабинета: выбор между классическим и новым
// (минималистичным) и брендирование страниц — название, фирменный цвет и
// логотип провайдера. Всё применяется на лету: страницы читают настройки на
// каждом запросе.

const cbWebUI = "wui"

const (
	// brandLogoMax — предел размера логотипа. Знак в шапке — это килобайты;
	// мегабайт с запасом покрывает и нежатый PNG.
	brandLogoMax = 1 << 20
	// webNameMax — длина названия в шапке, в символах.
	webNameMax = 40
	// brandRetry — через сколько повторять скачивание логотипа после сбоя.
	brandRetry = time.Minute
)

// brandLogoEntry — логотип, скачанный у Telegram (или неудачная попытка).
type brandLogoEntry struct {
	data     []byte
	failedAt time.Time
}

// webUICfg — настройки оформления с допустимыми значениями.
func (a *App) webUICfg() model.WebUIConfig {
	c, _ := a.webUICfgLang()
	return c
}

// webUICfgLang — то же и язык бота, одним взятием замка.
func (a *App) webUICfgLang() (model.WebUIConfig, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.WebUIConfig{}.Normalized(), ""
	}
	return a.botCfg.WebUI.Normalized(), a.botCfg.Language
}

// WebUI отдаёт веб-серверу выбранный дизайн и брендирование.
func (a *App) WebUI() web.WebUIDTO {
	c, lang := a.webUICfgLang()
	return web.WebUIDTO{
		Design:   c.Design,
		Theme:    c.Theme,
		Name:     c.Name,
		Accent:   c.Accent,
		Logo:     brandLogoSrc(c.LogoFile, c.LogoURL, "/brand/logo"),
		LogoDark: brandLogoSrc(c.LogoDarkFile, c.LogoDarkURL, "/brand/logo-dark"),
		Lang:     lang,
	}
}

// brandLogoSrc — адрес логотипа для страницы. Загруженный отдаёт сам бот; к
// адресу добавлен отпечаток файла, чтобы замена логотипа не упиралась в кэш
// браузера.
func brandLogoSrc(file, link, route string) string {
	if file != "" {
		sum := sha256.Sum256([]byte(file))
		return route + "?v=" + hex.EncodeToString(sum[:4])
	}
	return link
}

// BrandLogo — байты загруженного логотипа. Скачиваются у Telegram при первом
// показе и держатся в памяти; неудача повторяется не чаще раза в минуту, чтобы
// поток запросов страницы не превращался в поток запросов к Telegram.
// Скачивание одно на всех (brandFetchMu) и не зависит от запроса: закрытая
// вкладка не должна оставлять логотип пустым для всех на минуту.
func (a *App) BrandLogo(_ context.Context, dark bool) ([]byte, bool) {
	c := a.webUICfg()
	file := c.LogoFile
	if dark {
		file = c.LogoDarkFile
	}
	if file == "" {
		return nil, false
	}
	cached := func() ([]byte, bool, bool) {
		a.webMu.Lock()
		defer a.webMu.Unlock()
		e, ok := a.brandCache[file]
		if ok && e.data != nil {
			return e.data, true, true
		}
		return nil, false, ok && time.Since(e.failedAt) < brandRetry
	}
	if data, ok, skip := cached(); ok || skip {
		return data, ok
	}
	a.brandFetchMu.Lock()
	defer a.brandFetchMu.Unlock()
	if data, ok, skip := cached(); ok || skip {
		return data, ok
	}
	fctx, cancel := context.WithTimeout(a.bgContext(), 20*time.Second)
	defer cancel()
	data, err := a.fetchBrandLogo(fctx, file)
	a.webMu.Lock()
	if a.brandCache == nil {
		a.brandCache = map[string]brandLogoEntry{}
	}
	if err != nil {
		a.brandCache[file] = brandLogoEntry{failedAt: time.Now()}
	} else {
		a.brandCache[file] = brandLogoEntry{data: data}
	}
	a.webMu.Unlock()
	if err != nil {
		a.log.Warn("логотип не скачан", "err", err)
		return nil, false
	}
	return data, true
}

// fetchBrandLogo скачивает файл логотипа и проверяет, что это картинка
// допустимого вида и размера.
func (a *App) fetchBrandLogo(ctx context.Context, file string) ([]byte, error) {
	data, err := a.msg.Download(ctx, file)
	if err != nil {
		return nil, err
	}
	if len(data) > brandLogoMax {
		return nil, errBrandLogoBig
	}
	if _, ok := web.SniffImage(data); !ok {
		return nil, errBrandLogoType
	}
	return data, nil
}

var (
	errBrandLogoBig  = errors.New("логотип больше предела")
	errBrandLogoType = errors.New("логотип не картинка допустимого вида")
)

// --- админка: Интерфейс → Дизайн мини-аппа и кабинета ---

func (a *App) showWebUIAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	c := a.webUICfg()
	logoState := func(file, link string) string {
		switch {
		case file != "":
			return i18n.T(lang, "webui.logo_uploaded")
		case link != "":
			return i18n.T(lang, "webui.logo_link")
		}
		return i18n.T(lang, "webui.logo_none")
	}
	text := i18n.T(lang, "webui.title",
		i18n.T(lang, "webui.design_"+c.Design),
		i18n.T(lang, "webui.theme_"+c.Theme),
		escapeName(firstNonEmpty(c.Name, "—")),
		firstNonEmpty(c.Accent, i18n.T(lang, "webui.accent_default")),
		logoState(c.LogoFile, c.LogoURL),
		logoState(c.LogoDarkFile, c.LogoDarkURL),
	)
	if c.Design == model.WebDesignClassic {
		text += "\n\n" + i18n.T(lang, "webui.classic_note")
	}
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "webui.btn_design", i18n.T(lang, "webui.design_"+c.Design)), cbWebUI+":design")},
		{btn(i18n.T(lang, "webui.btn_theme", i18n.T(lang, "webui.theme_"+c.Theme)), cbWebUI+":theme")},
		{btn(i18n.T(lang, "webui.btn_name"), cbWebUI+":name"), btn(i18n.T(lang, "webui.btn_accent"), cbWebUI+":accent")},
		{btn(i18n.T(lang, "webui.btn_logo"), cbWebUI+":logo"), btn(i18n.T(lang, "webui.btn_logo_url"), cbWebUI+":logourl")},
		{btn(i18n.T(lang, "webui.btn_logod"), cbWebUI+":logod"), btn(i18n.T(lang, "webui.btn_logod_url"), cbWebUI+":logodurl")},
	}
	if c.LogoFile != "" || c.LogoURL != "" || c.LogoDarkFile != "" || c.LogoDarkURL != "" {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "webui.btn_logo_reset"), cbWebUI+":logoreset")})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:iface"), btn(i18n.T(lang, "btn.home"), "menu:home"),
	})
	a.sendIfaceKB(ctx, chatID, text, rows)
}

// editWebUI меняет настройки оформления под замком и сохраняет конфиг.
func (a *App) editWebUI(ctx context.Context, fn func(w *model.WebUIConfig)) {
	a.mu.Lock()
	if a.botCfg != nil {
		fn(&a.botCfg.WebUI)
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
}

func (a *App) onWebUIAdmin(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	switch val {
	case "design":
		a.editWebUI(ctx, func(w *model.WebUIConfig) {
			if w.Normalized().Design == model.WebDesignMinimal {
				w.Design = model.WebDesignClassic
			} else {
				w.Design = model.WebDesignMinimal
			}
		})
	case "theme":
		a.editWebUI(ctx, func(w *model.WebUIConfig) {
			switch w.Normalized().Theme {
			case model.WebThemeAuto:
				w.Theme = model.WebThemeLight
			case model.WebThemeLight:
				w.Theme = model.WebThemeDark
			default:
				w.Theme = model.WebThemeAuto
			}
		})
	case "name", "accent", "logourl", "logodurl":
		ui.adminInput = "wui_" + val
		a.askInput(ctx, chatID, i18n.T(lang, "webui.ask_"+val), "menu:webui")
		return
	case "logo", "logod":
		ui.adminInput = ""
		ui.awaitLogo = "light"
		if val == "logod" {
			ui.awaitLogo = "dark"
		}
		a.sendKB(ctx, chatID, i18n.T(lang, "webui.ask_logo"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.cancel"), cbWebUI+":cancel")},
		})
		return
	case "cancel":
		ui.awaitLogo = ""
	case "logoreset":
		a.editWebUI(ctx, func(w *model.WebUIConfig) {
			w.LogoFile, w.LogoURL, w.LogoDarkFile, w.LogoDarkURL = "", "", "", ""
		})
	}
	a.showWebUIAdmin(ctx, chatID)
}

// setWebUIText сохраняет текстовое поле оформления. «-» сбрасывает значение.
func (a *App) setWebUIText(ctx context.Context, chatID int64, field, text string) {
	lang := a.lang(chatID)
	v := strings.TrimSpace(text)
	reset := v == "-"
	if reset {
		v = ""
	}
	retry := func(key string) {
		a.getUI(chatID).adminInput = "wui_" + field
		a.askInput(ctx, chatID, i18n.T(lang, key)+"\n\n"+i18n.T(lang, "webui.ask_"+field), "menu:webui")
	}
	switch field {
	case "name":
		if utf8.RuneCountInString(v) > webNameMax {
			retry("webui.bad_name")
			return
		}
		a.editWebUI(ctx, func(w *model.WebUIConfig) { w.Name = v })
	case "accent":
		c := model.NormalizeHexColor(v)
		if !reset && c == "" {
			retry("webui.bad_accent")
			return
		}
		a.editWebUI(ctx, func(w *model.WebUIConfig) { w.Accent = c })
	case "logourl", "logodurl":
		if !reset && !validLogoURL(v) {
			retry("webui.bad_url")
			return
		}
		a.editWebUI(ctx, func(w *model.WebUIConfig) {
			if field == "logourl" {
				w.LogoURL, w.LogoFile = v, ""
			} else {
				w.LogoDarkURL, w.LogoDarkFile = v, ""
			}
		})
	}
	a.showWebUIAdmin(ctx, chatID)
}

// validLogoURL — ссылка на логотип: https-адрес или путь на этом же сайте
// (например, файл из папки своей статики).
func validLogoURL(s string) bool {
	if len(s) > 500 || strings.ContainsAny(s, " \t\r\n\"'<>\\") {
		return false
	}
	if strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") {
		return true
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// setWebUILogoFile принимает логотип, присланный в чат. Файл сразу скачивается
// и проверяется: сохранить заведомо негодную картинку значило бы узнать об
// этом только по пустой шапке у покупателей.
func (a *App) setWebUILogoFile(ctx context.Context, chatID int64, fileID string, size int64) {
	ui := a.getUI(chatID)
	which := ui.awaitLogo
	ui.awaitLogo = ""
	lang := a.lang(chatID)
	fail := func(key string) {
		ui.awaitLogo = which
		a.sendKB(ctx, chatID, i18n.T(lang, key), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.cancel"), cbWebUI+":cancel")},
		})
	}
	if size > brandLogoMax {
		fail("webui.logo_big")
		return
	}
	data, err := a.fetchBrandLogo(ctx, fileID)
	switch {
	case errors.Is(err, errBrandLogoBig):
		fail("webui.logo_big")
		return
	case errors.Is(err, errBrandLogoType):
		fail("webui.logo_type")
		return
	case err != nil:
		a.log.Warn("логотип не скачан", "err", err)
		fail("webui.logo_fail")
		return
	}
	a.webMu.Lock()
	if a.brandCache == nil {
		a.brandCache = map[string]brandLogoEntry{}
	}
	a.brandCache[fileID] = brandLogoEntry{data: data}
	a.webMu.Unlock()
	a.editWebUI(ctx, func(w *model.WebUIConfig) {
		if which == "dark" {
			w.LogoDarkFile, w.LogoDarkURL = fileID, ""
		} else {
			w.LogoFile, w.LogoURL = fileID, ""
		}
	})
	a.showWebUIAdmin(ctx, chatID)
}

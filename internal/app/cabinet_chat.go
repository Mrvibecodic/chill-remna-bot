package app

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"strings"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Сторонний чат поддержки в веб-кабинете. Администратор вставляет код чата или
// адрес его скрипта; хранится только адрес, тег на странице собирает сам
// кабинет. Произвольная разметка в страницу не попадает.

const chatSrcMax = 500

var chatScriptSrc = regexp.MustCompile(`(?is)<script\b[^>]*?\ssrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

// parseChatSrc достаёт адрес скрипта из кода вставки или из голого адреса.
func parseChatSrc(input string) (string, bool) {
	v := strings.TrimSpace(input)
	if strings.Contains(v, "<") {
		m := chatScriptSrc.FindStringSubmatch(v)
		if m == nil {
			return "", false
		}
		v = html.UnescapeString(strings.TrimSpace(m[1] + m[2] + m[3]))
	}
	if strings.HasPrefix(v, "//") {
		v = "https:" + v
	}
	if !validChatSrc(v) {
		return "", false
	}
	return v, true
}

// validChatSrc — только https и настоящий домен: скрипт исполняется на странице
// кабинета, по http его подменил бы любой узел на пути.
func validChatSrc(raw string) bool {
	if raw == "" || len(raw) > chatSrcMax || hasInvisible(raw) || strings.ContainsAny(raw, `"'<>\`) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := u.Hostname()
	return host != "" && strings.Contains(host, ".")
}

// cabinetChat — чат из настроек; адрес, не прошедший проверку (правка конфига
// руками, старый бэкап), считается незаданным.
func cabinetChat(cab model.CabinetConfig) (src string, all bool) {
	if !validChatSrc(cab.ChatSrc) {
		return "", false
	}
	return cab.ChatSrc, cab.ChatAll
}

// CabinetChat — адрес скрипта стороннего чата и виден ли он до входа.
func (a *App) CabinetChat() (string, bool) { return cabinetChat(a.cabinetCfg()) }

func (a *App) setCabinetChat(ctx context.Context, chatID int64, text string) {
	src := ""
	if strings.TrimSpace(text) != "-" {
		var ok bool
		if src, ok = parseChatSrc(text); !ok {
			lang := a.lang(chatID)
			a.getUI(chatID).adminInput = "cab_chat"
			a.askInput(ctx, chatID, i18n.T(lang, "cabinet.bad_chat")+"\n\n"+i18n.T(lang, "cabinet.ask_chat"), "menu:cabinet")
			return
		}
	}
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.NormalizeCabinet()
		a.botCfg.Cabinet.ChatSrc = src
		if src == "" {
			a.botCfg.Cabinet.ChatAll = false
		}
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showCabinetAdmin(ctx, chatID)
}

func (a *App) toggleCabinetChatAll(ctx context.Context, chatID int64) {
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.NormalizeCabinet()
		a.botCfg.Cabinet.ChatAll = !a.botCfg.Cabinet.ChatAll
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showCabinetAdmin(ctx, chatID)
}

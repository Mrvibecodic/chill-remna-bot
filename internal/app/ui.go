package app

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

//go:embed banner_default.jpg
var defaultBanner []byte

// botEmojis — эмодзи в сообщениях бота и где применяются (для /emoji).
var botEmojis = []struct{ E, Use string }{
	{"👋", "приветствие"}, {"✅", "успех / подтверждение"}, {"❌", "ошибка / отказ"},
	{"⚠️", "предупреждение"}, {"⏳", "ожидание"}, {"🕒", "на проверке"},
	{"💳", "оплата"}, {"📦", "тариф"}, {"💸", "платёж"}, {"🔒", "доступ"},
	{"🔔", "уведомление"}, {"📸", "скриншот"}, {"📊", "статус"}, {"🌐", "удалённо"},
	{"🏠", "локально"}, {"🔑", "токен"}, {"🍪", "кука"},
}

func (a *App) botLang() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg != nil && a.botCfg.Language != "" {
		return a.botCfg.Language
	}
	return i18n.Fallback
}

func displayName(first, username string) string {
	if first != "" {
		return first
	}
	if username != "" {
		return "@" + username
	}
	return "друг"
}

func homeRow(lang string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.home"), "menu:home")}
}

func (a *App) userMenuRows(lang string) [][]models.InlineKeyboardButton {
	return [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
	}
}

func (a *App) adminMenuRows(lang string) [][]models.InlineKeyboardButton {
	return [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.buy"), "menu:buy"), btn(i18n.T(lang, "btn.status"), "menu:status")},
		{btn(i18n.T(lang, "btn.p2p"), "menu:p2p"), btn(i18n.T(lang, "btn.emoji"), "menu:emoji")},
		{btn(i18n.T(lang, "btn.banner"), "menu:welcome"), btn(i18n.T(lang, "btn.update"), "menu:update")},
	}
}

// --- стартовый баннер / меню ---

func (a *App) welcomeContent(name string) (models.InputFile, string, []models.MessageEntity) {
	a.mu.Lock()
	var w model.WelcomeConfig
	lang := i18n.Fallback
	if a.botCfg != nil {
		w = a.botCfg.Welcome
		if a.botCfg.Language != "" {
			lang = a.botCfg.Language
		}
	}
	a.mu.Unlock()

	var photo models.InputFile
	switch {
	case w.ImageFileID != "":
		photo = &models.InputFileString{Data: w.ImageFileID}
	case w.ImageURL != "":
		photo = &models.InputFileString{Data: w.ImageURL}
	default:
		photo = &models.InputFileUpload{Filename: "welcome.jpg", Data: bytes.NewReader(defaultBanner)}
	}

	caption := w.Text
	var ents []models.MessageEntity
	if caption == "" {
		caption = i18n.T(lang, "menu.welcome", name)
	} else if len(w.Entities) > 0 {
		_ = json.Unmarshal(w.Entities, &ents)
	}
	return photo, caption, ents
}

func (a *App) showMenu(ctx context.Context, chatID int64, isAdmin bool, name string) {
	lang := a.botLang()
	photo, caption, ents := a.welcomeContent(name)
	var rows [][]models.InlineKeyboardButton
	if isAdmin {
		caption = i18n.T(lang, "menu.admin_title")
		ents = nil
		rows = a.adminMenuRows(lang)
	} else {
		rows = a.userMenuRows(lang)
	}
	if len(ents) == 0 {
		caption = a.applyPremium(caption)
	}
	a.msg.SendBanner(ctx, chatID, photo, caption, ents, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (a *App) showRegister(ctx context.Context, chatID int64, name string) {
	lang := a.botLang()
	a.sendKB(ctx, chatID, i18n.T(lang, "register.prompt", name), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.register"), "menu:register")},
	})
}

func (a *App) registerUser(ctx context.Context, chatID int64, name string) {
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	a.showMenu(ctx, chatID, false, name)
}

func (a *App) onMenu(ctx context.Context, chatID int64, val string, isAdmin bool, name string) {
	switch val {
	case "buy":
		a.showPlans(ctx, chatID)
	case "home":
		a.showMenu(ctx, chatID, isAdmin, name)
	case "register":
		a.registerUser(ctx, chatID, name)
	case "status":
		if isAdmin {
			a.handleStatus(ctx, chatID)
		}
	case "p2p":
		if isAdmin {
			a.showP2PAdmin(ctx, chatID)
		}
	case "emoji":
		if isAdmin {
			a.showEmojiGrid(ctx, chatID)
		}
	case "welcome":
		if isAdmin {
			a.showWelcomeAdmin(ctx, chatID)
		}
	case "update":
		if isAdmin {
			a.handleUpdate(ctx, chatID)
		}
	}
}

// --- админ: баннер ---

func (a *App) showWelcomeAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, i18n.T(lang, "welcome.title"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "welcome.btn_image"), "wel:img"), btn(i18n.T(lang, "welcome.btn_text"), "wel:txt")},
		homeRow(lang),
	})
}

func (a *App) onWelcome(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	cancel := [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.cancel"), "wel:cancel")}}
	switch val {
	case "img":
		ui.welcomeAwait = "img"
		a.sendKB(ctx, chatID, i18n.T(lang, "welcome.ask_image"), cancel)
	case "txt":
		ui.welcomeAwait = "txt"
		a.sendKB(ctx, chatID, i18n.T(lang, "welcome.ask_text"), cancel)
	case "cancel":
		ui.welcomeAwait = ""
		a.showWelcomeAdmin(ctx, chatID)
	}
}

func (a *App) setWelcomeImageURL(ctx context.Context, chatID int64, url string) {
	a.getUI(chatID).welcomeAwait = ""
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.Welcome.ImageURL = strings.TrimSpace(url)
		a.botCfg.Welcome.ImageFileID = ""
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.send(ctx, chatID, i18n.T(a.lang(chatID), "welcome.saved"))
}

func (a *App) setWelcomeImageFile(ctx context.Context, chatID int64, fileID string) {
	a.getUI(chatID).welcomeAwait = ""
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.Welcome.ImageFileID = fileID
		a.botCfg.Welcome.ImageURL = ""
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.send(ctx, chatID, i18n.T(a.lang(chatID), "welcome.saved"))
}

func (a *App) setWelcomeText(ctx context.Context, chatID int64, m *models.Message) {
	a.getUI(chatID).welcomeAwait = ""
	ents, _ := json.Marshal(m.Entities)
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.Welcome.Text = m.Text
		a.botCfg.Welcome.Entities = ents
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.send(ctx, chatID, i18n.T(a.lang(chatID), "welcome.saved"))
}

// --- админ: эмодзи (грид) ---

func (a *App) showEmojiGrid(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	m := a.premiumMap()
	var sb strings.Builder
	sb.WriteString(i18n.T(lang, "emoji.title"))
	for _, e := range botEmojis {
		sb.WriteString("\n" + e.E + " — " + e.Use)
	}
	var rows [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for _, e := range botEmojis {
		label := e.E
		if _, ok := m[e.E]; ok {
			label = e.E + " ✅"
		}
		row = append(row, btn(label, "emo:set:"+e.E))
		if len(row) == 3 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, homeRow(lang))
	a.sendKB(ctx, chatID, sb.String(), rows)
}

func (a *App) onEmoji(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	action, arg, _ := strings.Cut(val, ":")
	switch action {
	case "set":
		a.getUI(chatID).awaitEmojiFor = arg
		a.sendKB(ctx, chatID, i18n.T(lang, "emoji.ask_one", arg),
			[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.cancel"), "emo:done")}})
	case "done":
		a.getUI(chatID).awaitEmojiFor = ""
		a.showEmojiGrid(ctx, chatID)
	}
}

func (a *App) setEmojiFor(ctx context.Context, chatID int64, m *models.Message) {
	ui := a.getUI(chatID)
	target := ui.awaitEmojiFor
	ui.awaitEmojiFor = ""
	var id string
	for _, e := range m.Entities {
		if e.Type == models.MessageEntityTypeCustomEmoji && e.CustomEmojiID != "" {
			id = e.CustomEmojiID
			break
		}
	}
	if id == "" {
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "emoji.none_in_msg"))
		a.showEmojiGrid(ctx, chatID)
		return
	}
	a.mu.Lock()
	if a.botCfg != nil {
		if a.botCfg.PremiumEmoji == nil {
			a.botCfg.PremiumEmoji = map[string]string{}
		}
		a.botCfg.PremiumEmoji[target] = id
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.send(ctx, chatID, i18n.T(a.lang(chatID), "emoji.set_ok", target))
	a.showEmojiGrid(ctx, chatID)
}

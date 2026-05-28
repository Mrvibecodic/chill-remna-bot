package app

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
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

// userLabel — «Ник (id)» для списка/карточки. Ник: @username, иначе имя, иначе только id.
func userLabel(u *model.User) string {
	id := strconv.FormatInt(u.TelegramID, 10)
	nick := ""
	switch {
	case u.Username != "":
		nick = "@" + u.Username
	case u.FirstName != "":
		nick = u.FirstName
	}
	if nick == "" {
		return id
	}
	return nick + " (" + id + ")"
}

// userHasSub — есть ли у пользователя одобренная покупка (для выбора кнопки нав-ряда).
func (a *App) userHasSub(ctx context.Context, chatID int64) bool {
	if a.store == nil {
		return false
	}
	if ok, _ := a.store.HasPaidPayment(ctx, chatID); ok {
		return true
	}
	ok, _ := a.store.HasApprovedPurchase(ctx, chatID)
	return ok
}

// navRow — нижний ряд инлайн-навигации: админу только «Главная»; пользователю
// «Главная» + «Купить», а после покупки — «Главная» + «Мои подписки».
func (a *App) navRow(ctx context.Context, chatID int64, isAdmin bool) []models.InlineKeyboardButton {
	lang := a.lang(chatID)
	row := []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.home"), "menu:home")}
	if isAdmin {
		return row
	}
	if a.userHasSub(ctx, chatID) {
		row = append(row, btn(i18n.T(lang, "btn.mysubs"), "menu:mysubs"))
	} else {
		row = append(row, btn(i18n.T(lang, "btn.buy"), "menu:buy"))
	}
	return row
}

func homeRow(lang string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.home"), "menu:home")}
}

func (a *App) adminMenuRows(lang string) [][]models.InlineKeyboardButton {
	return [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
		{btn(i18n.T(lang, "menu.cat_iface"), "menu:iface"), btn(i18n.T(lang, "menu.cat_pay"), "menu:pay")},
		{btn(i18n.T(lang, "menu.cat_manage"), "menu:manage")},
		homeRow(lang),
	}
}

func (a *App) showIface(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.sendKBSection(ctx, chatID, assets.SectionMainMenu, i18n.T(lang, "menu.iface_title"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.banner"), "menu:welcome"), btn(i18n.T(lang, "btn.emoji"), "menu:emoji")},
		{btn(i18n.T(lang, "btn.section_banners"), "menu:welcome_sections")},
		homeRow(lang),
	})
}

func (a *App) showPay(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.sendKBSection(ctx, chatID, assets.SectionBuySubscription, i18n.T(lang, "menu.pay_title"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.p2p"), "menu:p2p"), btn(i18n.T(lang, "btn.stars"), "menu:stars")},
		{btn(i18n.T(lang, "btn.yookassa"), "menu:yookassa"), btn(i18n.T(lang, "btn.pricing"), "menu:pricing")},
		homeRow(lang),
	})
}

func (a *App) showManage(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.sendKBSection(ctx, chatID, assets.SectionAdminStats, i18n.T(lang, "menu.manage_title"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.users"), "menu:users"), btn(i18n.T(lang, "btn.payments"), "menu:payments")},
		{btn(i18n.T(lang, "btn.status"), "menu:status"), btn(i18n.T(lang, "btn.update"), "menu:update")},
		{btn(i18n.T(lang, "btn.subdomain"), "menu:subdomain"), btn(i18n.T(lang, "btn.apilog"), "menu:apilog")},
		{btn(i18n.T(lang, "btn.reconfig"), "menu:reconf")},
		homeRow(lang),
	})
}

// startReconfigure перезапускает мастер с шага БД, СОХРАНЯЯ текущий конфиг
// (P2P, баннер, эмодзи, язык) — меняются только БД и подключение к панели.
func (a *App) startReconfigure(ctx context.Context, chatID int64) {
	a.mu.Lock()
	var base model.BotConfig
	if a.botCfg != nil {
		base = *a.botCfg
	}
	w := &wizard{step: stepDB, cfg: base}
	a.wiz[chatID] = w
	a.mu.Unlock()
	a.gotoDB(ctx, chatID, w)
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
	a.ensureHomeKey(ctx, chatID)
	lang := a.botLang()
	photo, caption, ents := a.welcomeContent(name)
	var rows [][]models.InlineKeyboardButton
	if isAdmin {
		caption = i18n.T(lang, "menu.admin_title")
		ents = nil
		rows = a.adminMenuRows(lang)
	} else {
		rows = [][]models.InlineKeyboardButton{a.navRow(ctx, chatID, false)}
	}
	if len(ents) == 0 {
		caption = a.applyPremium(caption)
	}
	a.sendBanner(ctx, chatID, photo, caption, ents, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (a *App) showRegister(ctx context.Context, chatID int64, name string) {
	a.ensureHomeKey(ctx, chatID)
	lang := a.botLang()
	a.sendKB(ctx, chatID, i18n.T(lang, "register.prompt", name), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.register"), "menu:register")},
	})
}

func (a *App) registerUser(ctx context.Context, chatID int64, firstName, username string) {
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
		_ = a.store.SetUserInfo(ctx, chatID, username, firstName)
	}
	a.showMenu(ctx, chatID, false, displayName(firstName, username))
}

func (a *App) onMenu(ctx context.Context, chatID int64, val string, isAdmin bool, firstName, username string) {
	name := displayName(firstName, username)
	switch val {
	case "buy":
		a.showPlans(ctx, chatID)
	case "mysubs":
		a.showMySubs(ctx, chatID)
	case "home":
		a.showMenu(ctx, chatID, isAdmin, name)
	case "register":
		a.registerUser(ctx, chatID, firstName, username)
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
	case "welcome_sections":
		if isAdmin {
			a.showSectionBanners(ctx, chatID)
		}
	case "subdomain":
		if isAdmin {
			a.showSubdomain(ctx, chatID)
		}
	case "apilog":
		if isAdmin {
			a.showAPILog(ctx, chatID, 0)
		}
	case "update":
		if isAdmin {
			a.handleUpdate(ctx, chatID)
		}
	case "iface":
		if isAdmin {
			a.showIface(ctx, chatID)
		}
	case "pay":
		if isAdmin {
			a.showPay(ctx, chatID)
		}
	case "manage":
		if isAdmin {
			a.showManage(ctx, chatID)
		}
	case "reconf":
		if isAdmin {
			a.startReconfigure(ctx, chatID)
		}
	case "users":
		if isAdmin {
			a.showUsers(ctx, chatID, 0)
		}
	case "stars":
		if isAdmin {
			a.showStarsAdmin(ctx, chatID)
		}
	case "yookassa":
		if isAdmin {
			a.showYooKassaAdmin(ctx, chatID)
		}
	case "pricing":
		if isAdmin {
			a.showPricing(ctx, chatID)
		}
	case "payments":
		if isAdmin {
			a.showPayments(ctx, chatID, 0)
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
	a.showWelcomeAdmin(ctx, chatID)
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
	a.showWelcomeAdmin(ctx, chatID)
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
	a.showWelcomeAdmin(ctx, chatID)
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
	a.showEmojiGrid(ctx, chatID)
}

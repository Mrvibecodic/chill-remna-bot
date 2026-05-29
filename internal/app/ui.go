package app

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

//go:embed banner_default.jpg
var defaultBanner []byte

var botEmojis = []struct{ E, Use string }{
	{"👋", "приветствие на /start"},
	{"✅", "подтверждение: «Я оплатил», активация подписки, доступ"},
	{"❌", "отказ оплаты, ошибка, кнопка «Закрыть»"},
	{"⏳", "«запускаю обновление…» и другие процессы"},
	{"🕒", "«скриншот получен, ожидайте подтверждения»"},
	{"🔒", "P2P: нужно одобрение администратора"},
	{"📸", "просьба прислать скриншот оплаты"},
	{"💳", "кнопка «Купить», карта в P2P, методы оплаты"},
	{"📦", "выбор тарифа, «моя подписка»"},
	{"📭", "пусто: «нет активных подписок», «тарифы не настроены»"},
	{"🙏", "«способы оплаты пока не настроены»"},
	{"🔥", "подсказка «чаще всего выбирают X мес»"},
	{"⭐", "оплата через Telegram Stars"},
	{"🎁", "триал, кнопка «🎁 Триал», уведомление об активации"},
	{"🏠", "кнопка «На главную»"},
	{"📲", "кнопка «Мои подписки»"},
	{"👥", "кнопка «Группа» на главной у юзера"},
	{"🛟", "кнопка «Поддержка» на главной у юзера"},
	{"📜", "пользовательское соглашение перед первой покупкой"},
	{"🚪", "кнопка «Не сейчас» в соглашении"},
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

func (a *App) userLabelByID(ctx context.Context, id int64) string {
	if a.store != nil {
		if u, _ := a.store.GetUser(ctx, id); u != nil {
			return userLabel(u)
		}
	}
	return strconv.FormatInt(id, 10)
}

const subCacheTTL = 30

func (a *App) userHasSub(ctx context.Context, chatID int64) bool {

	a.subMu.Lock()
	if a.subCache != nil {
		if e, ok := a.subCache[chatID]; ok && time.Now().Before(e.expireAt) {
			a.subMu.Unlock()
			return e.has
		}
	}
	a.subMu.Unlock()

	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if panel == nil {
		return false
	}
	_, _, has := panel.Subscription(ctx, chatID)

	a.subMu.Lock()
	if a.subCache == nil {
		a.subCache = map[int64]subCacheEntry{}
	}
	a.subCache[chatID] = subCacheEntry{has: has, expireAt: time.Now().Add(subCacheTTL * time.Second)}
	a.subMu.Unlock()
	return has
}

func (a *App) invalidateSubCache(chatID int64) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	if a.subCache != nil {
		delete(a.subCache, chatID)
	}
}

func (a *App) navRow(ctx context.Context, chatID int64) []models.InlineKeyboardButton {
	lang := a.lang(chatID)
	var row []models.InlineKeyboardButton
	if a.userHasSub(ctx, chatID) {
		row = append(row, btn(i18n.T(lang, "btn.mysubs"), "menu:mysubs"))
		if a.renewEligible(ctx, chatID) {
			row = append(row, btn(i18n.T(lang, "btn.renew"), "menu:renew"))
		}
	} else {
		if a.trialAvailable(ctx, chatID) {
			row = append(row, btn(i18n.T(lang, "btn.trial_user"), "menu:trial"))
		}
		row = append(row, btn(i18n.T(lang, "btn.buy"), "menu:buy"))
	}
	row = append(row, btn(i18n.T(lang, "btn.balance"), "menu:balance"))
	return row
}

func (a *App) renewEligible(ctx context.Context, chatID int64) bool {
	if a.store == nil {
		return false
	}
	u, _ := a.store.GetUser(ctx, chatID)
	if u == nil {
		return false
	}
	if u.NotifyKind == "trial" {
		return true
	}
	if u.SubExpireAt == "" {
		return false
	}
	exp, err := time.Parse(time.RFC3339, u.SubExpireAt)
	if err != nil {
		return false
	}
	return daysUntil(exp, time.Now().UTC()) <= 7
}

func (a *App) contactRows() [][]models.InlineKeyboardButton {
	a.mu.Lock()
	g, sup := "", ""
	if a.botCfg != nil {
		g, sup = a.botCfg.Contact.GroupURL, a.botCfg.Contact.SupportURL
	}
	lang := i18n.Fallback
	if a.botCfg != nil && a.botCfg.Language != "" {
		lang = a.botCfg.Language
	}
	a.mu.Unlock()
	var row []models.InlineKeyboardButton
	if g != "" {
		row = append(row, models.InlineKeyboardButton{Text: i18n.T(lang, "btn.group"), URL: g})
	}
	if sup != "" {
		row = append(row, models.InlineKeyboardButton{Text: i18n.T(lang, "btn.support"), URL: sup})
	}
	if len(row) == 0 {
		return nil
	}
	return [][]models.InlineKeyboardButton{row}
}

func homeRow(lang string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.home"), "menu:home")}
}

func navBack(lang, backCB string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), backCB),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	}
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
		{btn(i18n.T(lang, "btn.contacts"), "menu:contacts")},
		homeRow(lang),
	})
}

func (a *App) showPay(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	p2pOn, starsOn, ykOn, cbOn := false, false, false, false
	strat := "MONTH"
	if a.botCfg != nil {
		p2pOn = a.botCfg.P2P.Enabled
		starsOn = a.botCfg.Stars.Enabled
		ykOn = a.botCfg.YooKassa.Enabled
		cbOn = a.botCfg.CryptoBot.Enabled
		strat = a.botCfg.Pricing.ResetStrategy()
	}
	a.mu.Unlock()
	mark := func(on bool) string {
		if on {
			return "✅"
		}
		return "❌"
	}
	internalCSV, externalName := a.squadDisplay(ctx)
	title := i18n.T(lang, "subsetup.title",
		mark(p2pOn), mark(starsOn), mark(ykOn), mark(cbOn),
		a.formatTrafficLimits(), a.formatDeviceLimits(lang), strat,
		internalCSV, externalName,
	)
	a.sendKBSection(ctx, chatID, assets.SectionBuySubscription, title, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "subsetup.btn_quick"), "prc:quick")},
		{btn(i18n.T(lang, "subsetup.btn_manual"), "menu:pricing")},
		{btn(i18n.T(lang, "btn.trial_admin"), "menu:trial"), btn(i18n.T(lang, "btn.squads"), "menu:squads")},
		{btn(i18n.T(lang, "btn.p2p"), "menu:p2p"), btn(i18n.T(lang, "btn.stars"), "menu:stars")},
		{btn(i18n.T(lang, "btn.yookassa"), "menu:yookassa"), btn(i18n.T(lang, "btn.cryptobot"), "menu:cryptobot")},
		{btn(i18n.T(lang, "btn.platega"), "menu:platega"), btn(i18n.T(lang, "btn.tribute"), "menu:tribute")},
		{btn(i18n.T(lang, "btn.promo_admin"), "menu:promoadmin"), btn(i18n.T(lang, "btn.moynalog"), "menu:moynalog")},
		homeRow(lang),
	})
}

func (a *App) showManage(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.sendKBSection(ctx, chatID, assets.SectionAdminStats, i18n.T(lang, "menu.manage_title"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.users"), "menu:users"), btn(i18n.T(lang, "btn.payments"), "menu:payments")},
		{btn(i18n.T(lang, "btn.analytics"), "menu:analytics")},
		{btn(i18n.T(lang, "btn.status"), "menu:status"), btn(i18n.T(lang, "btn.update"), "menu:update")},
		{btn(i18n.T(lang, "btn.subdomain"), "menu:subdomain"), btn(i18n.T(lang, "btn.apilog"), "menu:apilog")},
		{btn(i18n.T(lang, "btn.webhooks"), "menu:webhooks"), btn(i18n.T(lang, "btn.notify"), "menu:notify")},
		{btn(i18n.T(lang, "btn.referral_admin"), "menu:refadmin"), btn(i18n.T(lang, "btn.broadcast"), "menu:broadcast")},
		{btn(i18n.T(lang, "btn.reconfig"), "menu:reconf")},
		homeRow(lang),
	})
}

func (a *App) squadDisplay(ctx context.Context) (string, string) {
	a.mu.Lock()
	var activeInt []string
	extUUID := ""
	if a.botCfg != nil {
		activeInt = append([]string(nil), a.botCfg.Plan.ActiveInternalSquads...)
		extUUID = a.botCfg.Plan.ExternalSquadUUID
	}
	a.mu.Unlock()
	return a.squadNames(ctx, activeInt, extUUID)
}

func (a *App) squadNames(ctx context.Context, activeInt []string, extUUID string) (string, string) {
	a.mu.Lock()
	panel := a.panel
	lang := i18n.Fallback
	if a.botCfg != nil && a.botCfg.Language != "" {
		lang = a.botCfg.Language
	}
	a.mu.Unlock()

	names := map[string]string{}
	if panel != nil {
		if ints, err := panel.ListSquads(ctx); err == nil {
			for _, s := range ints {
				names[s.UUID] = s.Name
			}
		}
		if exts, err := panel.ListExternalSquads(ctx); err == nil {
			for _, s := range exts {
				names[s.UUID] = s.Name
			}
		}
	}
	disp := func(uuid string) string {
		if n, ok := names[uuid]; ok && n != "" {
			return n
		}
		return uuid
	}
	var ints []string
	for _, u := range activeInt {
		ints = append(ints, disp(u))
	}
	internalCSV := strings.Join(ints, ", ")
	if internalCSV == "" {
		internalCSV = i18n.T(lang, "admin.none")
	}
	externalName := i18n.T(lang, "admin.none")
	if extUUID != "" {
		externalName = disp(extUUID)
	}
	return internalCSV, externalName
}

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

func bannerInputFor(section string) models.InputFile {
	if b := assets.Bytes(section); len(b) > 0 {
		return &models.InputFileUpload{Filename: section + ".jpg", Data: bytes.NewReader(b)}
	}
	return &models.InputFileUpload{Filename: "welcome.jpg", Data: bytes.NewReader(defaultBanner)}
}

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

		photo = bannerInputFor(assets.SectionMainMenu)
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
		photo = bannerInputFor(assets.SectionAdminStats)
	} else {
		rows = a.contactRows()
		rows = append(rows, a.navRow(ctx, chatID))
		if a.referralCfg().Enabled {
			rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.referral"), "menu:ref")})
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.promo"), "menu:promo")})
		rows = append(rows, homeRow(lang))
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
	case "renew":
		a.showPlans(ctx, chatID)
	case "topup":
		a.showTopUp(ctx, chatID)
	case "balance":
		a.showBalance(ctx, chatID)
	case "ref":
		a.showReferral(ctx, chatID)
	case "refadmin":
		if isAdmin {
			a.showReferralAdmin(ctx, chatID)
		}
	case "broadcast":
		if isAdmin {
			a.showBroadcast(ctx, chatID)
		}
	case "promo":
		a.showPromoUser(ctx, chatID)
	case "promoadmin":
		if isAdmin {
			a.showPromoAdmin(ctx, chatID)
		}
	case "moynalog":
		if isAdmin {
			a.showMoyNalogAdmin(ctx, chatID)
		}
	case "platega":
		if isAdmin {
			a.showPlategaAdmin(ctx, chatID)
		}
	case "tribute":
		if isAdmin {
			a.showTributeAdmin(ctx, chatID)
		}
	case "analytics":
		if isAdmin {
			a.showAnalytics(ctx, chatID)
		}
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
	case "webhooks":
		if isAdmin {
			a.showWebhooksAdmin(ctx, chatID)
		}
	case "notify":
		if isAdmin {
			a.showNotifyAdmin(ctx, chatID)
		}
	case "cryptobot":
		if isAdmin {
			a.showCryptoBotAdmin(ctx, chatID)
		}
	case "squads":
		if isAdmin {
			a.showSquads(ctx, chatID)
		}
	case "trial":
		if isAdmin {
			a.showTrialAdmin(ctx, chatID)
		} else {
			a.activateTrial(ctx, chatID)
		}
	case "contacts":
		if isAdmin {
			a.showContacts(ctx, chatID)
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

func (a *App) showWelcomeAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, i18n.T(lang, "welcome.title"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "welcome.btn_image"), "wel:img"), btn(i18n.T(lang, "welcome.btn_text"), "wel:txt")},
		{btn(i18n.T(lang, "btn.back"), "menu:iface"), btn(i18n.T(lang, "btn.home"), "menu:home")},
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

func (a *App) showEmojiGrid(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	m := a.premiumMap()
	var sb strings.Builder
	sb.WriteString(i18n.T(lang, "emoji.title"))
	sb.WriteString("\n")
	for _, e := range botEmojis {
		mark := ""
		if _, ok := m[e.E]; ok {
			mark = " ✅"
		}
		sb.WriteString("\n" + e.E + mark + " — " + e.Use)
	}

	var rows [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for _, e := range botEmojis {
		label := e.E
		if _, ok := m[e.E]; ok {
			label = e.E + "✅"
		}
		row = append(row, btn(label, "emo:set:"+e.E))
		if len(row) == 4 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:iface"), btn(i18n.T(lang, "btn.home"), "menu:home")})
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

package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// uiState — рантайм-состояние меню/покупки/админки по chatID (вне мастера установки).
type uiState struct {
	buyMonths     int    // выбранный срок плана
	awaitShotReq  int64  // id заявки P2P, по которой ждём скриншот
	rejectReq     int64  // id заявки, для которой админ вводит причину отказа
	adminInput    string // ожидаемый ввод админа: "cards"|"price"|"squad"
	priceMonths   int    // при adminInput=="price" — для какого срока
	welcomeAwait  string // ожидаем для баннера: "img"|"txt"
	awaitEmojiFor string // ожидаем аним-эмодзи для этой стандартной эмодзи
}

func (a *App) getUI(chatID int64) *uiState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ui == nil {
		a.ui = map[int64]*uiState{}
	}
	st := a.ui[chatID]
	if st == nil {
		st = &uiState{}
		a.ui[chatID] = st
	}
	return st
}

func (a *App) saveBotConfig(ctx context.Context) error {
	a.mu.Lock()
	cfg, st := a.botCfg, a.store
	a.mu.Unlock()
	if cfg == nil || st == nil {
		return fmt.Errorf("бот не настроен")
	}
	return st.SaveConfig(ctx, cfg)
}

func (a *App) p2pConfig() model.P2PConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.P2PConfig{}
	}
	return a.botCfg.P2P
}

// --- меню / покупка ---

func (a *App) showPlans(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	p2p := a.p2pConfig()
	var rows [][]models.InlineKeyboardButton
	for _, mo := range model.PlanMonths {
		price, ok := p2p.Prices[mo]
		if !ok || price == "" {
			continue
		}
		label := i18n.T(lang, "buy.plan_btn", mo, price+curSuffix(p2p.Currency))
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "buy:"+strconv.Itoa(mo))})
	}
	if len(rows) == 0 {
		a.sendKB(ctx, chatID, i18n.T(lang, "buy.no_plans"), [][]models.InlineKeyboardButton{homeRow(lang)})
		return
	}
	rows = append(rows, homeRow(lang))
	a.sendKB(ctx, chatID, i18n.T(lang, "buy.choose_plan"), rows)
}

func (a *App) onBuyPlan(ctx context.Context, chatID int64, val string) {
	mo, err := strconv.Atoi(val)
	if err != nil {
		return
	}
	a.getUI(chatID).buyMonths = mo
	a.showMethods(ctx, chatID)
}

func (a *App) showMethods(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if !a.p2pConfig().Enabled {
		a.send(ctx, chatID, i18n.T(lang, "buy.no_methods"))
		return
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "buy.choose_method"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "method.p2p_btn"), "method:p2p")},
	})
}

func (a *App) onMethod(ctx context.Context, chatID int64, val string) {
	if val == "p2p" {
		a.startP2P(ctx, chatID)
	}
}

// --- P2P: гейт доступа -> карта -> скрин ---

func (a *App) startP2P(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	_ = a.store.UpsertUser(ctx, chatID)
	u, err := a.store.GetUser(ctx, chatID)
	if err != nil {
		a.send(ctx, chatID, "❌ "+err.Error())
		return
	}
	if u == nil || !u.P2PApproved {
		a.send(ctx, chatID, i18n.T(lang, "p2p.need_approval"))
		a.notifyAdminUserRequest(ctx, chatID)
		return
	}
	a.issueCard(ctx, chatID)
}

func (a *App) notifyAdminUserRequest(ctx context.Context, userID int64) {
	lang := a.lang(a.cfg.AdminID)
	id := strconv.FormatInt(userID, 10)
	a.notifyKB(ctx, a.cfg.AdminID, i18n.T(lang, "admin.user_request", userID), [][]models.InlineKeyboardButton{{
		btn(i18n.T(lang, "admin.btn_user_ok"), "adm:uok:"+id),
		btn(i18n.T(lang, "admin.btn_user_no"), "adm:uno:"+id),
	}})
}

func (a *App) issueCard(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	months := ui.buyMonths
	if months == 0 {
		months = model.PlanMonths[0]
	}

	a.mu.Lock()
	p2p := a.botCfg.P2P
	if len(p2p.Cards) == 0 {
		a.mu.Unlock()
		a.send(ctx, chatID, i18n.T(lang, "p2p.no_cards"))
		return
	}
	idx := 0
	if p2p.Rotate && len(p2p.Cards) > 1 {
		idx = p2p.RotateIdx % len(p2p.Cards)
		a.botCfg.P2P.RotateIdx = idx + 1
	}
	card := p2p.Cards[idx]
	price := p2p.Prices[months]
	cur := p2p.Currency
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)

	req := &model.P2PRequest{TelegramID: chatID, Months: months, Price: price, Status: model.P2PAwaiting}
	if err := a.store.CreateP2PRequest(ctx, req); err != nil {
		a.send(ctx, chatID, "❌ "+err.Error())
		return
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "p2p.card", months, price+curSuffix(cur), card),
		[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "p2p.paid_btn"), "p2p:paid:"+strconv.FormatInt(req.ID, 10))}})
}

func (a *App) onP2PUser(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	if action == "paid" {
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return
		}
		a.getUI(chatID).awaitShotReq = id
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "p2p.send_screenshot"))
	}
}

func (a *App) handlePhoto(ctx context.Context, m *models.Message) {
	chatID := m.Chat.ID
	a.beginScreen(chatID)
	ui := a.getUI(chatID)
	if ui.welcomeAwait == "img" {
		a.setWelcomeImageFile(ctx, chatID, m.Photo[len(m.Photo)-1].FileID)
		return
	}
	if ui.awaitShotReq == 0 || a.store == nil {
		return
	}
	reqID := ui.awaitShotReq
	fileID := m.Photo[len(m.Photo)-1].FileID
	req, err := a.store.GetP2PRequest(ctx, reqID)
	if err != nil || req == nil {
		return
	}
	req.Screenshot = fileID
	req.Status = model.P2PSubmitted
	if err := a.store.UpdateP2PRequest(ctx, req); err != nil {
		a.send(ctx, chatID, "❌ "+err.Error())
		return
	}
	ui.awaitShotReq = 0
	a.send(ctx, chatID, i18n.T(a.lang(chatID), "p2p.submitted"))
	a.notifyAdminPayment(ctx, req, fileID)
}

func (a *App) notifyAdminPayment(ctx context.Context, req *model.P2PRequest, fileID string) {
	lang := a.lang(a.cfg.AdminID)
	caption := i18n.T(lang, "admin.payment_caption", req.TelegramID, req.Months, req.Price+curSuffix(a.p2pConfig().Currency), req.ID)
	id := strconv.FormatInt(req.ID, 10)
	a.notifyPhoto(ctx, a.cfg.AdminID, fileID, caption, [][]models.InlineKeyboardButton{{
		btn(i18n.T(lang, "admin.btn_pay_ok"), "adm:pok:"+id),
		btn(i18n.T(lang, "admin.btn_pay_no"), "adm:pno:"+id),
	}})
}

// --- админ: настройки P2P + модерация ---

func (a *App) showP2PAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	p2p := a.p2pConfig()
	status := i18n.T(lang, "admin.off")
	if p2p.Enabled {
		status = i18n.T(lang, "admin.on")
	}
	rot := i18n.T(lang, "admin.no")
	if p2p.Rotate {
		rot = i18n.T(lang, "admin.yes")
	}
	squad := p2p.SquadUUID
	if squad == "" {
		squad = i18n.T(lang, "admin.none")
	}
	text := i18n.T(lang, "admin.p2p_title", status, len(p2p.Cards), rot, formatPrices(p2p), squad)
	a.sendKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "admin.btn_toggle"), "adm:toggle"), btn(i18n.T(lang, "admin.btn_rotate"), "adm:rotate")},
		{btn(i18n.T(lang, "admin.btn_cards"), "adm:cards"), btn(i18n.T(lang, "admin.btn_prices"), "adm:prices")},
		{btn(i18n.T(lang, "admin.btn_squad"), "sq:pick")},
		homeRow(lang),
	})
}

func (a *App) onAdmin(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	switch action {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.P2P.Enabled = !a.botCfg.P2P.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showP2PAdmin(ctx, chatID)
	case "rotate":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.P2P.Rotate = !a.botCfg.P2P.Rotate
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showP2PAdmin(ctx, chatID)
	case "cards":
		a.getUI(chatID).adminInput = "cards"
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_cards"))
	case "squad":
		a.getUI(chatID).adminInput = "squad"
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_squad"))
	case "prices":
		a.adminAskPriceMonth(ctx, chatID)
	case "price":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "price"
		ui.priceMonths = mo
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_price", mo))
	case "uok":
		a.adminApproveUser(ctx, chatID, arg, true)
	case "uno":
		a.adminApproveUser(ctx, chatID, arg, false)
	case "pok":
		a.adminApprovePayment(ctx, chatID, arg)
	case "pno":
		id, _ := strconv.ParseInt(arg, 10, 64)
		a.getUI(chatID).rejectReq = id
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_reason"))
	}
}

func (a *App) adminAskPriceMonth(ctx context.Context, chatID int64) {
	var row []models.InlineKeyboardButton
	for _, mo := range model.PlanMonths {
		row = append(row, btn(strconv.Itoa(mo)+"м", "adm:price:"+strconv.Itoa(mo)))
	}
	a.sendKB(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_price_month"), [][]models.InlineKeyboardButton{row})
}

func (a *App) adminApproveUser(ctx context.Context, adminChat int64, arg string, ok bool) {
	uid, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return
	}
	alang := a.lang(adminChat)
	if !ok {
		a.send(ctx, adminChat, i18n.T(alang, "admin.user_denied"))
		return
	}
	if err := a.store.SetP2PApproved(ctx, uid, true); err != nil {
		a.send(ctx, adminChat, "❌ "+err.Error())
		return
	}
	a.notify(ctx, uid, i18n.T(a.lang(uid), "p2p.user_approved"))
	a.send(ctx, adminChat, i18n.T(alang, "admin.user_ok_done"))
}

func (a *App) adminApprovePayment(ctx context.Context, adminChat int64, arg string) {
	alang := a.lang(adminChat)
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return
	}
	req, err := a.store.GetP2PRequest(ctx, id)
	if err != nil || req == nil || req.Status != model.P2PSubmitted {
		a.send(ctx, adminChat, i18n.T(alang, "admin.not_found"))
		return
	}
	a.mu.Lock()
	panel := a.panel
	squad := ""
	if a.botCfg != nil {
		squad = a.botCfg.P2P.SquadUUID
	}
	a.mu.Unlock()
	if panel == nil {
		a.send(ctx, adminChat, i18n.T(alang, "admin.provision_fail", "панель не подключена"))
		return
	}
	link, err := panel.CreateOrUpdateUser(ctx, req.TelegramID, req.Months, squad)
	if err != nil {
		a.send(ctx, adminChat, i18n.T(alang, "admin.provision_fail", err.Error()))
		return
	}
	req.Status = model.P2PApproved
	req.DecidedAt = time.Now().UTC().Format(time.RFC3339)
	_ = a.store.UpdateP2PRequest(ctx, req)
	a.notify(ctx, req.TelegramID, i18n.T(a.lang(req.TelegramID), "p2p.user_paid_ok", link))
	a.send(ctx, adminChat, i18n.T(alang, "admin.done"))
}

// handleAdminText обрабатывает текстовый ввод админа вне мастера установки
// (причина отказа, реквизиты карт, цена, сквад).
func (a *App) handleAdminText(ctx context.Context, chatID int64, text string) {
	ui := a.getUI(chatID)
	lang := a.lang(chatID)

	if ui.rejectReq != 0 {
		id := ui.rejectReq
		ui.rejectReq = 0
		req, err := a.store.GetP2PRequest(ctx, id)
		if err != nil || req == nil {
			a.send(ctx, chatID, i18n.T(lang, "admin.not_found"))
			return
		}
		req.Status = model.P2PRejected
		req.Comment = text
		req.DecidedAt = time.Now().UTC().Format(time.RFC3339)
		_ = a.store.UpdateP2PRequest(ctx, req)
		a.notify(ctx, req.TelegramID, i18n.T(a.lang(req.TelegramID), "p2p.user_paid_rejected", text))
		a.send(ctx, chatID, i18n.T(lang, "admin.done"))
		return
	}

	switch ui.adminInput {
	case "cards":
		ui.adminInput = ""
		cards := splitTrim(text, ";")
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.P2P.Cards = cards
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.send(ctx, chatID, i18n.T(lang, "admin.saved"))
	case "squad":
		ui.adminInput = ""
		v := strings.TrimSpace(text)
		if v == "-" {
			v = ""
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.P2P.SquadUUID = v
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.send(ctx, chatID, i18n.T(lang, "admin.saved"))
	case "price":
		mo := ui.priceMonths
		ui.adminInput = ""
		ui.priceMonths = 0
		a.mu.Lock()
		if a.botCfg != nil {
			if a.botCfg.P2P.Prices == nil {
				a.botCfg.P2P.Prices = map[int]string{}
			}
			a.botCfg.P2P.Prices[mo] = strings.TrimSpace(text)
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.send(ctx, chatID, i18n.T(lang, "admin.saved"))
	}
}

// --- helpers ---

func curSuffix(cur string) string {
	if cur == "" {
		return ""
	}
	return " " + cur
}

func splitTrim(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func formatPrices(p model.P2PConfig) string {
	var parts []string
	for _, mo := range model.PlanMonths {
		if v, ok := p.Prices[mo]; ok && v != "" {
			parts = append(parts, strconv.Itoa(mo)+"м="+v)
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

// collectEmoji строит карту "эмодзи -> custom_emoji_id" из присланного админом
// сообщения с анимированными (premium) эмодзи. Работает, если у владельца бота
// есть Telegram Premium. Эмодзи затем подставляются во все сообщения бота.
func (a *App) collectEmoji(ctx context.Context, chatID int64, m *models.Message) {
	a.getUI(chatID).adminInput = ""
	u16 := utf16.Encode([]rune(m.Text))
	added := 0
	a.mu.Lock()
	if a.botCfg != nil {
		if a.botCfg.PremiumEmoji == nil {
			a.botCfg.PremiumEmoji = map[string]string{}
		}
		for _, e := range m.Entities {
			if e.Type != models.MessageEntityTypeCustomEmoji || e.CustomEmojiID == "" {
				continue
			}
			if e.Offset < 0 || e.Offset+e.Length > len(u16) {
				continue
			}
			emoji := string(utf16.Decode(u16[e.Offset : e.Offset+e.Length]))
			a.botCfg.PremiumEmoji[emoji] = e.CustomEmojiID
			added++
		}
	}
	a.mu.Unlock()
	if added > 0 {
		_ = a.saveBotConfig(ctx)
	}
	a.send(ctx, chatID, i18n.T(a.lang(chatID), "admin.emoji_saved", added))
}

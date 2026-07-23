package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
)

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

func (a *App) showPlans(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)

	if a.store != nil {
		if u, _ := a.store.GetUser(ctx, chatID); u != nil && u.NotifyKind == "trial" && u.SubExpireAt != "" {
			if exp, err := time.Parse(time.RFC3339, u.SubExpireAt); err == nil && daysUntil(exp, time.Now().UTC()) > 1 {
				a.sendKB(ctx, chatID, i18n.T(lang, "buy.trial_locked", formatExpire(u.SubExpireAt, lang)),
					[][]models.InlineKeyboardButton{homeRow(lang)})
				return
			}
		}
	}

	if text, need := a.termsRequired(ctx, chatID); need {
		a.askTerms(ctx, chatID, text)
		return
	}
	pr := a.pricing()
	var rows [][]models.InlineKeyboardButton
	for _, mo := range model.PlanMonths {
		price := pr.Base[mo]
		if price == "" {
			continue
		}
		label := i18n.T(lang, "buy.plan_btn", mo, price+curSuffix(curRUB))
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "buy:"+strconv.Itoa(mo))})
	}
	if len(rows) == 0 {
		a.sendKB(ctx, chatID, i18n.T(lang, "buy.no_plans"), [][]models.InlineKeyboardButton{homeRow(lang)})
		return
	}
	rows = append(rows, homeRow(lang))

	caption := i18n.T(lang, "buy.choose_plan")
	if a.store != nil {
		if months, total, err := a.store.MostPopularPlan(ctx); err == nil && months > 0 && total >= popularThreshold {
			caption = i18n.T(lang, "buy.popular", months) + "\n\n" + caption
		}
	}
	a.sendKBSection(ctx, chatID, assets.SectionBuySubscription, caption, rows)
}

const popularThreshold = 10

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
	months := a.getUI(chatID).buyMonths
	a.mu.Lock()
	var p2p model.P2PConfig
	var stars model.StarsConfig
	var yk model.YooKassaConfig
	var pr model.Pricing
	var cb model.CryptoBotConfig
	if a.botCfg != nil {
		p2p = a.botCfg.P2P
		stars = a.botCfg.Stars
		yk = a.botCfg.YooKassa
		cb = a.botCfg.CryptoBot
		pr = a.botCfg.Pricing
	}
	a.mu.Unlock()

	var rows [][]models.InlineKeyboardButton
	if p2p.Enabled {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "method.p2p_btn"), "method:p2p")})
	}
	if yk.Enabled && pr.Fiat(model.PayMethodYooKassa, months) != "" {
		label := i18n.T(lang, "method.yk_btn", pr.Fiat(model.PayMethodYooKassa, months)+curSuffix(a.curFor(model.PayMethodYooKassa)))
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "method:yk")})
	}
	if stars.Enabled && pr.StarPrice(months) > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "method.stars_btn", pr.StarPrice(months)), "method:stars")})
	}
	if cb.Enabled && pr.Base[months] != "" {
		label := i18n.T(lang, "method.cb_btn", pr.Base[months]+curSuffix(curRUB))
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "method:cb")})
	}
	if a.plConfig().Enabled && pr.Fiat(model.PayMethodPlatega, months) != "" {
		label := i18n.T(lang, "method.pl_btn", pr.Fiat(model.PayMethodPlatega, months)+curSuffix(curRUB))
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "method:pl")})
	}
	if a.tributeCfg().Enabled && a.tributeCfg().PayURL != "" {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "method.trb_btn"), "method:trb")})
	}

	bal := a.userBalance(ctx, chatID)
	if k, ok := rubToKopecks(pr.Base[months]); ok && k > 0 && bal >= k {
		payBtn := []models.InlineKeyboardButton{btn(i18n.T(lang, "balance.btn_pay", kopecksToRub(k)), "method:bal")}
		rows = append([][]models.InlineKeyboardButton{payBtn}, rows...)
	}
	if len(rows) == 0 {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "buy.no_methods"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "balance.btn_topup"), "menu:topup")}, homeRow(lang),
		})
		return
	}

	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "balance.btn_topup"), "menu:topup")})
	rows = append(rows, homeRow(lang))
	caption := i18n.T(lang, "buy.choose_method", kopecksToRub(bal))
	if line := a.countriesLine(ctx, lang, months); line != "" {
		caption = line + "\n\n" + caption
	}
	a.sendPayKB(ctx, chatID, caption, rows)
}

func (a *App) onMethod(ctx context.Context, chatID int64, val string) {
	switch val {
	case "bal":
		a.payFromBalance(ctx, chatID)
	case "p2p":
		a.startP2P(ctx, chatID)
	case "stars":
		a.startStars(ctx, chatID)
	case "yk":
		a.startYooKassa(ctx, chatID)
	case "cb":
		a.startCryptoBot(ctx, chatID)
	case "pl":
		a.startPlatega(ctx, chatID)
	case "trb":
		a.startTribute(ctx, chatID)
	}
}

func (a *App) startP2P(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	_ = a.store.UpsertUser(ctx, chatID)
	u, err := a.store.GetUser(ctx, chatID)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	if u == nil || !u.P2PApproved {
		a.sendHome(ctx, chatID, i18n.T(lang, "p2p.need_approval"))
		a.notifyAdminUserRequest(ctx, chatID)
		return
	}
	a.issueCard(ctx, chatID)
}

func (a *App) notifyAdminUserRequest(ctx context.Context, userID int64) {
	lang := a.lang(a.cfg.AdminID)
	id := strconv.FormatInt(userID, 10)
	a.notifyKB(ctx, a.cfg.AdminID, i18n.T(lang, "admin.user_request", a.userLabelByID(ctx, userID)), [][]models.InlineKeyboardButton{{
		btn(i18n.T(lang, "admin.btn_user_ok"), "adm:uok:"+id),
		btn(i18n.T(lang, "admin.btn_user_no"), "adm:uno:"+id),
	}})
}

func (a *App) issueCard(ctx context.Context, chatID int64) {
	ui := a.getUI(chatID)
	months := ui.buyMonths
	if months == 0 {
		months = model.PlanMonths[0]
	}
	a.issueCardMonths(ctx, chatID, months)
}

// issueCardMonths is issueCard for an explicit period (used by the Mini App,
// which has no chat-side buyMonths state).
func (a *App) issueCardMonths(ctx context.Context, chatID int64, months int) {
	lang := a.lang(chatID)
	card, price, reqID, err := a.prepareP2PCard(ctx, chatID, months)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	idStr := strconv.FormatInt(reqID, 10)
	a.sendKB(ctx, chatID, i18n.T(lang, "p2p.card", months, price+curSuffix(curRUB), card),
		[][]models.InlineKeyboardButton{{
			btn(i18n.T(lang, "p2p.paid_btn"), "p2p:paid:"+idStr),
			btn(i18n.T(lang, "btn.cancel"), "p2p:cancel:"+idStr),
		}})
}

// prepareP2PCard picks the next card, creates an awaiting P2P request and
// returns the card + price + request id, without messaging the user (shared by
// the chat flow and the web cabinet).
func (a *App) prepareP2PCard(ctx context.Context, chatID int64, months int) (card, price string, reqID int64, err error) {
	a.mu.Lock()
	a.botCfg.NormalizePricing()
	p2p := a.botCfg.P2P
	pr := a.botCfg.Pricing
	if len(p2p.Cards) == 0 {
		a.mu.Unlock()
		return "", "", 0, errors.New(i18n.T(a.lang(chatID), "p2p.no_cards"))
	}
	idx := 0
	if p2p.Rotate && len(p2p.Cards) > 1 {
		idx = p2p.RotateIdx % len(p2p.Cards)
		a.botCfg.P2P.RotateIdx = idx + 1
	}
	card = p2p.Cards[idx]
	price = pr.Fiat(model.PayMethodP2P, months)
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)

	if a.store == nil {
		return "", "", 0, errors.New("storage unavailable")
	}
	req := &model.P2PRequest{TelegramID: chatID, Months: months, Price: price, Status: model.P2PAwaiting}
	if err = a.store.CreateP2PRequest(ctx, req); err != nil {
		return "", "", 0, err
	}
	a.payLog(ctx, model.PayMethodP2P, p2pExt(req.ID), chatID, "request_created", "months=%d price=%s", months, price)
	return card, price, req.ID, nil
}

// sendAdminPhotoUpload forwards an uploaded image (bytes) to the admin chat.
func (a *App) sendAdminPhotoUpload(ctx context.Context, filename string, data []byte, caption string, rows [][]models.InlineKeyboardButton) {
	photo := &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(data)}
	a.msg.SendBanner(ctx, a.cfg.AdminID, photo, caption, nil, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (a *App) onP2PUser(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	id, _ := strconv.ParseInt(arg, 10, 64)
	switch action {
	case "paid":
		if id == 0 {
			return
		}
		a.getUI(chatID).awaitShotReq = id
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "p2p.send_screenshot"))
	case "cancel":
		if id != 0 && a.store != nil {
			if r, e := a.store.GetP2PRequest(ctx, id); e == nil && r != nil && r.TelegramID == chatID &&
				(r.Status == model.P2PAwaiting || r.Status == model.P2PSubmitted) {
				r.Status = model.P2PRejected
				_ = a.store.UpdateP2PRequest(ctx, r)
				a.payLog(ctx, model.PayMethodP2P, p2pExt(id), chatID, "cancelled", "отменено пользователем")
			}
		}
		a.getUI(chatID).awaitShotReq = 0
		a.showMenu(ctx, chatID, chatID == a.cfg.AdminID, a.displayNameByID(ctx, chatID))
	}
}

func (a *App) handlePhoto(ctx context.Context, m *models.Message) {
	chatID := m.Chat.ID
	ui := a.getUI(chatID)
	if ui.awaitSectionBanner != "" {
		section := ui.awaitSectionBanner
		ui.awaitSectionBanner = ""
		a.setSectionBannerFile(ctx, chatID, section, m.Photo[len(m.Photo)-1].FileID)
		return
	}
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
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	a.payLog(ctx, model.PayMethodP2P, p2pExt(req.ID), chatID, "screenshot_submitted", "ожидает проверки админом")
	ui.awaitShotReq = 0

	ui.p2pShotMsgID = m.ID

	lang := a.lang(chatID)
	ui.p2pSubmitMsgID = a.msg.SendKB(ctx, chatID,
		a.applyPremium(i18n.T(lang, "p2p.submitted")),
		[][]models.InlineKeyboardButton{backHomeRow(lang)})
	a.notifyAdminPayment(ctx, req, fileID)
}

func (a *App) notifyAdminPayment(ctx context.Context, req *model.P2PRequest, fileID string) {
	lang := a.lang(a.cfg.AdminID)
	caption := i18n.T(lang, "admin.payment_caption", a.userLabelByID(ctx, req.TelegramID), req.Months, req.Price+curSuffix(a.curFor(model.PayMethodP2P)), req.ID)
	id := strconv.FormatInt(req.ID, 10)
	a.notifyPhoto(ctx, a.cfg.AdminID, fileID, caption, [][]models.InlineKeyboardButton{{
		btn(i18n.T(lang, "admin.btn_pay_ok"), "adm:pok:"+id),
		btn(i18n.T(lang, "admin.btn_pay_no"), "adm:pno:"+id),
	}})
}

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
	text := i18n.T(lang, "admin.p2p_title", status, len(p2p.Cards), rot, curRUB, a.formatFiatPrices(model.PayMethodP2P), squad)
	a.sendPayKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{toggleBtn(lang, p2p.Enabled, "adm:toggle"), btn(i18n.T(lang, "admin.btn_rotate"), "adm:rotate")},
		{btn(i18n.T(lang, "admin.btn_cards"), "adm:cards"), btn(i18n.T(lang, "admin.btn_prices"), "adm:prices")},
		{btn(i18n.T(lang, "admin.btn_squad"), "sq:pick")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onAdmin(ctx context.Context, chatID int64, val string, srcMsgID int) {
	action, arg, _ := strings.Cut(val, ":")

	switch action {
	case "uok", "uno", "pok", "pno", "wok", "wno":
		if srcMsgID != 0 {
			a.msg.Delete(ctx, chatID, srcMsgID)
		}
	}
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
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_cards"), "menu:p2p")
	case "squad":
		a.getUI(chatID).adminInput = "squad"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_squad"), "menu:p2p")
	case "cur":
		a.getUI(chatID).adminInput = "p2p_cur"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_currency"), "menu:p2p")
	case "prices":
		a.adminAskPriceMonth(ctx, chatID)
	case "price":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "price"
		ui.priceMonths = mo
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_price", mo), "menu:p2p")
	case "uok":
		a.adminApproveUser(ctx, chatID, arg, true)
	case "uno":
		a.adminApproveUser(ctx, chatID, arg, false)
	case "wok":
		a.adminApproveWebUser(ctx, chatID, arg, true)
	case "wno":
		a.adminApproveWebUser(ctx, chatID, arg, false)
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
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, i18n.T(lang, "admin.ask_price_month"), [][]models.InlineKeyboardButton{row, navBack(lang, "menu:p2p")})
}

func (a *App) adminApproveUser(ctx context.Context, adminChat int64, arg string, ok bool) {
	uid, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return
	}
	alang := a.lang(adminChat)
	if !ok {
		a.sendHome(ctx, adminChat, i18n.T(alang, "admin.user_denied"))
		return
	}
	if err := a.store.SetP2PApproved(ctx, uid, true); err != nil {
		a.sendHome(ctx, adminChat, "❌ "+err.Error())
		return
	}
	a.notify(ctx, uid, i18n.T(a.lang(uid), "p2p.user_approved"))
	a.sendHome(ctx, adminChat, i18n.T(alang, "admin.user_ok_done"))
}

func (a *App) adminApprovePayment(ctx context.Context, adminChat int64, arg string) {
	alang := a.lang(adminChat)
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return
	}
	req, err := a.store.GetP2PRequest(ctx, id)
	if err != nil || req == nil || req.Status != model.P2PSubmitted {
		a.sendHome(ctx, adminChat, i18n.T(alang, "admin.not_found"))
		return
	}
	amount := req.Price + curSuffix(a.curFor(model.PayMethodP2P))
	req.Status = model.P2PApproved
	req.DecidedAt = time.Now().UTC().Format(time.RFC3339)
	if err := a.store.UpdateP2PRequest(ctx, req); err != nil {
		a.sendHome(ctx, adminChat, "❌ "+err.Error())
		return
	}
	a.payLog(ctx, model.PayMethodP2P, p2pExt(req.ID), req.TelegramID, "approved", "подтверждено администратором")
	link, expireAt, err := a.finalizePurchase(ctx, req.TelegramID, req.Months, model.PayMethodP2P, amount, p2pExt(req.ID))
	if err != nil {
		if errors.Is(err, storage.ErrDuplicateExtID) {
			a.sendHome(ctx, adminChat, i18n.T(alang, "admin.done"))
			return
		}
		req.Status = model.P2PSubmitted
		req.DecidedAt = ""
		_ = a.store.UpdateP2PRequest(ctx, req)
		a.sendHome(ctx, adminChat, i18n.T(alang, "admin.provision_fail", err.Error()))
		return
	}
	a.cleanupP2PUser(ctx, req.TelegramID)
	a.sendSubActive(ctx, req.TelegramID, link, expireAt)
	a.sendHome(ctx, adminChat, i18n.T(alang, "admin.done"))
}

const finalizeLockShards = 64

func extLockIndex(s string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32() % finalizeLockShards)
}

func (a *App) finalizePurchase(ctx context.Context, telegramID int64, months int, method, amount, extID string) (string, string, error) {
	// Serialize duplicate deliveries of the same payment and bail before we touch
	// the panel if it's already been finalized (the panel extend happens below,
	// before the AddPayment idempotency barrier, so without this two concurrent
	// deliveries would each extend the subscription).
	if extID != "" {
		lk := &a.finalizeLk[extLockIndex(extID)]
		lk.Lock()
		defer lk.Unlock()
		if a.store != nil {
			if done, _ := a.store.PaymentByExtID(ctx, extID); done {
				a.payLog(ctx, method, extID, telegramID, "duplicate", "платёж уже финализирован — пропуск")
				return "", "", storage.ErrDuplicateExtID
			}
		}
	}
	a.mu.Lock()
	panel := a.panel
	limits := remnawave.UserLimits{}
	if a.botCfg != nil {

		limits.InternalSquads = a.botCfg.Plan.ActiveInternalSquads
		limits.ExternalSquad = a.botCfg.Plan.ExternalSquadUUID

		if len(limits.InternalSquads) == 0 && a.botCfg.P2P.SquadUUID != "" {
			limits.InternalSquads = []string{a.botCfg.P2P.SquadUUID}
		}
		if sq := a.botCfg.Pricing.SquadsInt[months]; len(sq) > 0 {
			limits.InternalSquads = append([]string(nil), sq...)
		}
		if e := a.botCfg.Pricing.SquadsExt[months]; e != "" {
			limits.ExternalSquad = e
		}
		limits.TrafficBytes = a.botCfg.Pricing.TrafficBytes(months)
		limits.DeviceLimit = a.botCfg.Pricing.DeviceLimitFor(months)
		limits.Strategy = a.botCfg.Pricing.ResetStrategy()
	}
	a.mu.Unlock()
	a.payLog(ctx, method, extID, telegramID, "finalize", "months=%d amount=%s", months, amount)
	if panel == nil {
		a.payLog(ctx, method, extID, telegramID, "error", "панель не подключена")
		return "", "", fmt.Errorf("панель не подключена")
	}
	link, expireAt, err := panel.CreateOrUpdateUser(ctx, telegramID, months, limits)
	if err != nil {
		a.payLog(ctx, method, extID, telegramID, "panel_error", "%v", err)
		return "", "", err
	}
	a.payLog(ctx, method, extID, telegramID, "panel_ok", "expire=%s", expireAt)
	link = a.rewriteSub(link)
	a.invalidateSubCache(telegramID)
	a.syncAddSub(ctx, telegramID)
	if a.store != nil {
		if err := a.store.AddPayment(ctx, &model.Payment{
			TelegramID: telegramID, Method: method, Months: months, Amount: amount, Status: model.PaymentPaid, ExtID: extID,
		}); err != nil {
			if errors.Is(err, storage.ErrDuplicateExtID) && extID != "" {
				a.payLog(ctx, method, extID, telegramID, "duplicate", "платёж с этим ext_id уже записан")
				return "", "", err
			}
			a.log.Warn("add payment", "err", err)
		}
		_ = a.store.SetSubExpiry(ctx, telegramID, expireAt, "paid")
	}
	a.payLog(ctx, method, extID, telegramID, "done", "подписка выдана, ссылка отправляется")
	a.grantReferralBonus(ctx, telegramID)
	a.creditReferralPercent(ctx, telegramID, amount)
	if method != "balance" && method != model.PayMethodStars && method != model.PayMethodTribute {
		a.fiscalize(parseAmountRub(amount), fmt.Sprintf("Подписка %d мес.", months))
	}
	return link, expireAt, nil
}

func (a *App) handleAdminText(ctx context.Context, chatID int64, text string) {
	ui := a.getUI(chatID)
	lang := a.lang(chatID)

	if ui.rejectReq != 0 {
		id := ui.rejectReq
		ui.rejectReq = 0
		req, err := a.store.GetP2PRequest(ctx, id)
		if err != nil || req == nil {
			a.sendHome(ctx, chatID, i18n.T(lang, "admin.not_found"))
			return
		}
		req.Status = model.P2PRejected
		req.Comment = text
		req.DecidedAt = time.Now().UTC().Format(time.RFC3339)
		_ = a.store.UpdateP2PRequest(ctx, req)
		a.payLog(ctx, model.PayMethodP2P, p2pExt(req.ID), req.TelegramID, "rejected", "%s", text)
		_ = a.store.AddPayment(ctx, &model.Payment{
			TelegramID: req.TelegramID, Method: model.PayMethodP2P, Months: req.Months,
			Amount: req.Price + curSuffix(a.curFor(model.PayMethodP2P)), Status: model.PaymentRejected, Comment: text,
		})
		a.cleanupP2PUser(ctx, req.TelegramID)
		a.notify(ctx, req.TelegramID, i18n.T(a.lang(req.TelegramID), "p2p.user_paid_rejected", text))
		a.sendHome(ctx, chatID, i18n.T(lang, "admin.done"))
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
		a.showP2PAdmin(ctx, chatID)
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
		a.showP2PAdmin(ctx, chatID)
	case "price":
		mo := ui.priceMonths
		ui.adminInput = ""
		ui.priceMonths = 0
		a.setFiatPrice(model.PayMethodP2P, mo, strings.TrimSpace(text))
		_ = a.saveBotConfig(ctx)
		a.showP2PAdmin(ctx, chatID)
	case "starprice":
		mo := ui.priceMonths
		ui.adminInput = ""
		ui.priceMonths = 0
		v, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setStarPrice(mo, v)
		_ = a.saveBotConfig(ctx)
		a.showStarsAdmin(ctx, chatID)
	case "baseprice":
		mo := ui.priceMonths
		ui.adminInput = ""
		ui.priceMonths = 0
		a.setBasePrice(mo, strings.TrimSpace(text))
		_ = a.saveBotConfig(ctx)
		a.showPricing(ctx, chatID)
	case "currency":
		ui.adminInput = ""
		a.setCurrency(strings.TrimSpace(text))
		_ = a.saveBotConfig(ctx)
		a.showPricing(ctx, chatID)
	case "p2p_cur":
		ui.adminInput = ""
		v := strings.TrimSpace(text)
		if v == "-" {
			v = ""
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.P2P.Currency = v
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showP2PAdmin(ctx, chatID)
	case "yk_cur":
		ui.adminInput = ""
		v := strings.TrimSpace(text)
		if v == "-" {
			v = ""
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.YooKassa.Currency = v
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooKassaAdmin(ctx, chatID)
	case "ykprice":
		mo := ui.priceMonths
		ui.adminInput = ""
		ui.priceMonths = 0
		a.setFiatPrice(model.PayMethodYooKassa, mo, strings.TrimSpace(text))
		_ = a.saveBotConfig(ctx)
		a.showYooKassaAdmin(ctx, chatID)
	case "yk_shop":
		ui.adminInput = ""
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.YooKassa.ShopID = strings.TrimSpace(text)
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooKassaAdmin(ctx, chatID)
	case "yk_secret":
		ui.adminInput = ""
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.YooKassa.SecretKey = strings.TrimSpace(text)
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooKassaAdmin(ctx, chatID)
	case "yk_return":
		ui.adminInput = ""
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.YooKassa.ReturnURL = strings.TrimSpace(text)
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooKassaAdmin(ctx, chatID)
	case "subdomain":
		a.setSubdomain(ctx, chatID, text)
	case "wh_addr":
		text = strings.TrimSpace(text)
		// Accept a bare port ("18080") or a full bind addr (":18080",
		// "0.0.0.0:18080"); normalize a bare number to ":port".
		if text != "" && !strings.Contains(text, ":") {
			text = ":" + text
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Webhook.ListenAddr = text
		}
		a.mu.Unlock()
		ui.adminInput = ""
		_ = a.saveBotConfig(ctx)
		// Apply the new port itself: rewrite compose (127.0.0.1:port:port) and
		// recreate the container, so the admin doesn't touch compose by hand.
		a.applyBotPort(ctx, chatID)
	case "wh_base":
		text = normalizeBaseURL(text)
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Webhook.PublicBaseURL = text
		}
		a.mu.Unlock()
		ui.adminInput = ""
		_ = a.saveBotConfig(ctx)
		a.showWebhooksAdmin(ctx, chatID)
	case "wh_secret":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Webhook.RemnawaveSecret = strings.TrimSpace(text)
		}
		a.mu.Unlock()
		ui.adminInput = ""
		_ = a.saveBotConfig(ctx)
		a.showWebhooksAdmin(ctx, chatID)
	case "cb_token":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.CryptoBot.Token = strings.TrimSpace(text)
		}
		a.mu.Unlock()
		ui.adminInput = ""
		_ = a.saveBotConfig(ctx)
		a.showCryptoBotAdmin(ctx, chatID)
	case "cb_asset":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.CryptoBot.Asset = strings.ToUpper(strings.TrimSpace(text))
		}
		a.mu.Unlock()
		ui.adminInput = ""
		_ = a.saveBotConfig(ctx)
		a.showCryptoBotAdmin(ctx, chatID)
	case "ctc_group":
		a.setContact(ctx, chatID, "group", text)
	case "ctc_support":
		a.setContact(ctx, chatID, "support", text)
	case "ctc_terms":
		a.setContact(ctx, chatID, "terms", text)
	case "traffic_gb":
		mo := ui.priceMonths
		ui.adminInput = ""
		ui.priceMonths = 0
		gb, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrafficGB(mo, gb)
		_ = a.saveBotConfig(ctx)
		a.showPricing(ctx, chatID)
	case "device_limit":
		ui.adminInput = ""
		ui.priceMonths = 0
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setDeviceLimitGlobal(n)
		_ = a.saveBotConfig(ctx)
		a.showPricing(ctx, chatID)
	case "bcast":
		ui.adminInput = ""
		a.previewBroadcast(ctx, chatID, text)
	case "promo_create":
		ui.adminInput = ""
		a.createPromoFromText(ctx, chatID, text)
	case "mn_login", "mn_pass", "mn_name":
		field := ui.adminInput
		ui.adminInput = ""
		a.setMoyNalogField(ctx, chatID, field, text)
	case "pl_merchant", "pl_secret", "pl_return":
		field := ui.adminInput
		ui.adminInput = ""
		a.setPlategaField(ctx, chatID, field, text)
	case "trb_key", "trb_url":
		field := ui.adminInput
		ui.adminInput = ""
		a.setTributeField(ctx, chatID, field, text)
	case "paylog":
		ui.adminInput = ""
		a.adminSendPayLog(ctx, chatID, text)
	case "link_panel":
		uid := ui.linkUID
		ui.adminInput = ""
		ui.linkUID = 0
		a.adminLinkPanel(ctx, chatID, uid, text)
	case "wl_add":
		ui.adminInput = ""
		raw := strings.NewReplacer(",", " ", "\n", " ", ";", " ").Replace(text)
		for _, f := range strings.Fields(raw) {
			id, err := strconv.ParseInt(f, 10, 64)
			if err != nil || id == 0 {
				continue
			}
			if a.store != nil {
				if u, _ := a.store.GetUser(ctx, id); u != nil {
					_ = a.store.SetWhitelisted(ctx, id, true)
				} else {
					_ = a.store.AddWhitelistID(ctx, id)
				}
			}
		}
		a.showWhitelist(ctx, chatID)
	case "wh_domain":
		ui.adminInput = ""
		d := strings.TrimSpace(text)
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Webhook.Domain = d
			if d != "" {
				a.botCfg.Webhook.PublicBaseURL = "https://" + d
			}
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showWebhooksAdmin(ctx, chatID)
	case "ref_value":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if n < 0 {
			n = 0
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.NormalizeReferral()
			a.botCfg.Referral.BonusValue = n
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showReferralAdmin(ctx, chatID)
	case "ref_invitee_value":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if n < 0 {
			n = 0
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.NormalizeReferral()
			a.botCfg.Referral.InviteeValue = n
			if n > 0 && a.botCfg.Referral.InviteeKind == "" {
				a.botCfg.Referral.InviteeKind = model.ReferralBonusBalance
			}
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showReferralAdmin(ctx, chatID)
	case "ref_percent":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if n < 0 {
			n = 0
		}
		if n > 100 {
			n = 100
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.NormalizeReferral()
			a.botCfg.Referral.Percent = n
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showReferralAdmin(ctx, chatID)
	case "cab_path":
		ui.adminInput = ""
		a.setCabinetPath(ctx, chatID, text)
	case "cab_title":
		ui.adminInput = ""
		a.setCabinetField(ctx, chatID, "title", text)
	case "cab_desc":
		ui.adminInput = ""
		a.setCabinetField(ctx, chatID, "desc", text)
	case "cab_favicon":
		ui.adminInput = ""
		a.setCabinetField(ctx, chatID, "favicon", text)
	case "device_per":
		mo := ui.priceMonths
		ui.adminInput = ""
		ui.priceMonths = 0
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setDevicesPer(mo, n)
		_ = a.saveBotConfig(ctx)
		a.showPricing(ctx, chatID)
	case "ntf_trial_days":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if n < 0 {
			n = 0
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Reminders.TrialDaysBefore = n
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showNotifyAdmin(ctx, chatID)
	case "trial_days":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrialDays(n)
		_ = a.saveBotConfig(ctx)
		a.showTrialAdmin(ctx, chatID)
	case "trial_gb":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrialGB(n)
		_ = a.saveBotConfig(ctx)
		a.showTrialAdmin(ctx, chatID)
	case "addsub_gb":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if n < 0 {
			n = 0
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.AddSub.TrafficGB = n
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showAddSubAdmin(ctx, chatID)
	case "trial_hwid":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrialHWID(n)
		_ = a.saveBotConfig(ctx)
		a.showTrialAdmin(ctx, chatID)
	case "trial_q_days":
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrialDays(n)
		ui.adminInput = "trial_q_gb"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "trial.q_gb"), "menu:trial")
	case "trial_q_gb":
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrialGB(n)
		ui.adminInput = "trial_q_hwid"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "trial.q_hwid"), "menu:trial")
	case "plan_q_price":
		mo := ui.priceMonths
		a.setBasePrice(mo, strings.TrimSpace(text))
		ui.adminInput = "plan_q_traffic"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "pricing.q_traffic", mo), "menu:pricing")
	case "plan_q_traffic":
		mo := ui.priceMonths
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.mu.Lock()
		if a.botCfg != nil {
			if a.botCfg.Pricing.Traffic == nil {
				a.botCfg.Pricing.Traffic = map[int]int{}
			}
			a.botCfg.Pricing.Traffic[mo] = n
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		ui.adminInput = "plan_q_hwid"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "pricing.q_hwid", mo), "menu:pricing")
	case "plan_q_hwid":
		mo := ui.priceMonths
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setDevicesPer(mo, n)
		ui.adminInput = ""
		ui.priceMonths = 0
		_ = a.saveBotConfig(ctx)
		a.showPlanSquads(ctx, chatID, mo)
	case "trial_q_hwid":
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrialHWID(n)
		ui.adminInput = ""
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Trial.Enabled = true
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showTrialAdmin(ctx, chatID)
	}
}

func curSuffix(cur string) string {
	if cur == "" {
		return ""
	}
	return " " + cur
}

const curRUB = "₽"

func p2pExt(id int64) string { return "p2p:" + strconv.FormatInt(id, 10) }

func (a *App) curFor(string) string { return curRUB }

func splitTrim(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (a *App) formatFiatPrices(method string) string {
	pr := a.pricing()
	var parts []string
	for _, mo := range model.PlanMonths {
		if v := pr.Fiat(method, mo); v != "" {
			parts = append(parts, strconv.Itoa(mo)+"м="+v)
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

func (a *App) setFiatPrice(method string, months int, val string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	a.botCfg.NormalizePricing()
	switch method {
	case model.PayMethodP2P:
		a.botCfg.Pricing.P2P[months] = val
	case model.PayMethodYooKassa:
		a.botCfg.Pricing.YooKassa[months] = val
	}
}

func (a *App) setBasePrice(months int, val string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	a.botCfg.NormalizePricing()
	a.botCfg.Pricing.Base[months] = val
}

func (a *App) setCurrency(val string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	a.botCfg.NormalizePricing()
	a.botCfg.Pricing.Currency = val
}

func (a *App) setStarPrice(months, val int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	a.botCfg.NormalizePricing()
	a.botCfg.Pricing.Stars[months] = val
}

func (a *App) cleanupP2PUser(ctx context.Context, userChatID int64) {
	a.mu.Lock()
	ui, ok := a.ui[userChatID]
	a.mu.Unlock()
	if !ok || ui == nil {
		return
	}
	if ui.p2pShotMsgID != 0 {
		a.msg.Delete(ctx, userChatID, ui.p2pShotMsgID)
		ui.p2pShotMsgID = 0
	}
	if ui.p2pSubmitMsgID != 0 {
		a.msg.Delete(ctx, userChatID, ui.p2pSubmitMsgID)
		ui.p2pSubmitMsgID = 0
	}
}

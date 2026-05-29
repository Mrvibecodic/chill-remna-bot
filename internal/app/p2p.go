package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
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
		a.sendKB(ctx, chatID, i18n.T(lang, "buy.no_methods"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "balance.btn_topup"), "menu:topup")}, homeRow(lang),
		})
		return
	}

	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "balance.btn_topup"), "menu:topup")})
	rows = append(rows, homeRow(lang))
	a.sendKB(ctx, chatID, i18n.T(lang, "buy.choose_method", kopecksToRub(bal)), rows)
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
	a.notifyKB(ctx, a.cfg.AdminID, i18n.T(lang, "admin.user_request", a.userLabelByID(ctx, userID)), [][]models.InlineKeyboardButton{{
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
	a.botCfg.NormalizePricing()
	p2p := a.botCfg.P2P
	pr := a.botCfg.Pricing
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
	price := pr.Fiat(model.PayMethodP2P, months)
	cur := curRUB
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
		a.send(ctx, chatID, "❌ "+err.Error())
		return
	}
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
	a.sendKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "admin.btn_toggle"), "adm:toggle"), btn(i18n.T(lang, "admin.btn_rotate"), "adm:rotate")},
		{btn(i18n.T(lang, "admin.btn_cards"), "adm:cards"), btn(i18n.T(lang, "admin.btn_prices"), "adm:prices")},
		{btn(i18n.T(lang, "admin.btn_squad"), "sq:pick")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onAdmin(ctx context.Context, chatID int64, val string, srcMsgID int) {
	action, arg, _ := strings.Cut(val, ":")

	switch action {
	case "uok", "uno", "pok", "pno":
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
	amount := req.Price + curSuffix(a.curFor(model.PayMethodP2P))
	link, expireAt, err := a.finalizePurchase(ctx, req.TelegramID, req.Months, model.PayMethodP2P, amount, "")
	if err != nil {
		a.send(ctx, adminChat, i18n.T(alang, "admin.provision_fail", err.Error()))
		return
	}
	req.Status = model.P2PApproved
	req.DecidedAt = time.Now().UTC().Format(time.RFC3339)
	_ = a.store.UpdateP2PRequest(ctx, req)
	a.cleanupP2PUser(ctx, req.TelegramID)
	a.sendSubActive(ctx, req.TelegramID, link, expireAt)
	a.send(ctx, adminChat, i18n.T(alang, "admin.done"))
}

func (a *App) finalizePurchase(ctx context.Context, telegramID int64, months int, method, amount, extID string) (string, string, error) {
	a.mu.Lock()
	panel := a.panel
	limits := remnawave.UserLimits{}
	if a.botCfg != nil {

		limits.InternalSquads = a.botCfg.Plan.ActiveInternalSquads
		limits.ExternalSquad = a.botCfg.Plan.ExternalSquadUUID

		if len(limits.InternalSquads) == 0 && a.botCfg.P2P.SquadUUID != "" {
			limits.InternalSquads = []string{a.botCfg.P2P.SquadUUID}
		}
		limits.TrafficBytes = a.botCfg.Pricing.TrafficBytes(months)
		limits.DeviceLimit = a.botCfg.Pricing.DeviceLimitFor(months)
		limits.Strategy = a.botCfg.Pricing.ResetStrategy()
	}
	a.mu.Unlock()
	if panel == nil {
		return "", "", fmt.Errorf("панель не подключена")
	}
	link, expireAt, err := panel.CreateOrUpdateUser(ctx, telegramID, months, limits)
	if err != nil {
		return "", "", err
	}
	link = a.rewriteSub(link)
	a.invalidateSubCache(telegramID)
	if a.store != nil {
		_ = a.store.AddPayment(ctx, &model.Payment{
			TelegramID: telegramID, Method: method, Months: months, Amount: amount, Status: model.PaymentPaid, ExtID: extID,
		})
		_ = a.store.SetSubExpiry(ctx, telegramID, expireAt, "paid")
	}
	a.grantReferralBonus(ctx, telegramID)
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
			a.send(ctx, chatID, i18n.T(lang, "admin.not_found"))
			return
		}
		req.Status = model.P2PRejected
		req.Comment = text
		req.DecidedAt = time.Now().UTC().Format(time.RFC3339)
		_ = a.store.UpdateP2PRequest(ctx, req)
		_ = a.store.AddPayment(ctx, &model.Payment{
			TelegramID: req.TelegramID, Method: model.PayMethodP2P, Months: req.Months,
			Amount: req.Price + curSuffix(a.curFor(model.PayMethodP2P)), Status: model.PaymentRejected, Comment: text,
		})
		a.cleanupP2PUser(ctx, req.TelegramID)
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
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Webhook.ListenAddr = text
		}
		a.mu.Unlock()
		ui.adminInput = ""
		_ = a.saveBotConfig(ctx)
		a.showWebhooksAdmin(ctx, chatID)
	case "wh_base":
		text = strings.TrimSpace(text)

		text = strings.TrimRight(text, "/")
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
	case "cbprice":
		mo := ui.priceMonths
		ui.adminInput = ""
		ui.priceMonths = 0
		a.mu.Lock()
		if a.botCfg != nil {
			if a.botCfg.CryptoBot.Prices == nil {
				a.botCfg.CryptoBot.Prices = map[int]string{}
			}
			a.botCfg.CryptoBot.Prices[mo] = strings.TrimSpace(text)
		}
		a.mu.Unlock()
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
		a.showPricing(ctx, chatID)
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

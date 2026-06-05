package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

const usersPageSize = 8

func (a *App) rememberUser(ctx context.Context, chatID int64, username, firstName string) {
	if a.store == nil || (username == "" && firstName == "") {
		return
	}
	_ = a.store.SetUserInfo(ctx, chatID, username, firstName)
}

func (a *App) userBlocked(ctx context.Context, chatID int64) bool {
	if chatID == a.cfg.AdminID || a.store == nil {
		return false
	}
	u, err := a.store.GetUser(ctx, chatID)
	return err == nil && u != nil && u.Blocked
}

func (a *App) denyAccess(ctx context.Context, chatID int64, isAdmin bool) bool {
	if isAdmin {
		return false
	}
	if a.userBlocked(ctx, chatID) {
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "user.you_blocked"))
		return true
	}
	a.mu.Lock()
	wl := a.botCfg != nil && a.botCfg.WhitelistMode
	a.mu.Unlock()
	if wl && a.store != nil {
		u, _ := a.store.GetUser(ctx, chatID)
		if u == nil || !u.Whitelisted {
			a.send(ctx, chatID, i18n.T(a.lang(chatID), "user.not_whitelisted"))
			return true
		}
	}
	return false
}

func (a *App) showUsers(ctx context.Context, chatID int64, page int) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	if page < 0 {
		page = 0
	}
	users, total, err := a.store.ListUsers(ctx, usersPageSize, page*usersPageSize)
	if err != nil {
		a.send(ctx, chatID, "❌ "+err.Error())
		return
	}
	if total == 0 {
		a.sendKB(ctx, chatID, i18n.T(lang, "users.empty"),
			[][]models.InlineKeyboardButton{homeRow(lang)})
		return
	}
	pages := (total + usersPageSize - 1) / usersPageSize

	a.mu.Lock()
	wlMode := a.botCfg != nil && a.botCfg.WhitelistMode
	a.mu.Unlock()
	wlLabel := i18n.T(lang, "users.wl_off")
	if wlMode {
		wlLabel = i18n.T(lang, "users.wl_on")
	}
	rows := [][]models.InlineKeyboardButton{{btn(wlLabel, "usr:wlmode")}}
	for _, u := range users {
		label := "👤 " + userLabel(&u)
		if u.Blocked {
			label += " 🚫"
		}
		rows = append(rows, []models.InlineKeyboardButton{
			btn(label, "usr:view:"+strconv.FormatInt(u.TelegramID, 10)),
		})
	}
	var nav []models.InlineKeyboardButton
	if page > 0 {
		nav = append(nav, btn(i18n.T(lang, "btn.prev"), "usr:page:"+strconv.Itoa(page-1)))
	}
	if page+1 < pages {
		nav = append(nav, btn(i18n.T(lang, "btn.next"), "usr:page:"+strconv.Itoa(page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, homeRow(lang))

	a.sendKBSection(ctx, chatID, assets.SectionReferral, i18n.T(lang, "users.title", total, page+1, pages), rows)
}

func (a *App) showUser(ctx context.Context, chatID, uid int64) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	u, err := a.store.GetUser(ctx, uid)
	if err != nil {
		a.send(ctx, chatID, "❌ "+err.Error())
		return
	}
	if u == nil {
		a.showUsers(ctx, chatID, 0)
		return
	}
	created := u.CreatedAt
	if len(created) >= 10 {
		created = created[:10]
	}
	if created == "" {
		created = "—"
	}
	p2p := i18n.T(lang, "user.no")
	if u.P2PApproved {
		p2p = i18n.T(lang, "user.yes")
	}
	status := i18n.T(lang, "user.active")
	if u.Blocked {
		status = i18n.T(lang, "user.blocked")
	}
	id := strconv.FormatInt(uid, 10)
	var toggle models.InlineKeyboardButton
	if u.Blocked {
		toggle = btn(i18n.T(lang, "btn.unblock"), "usr:unblock:"+id)
	} else {
		toggle = btn(i18n.T(lang, "btn.block"), "usr:block:"+id)
	}
	var wlBtn models.InlineKeyboardButton
	if u.Whitelisted {
		wlBtn = btn(i18n.T(lang, "btn.whitelist_del"), "usr:wloff:"+id)
	} else {
		wlBtn = btn(i18n.T(lang, "btn.whitelist_add"), "usr:wlon:"+id)
	}
	var p2pBtn models.InlineKeyboardButton
	if u.P2PApproved {
		p2pBtn = btn(i18n.T(lang, "btn.p2p_deny"), "usr:p2poff:"+id)
	} else {
		p2pBtn = btn(i18n.T(lang, "btn.p2p_allow"), "usr:p2pon:"+id)
	}
	subBlock := i18n.T(lang, "user.no_sub")
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if panel != nil {
		if url, exp, ok := panel.Subscription(ctx, uid); ok {
			subBlock = i18n.T(lang, "user.sub_active", formatExpire(exp, lang), a.rewriteSub(url))
		}
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "user.card", userLabel(u), created, p2p, status, subBlock), [][]models.InlineKeyboardButton{
		{p2pBtn, wlBtn},
		{toggle, btn(i18n.T(lang, "btn.delete"), "usr:del:"+id)},
		{btn(i18n.T(lang, "btn.link_panel"), "usr:link:"+id)},
		{btn(i18n.T(lang, "btn.back"), "usr:list"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onUsers(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	switch action {
	case "list":
		a.showUsers(ctx, chatID, 0)
	case "page":
		page, _ := strconv.Atoi(arg)
		a.showUsers(ctx, chatID, page)
	case "view":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		a.showUser(ctx, chatID, uid)
	case "block", "unblock":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		if a.store != nil {
			_ = a.store.SetBlocked(ctx, uid, action == "block")
		}
		if action == "unblock" && uid != 0 {
			ulang := a.lang(uid)
			a.msg.SendKB(ctx, uid, a.applyPremium(i18n.T(ulang, "guard.user_unblocked")),
				[][]models.InlineKeyboardButton{homeRow(ulang)})
		}
		a.showUser(ctx, chatID, uid)
	case "wlon", "wloff":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		if a.store != nil {
			_ = a.store.SetWhitelisted(ctx, uid, action == "wlon")
		}
		a.showUser(ctx, chatID, uid)
	case "wlmode":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.WhitelistMode = !a.botCfg.WhitelistMode
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showUsers(ctx, chatID, 0)
	case "p2pon", "p2poff":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		allow := action == "p2pon"
		if a.store != nil {
			_ = a.store.SetP2PApproved(ctx, uid, allow)
		}
		if allow {
			a.notify(ctx, uid, i18n.T(a.lang(uid), "p2p.user_approved"))
		}
		a.showUser(ctx, chatID, uid)
	case "del":
		lang := a.lang(chatID)
		a.sendKB(ctx, chatID, i18n.T(lang, "user.del_ask", arg), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.del_with_sub"), "usr:delfull:"+arg)},
			{btn(i18n.T(lang, "btn.del_bot_only"), "usr:delbot:"+arg)},
			{btn(i18n.T(lang, "btn.back"), "usr:view:"+arg)},
		})
	case "link":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		if uid == 0 {
			return
		}
		ui := a.getUI(chatID)
		ui.linkUID = uid
		ui.adminInput = "link_panel"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "user.link_ask", uid), "usr:view:"+arg)
	case "delfull":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		a.adminDeleteUser(ctx, chatID, uid, true)
		a.showUsers(ctx, chatID, 0)
	case "delbot":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		a.adminDeleteUser(ctx, chatID, uid, false)
		a.showUsers(ctx, chatID, 0)
	}
}

func (a *App) adminDeleteUser(ctx context.Context, adminChat, uid int64, deleteSub bool) {
	if a.store == nil {
		return
	}
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if deleteSub && panel != nil {
		if _, err := panel.DeleteByTelegramID(ctx, uid); err != nil {
			a.notify(ctx, adminChat, "⚠️ "+err.Error())
		}
	}
	a.invalidateSubCache(uid)
	_ = a.store.DeletePaymentsByUser(ctx, uid)
	_ = a.store.DeleteP2PRequestsByUser(ctx, uid)
	_ = a.store.DeleteUser(ctx, uid)
}

func payMethodLabel(method string) string {
	switch method {
	case "stars":
		return "⭐"
	case "p2p":
		return "P2P"
	}
	return method
}

func paymentTotals(ps []model.Payment) (users int, sums string) {
	seen := map[int64]struct{}{}
	byUnit := map[string]float64{}
	var order []string
	for _, p := range ps {
		seen[p.TelegramID] = struct{}{}
		fields := strings.Fields(p.Amount)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(strings.Replace(fields[0], ",", ".", 1), 64)
		if err != nil {
			continue
		}
		unit := strings.TrimSpace(strings.Join(fields[1:], " "))
		if _, ok := byUnit[unit]; !ok {
			order = append(order, unit)
		}
		byUnit[unit] += v
	}
	users = len(seen)
	var parts []string
	for _, u := range order {
		num := strconv.FormatFloat(byUnit[u], 'f', -1, 64)
		if u != "" {
			num += " " + u
		}
		parts = append(parts, num)
	}
	return users, strings.Join(parts, " · ")
}

func (a *App) showPayments(ctx context.Context, chatID int64, page int) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	if page < 0 {
		page = 0
	}
	items, total, err := a.store.ListPayments(ctx, usersPageSize, page*usersPageSize)
	if err != nil {
		a.send(ctx, chatID, "❌ "+err.Error())
		return
	}
	back := []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")}
	if total == 0 {
		a.sendKB(ctx, chatID, i18n.T(lang, "payments.empty"), [][]models.InlineKeyboardButton{back})
		return
	}
	pages := (total + usersPageSize - 1) / usersPageSize

	type row struct{ date, method, user, term, amount, status string }
	rows := make([]row, 0, len(items))
	wMethod, wUser, wAmount := len("Method"), len("User"), len("Amount")
	for _, p := range items {
		date := p.CreatedAt
		if len(date) >= 10 {
			date = date[:10]
		}
		statusKey := "payments.st_paid"
		if p.Status == model.PaymentRejected {
			statusKey = "payments.st_rejected"
		}
		user := strconv.FormatInt(p.TelegramID, 10)
		term := strconv.Itoa(p.Months) + "m"
		method := payMethodLabel(p.Method)
		amount := p.Amount
		rows = append(rows, row{date, method, user, term, amount, i18n.T(lang, statusKey)})
		if l := visualWidth(method); l > wMethod {
			wMethod = l
		}
		if l := visualWidth(user); l > wUser {
			wUser = l
		}
		if l := visualWidth(amount); l > wAmount {
			wAmount = l
		}
	}

	var sb strings.Builder
	sb.WriteString(i18n.T(lang, "payments.title", total, page+1, pages))
	if paid, err := a.store.PaidPayments(ctx); err == nil {
		users, sums := paymentTotals(paid)
		if sums == "" {
			sums = "—"
		}
		sb.WriteString("\n" + i18n.T(lang, "payments.totals", users, sums))
	}
	sb.WriteString("\n<pre>")
	header := padRight("Date", 10) + "  " + padRight("Method", wMethod) + "  " +
		padRight("User", wUser) + "  " + padRight("Term", 4) + "  " +
		padRight("Amount", wAmount) + "  " + "Status"
	sb.WriteString(header)
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", visualWidth(header)))
	for _, r := range rows {
		sb.WriteString("\n")
		sb.WriteString(padRight(r.date, 10))
		sb.WriteString("  ")
		sb.WriteString(padRight(r.method, wMethod))
		sb.WriteString("  ")
		sb.WriteString(padRight(r.user, wUser))
		sb.WriteString("  ")
		sb.WriteString(padRight(r.term, 4))
		sb.WriteString("  ")
		sb.WriteString(padRight(r.amount, wAmount))
		sb.WriteString("  ")
		sb.WriteString(r.status)
	}
	sb.WriteString("</pre>")

	var kbRows [][]models.InlineKeyboardButton
	var nav []models.InlineKeyboardButton
	if page > 0 {
		nav = append(nav, btn(i18n.T(lang, "btn.prev"), "pay:page:"+strconv.Itoa(page-1)))
	}
	if page+1 < pages {
		nav = append(nav, btn(i18n.T(lang, "btn.next"), "pay:page:"+strconv.Itoa(page+1)))
	}
	if len(nav) > 0 {
		kbRows = append(kbRows, nav)
	}
	kbRows = append(kbRows, []models.InlineKeyboardButton{btn(i18n.T(lang, "paylog.btn"), "pay:log")})
	kbRows = append(kbRows, back)
	a.sendKB(ctx, chatID, sb.String(), kbRows)
}

func padRight(s string, w int) string {
	cur := visualWidth(s)
	if cur >= w {
		return s
	}
	return s + strings.Repeat(" ", w-cur)
}

func visualWidth(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func (a *App) onPayments(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	switch action {
	case "page":
		page, _ := strconv.Atoi(arg)
		a.showPayments(ctx, chatID, page)
	case "log":
		a.getUI(chatID).adminInput = "paylog"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "paylog.ask"), "menu:payments")
	}
}

func (a *App) showMySubs(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	home := []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.home"), "menu:home")}
	var url, expireAt string
	ok := false
	if panel != nil {
		url, expireAt, ok = panel.Subscription(ctx, chatID)
		if ok {
			url = a.rewriteSub(url)
		}
	}
	if !ok {
		a.sendKB(ctx, chatID, i18n.T(lang, "subs.none"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.buy"), "menu:buy")}, home,
		})
		return
	}
	rows := [][]models.InlineKeyboardButton{}
	if sup := a.supportURL(); sup != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(lang, "btn.support"), URL: sup}})
	}
	rows = append(rows, home)
	a.sendKBSection(ctx, chatID, assets.SectionMySubscription, a.subActiveText(ctx, chatID, url, expireAt), rows)
}

func (a *App) showSquadPicker(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	panel := a.panel
	cur := ""
	if a.botCfg != nil {
		cur = a.botCfg.P2P.SquadUUID
	}
	a.mu.Unlock()

	curLabel := cur
	if curLabel == "" {
		curLabel = i18n.T(lang, "squad.none")
	}

	manualRow := []models.InlineKeyboardButton{btn(i18n.T(lang, "squad.manual"), "sq:manual")}
	backRow := []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:p2p"), btn(i18n.T(lang, "btn.home"), "menu:home")}

	if panel == nil {
		a.sendKB(ctx, chatID, i18n.T(lang, "squad.fail", i18n.T(lang, "admin.none")),
			[][]models.InlineKeyboardButton{manualRow, backRow})
		return
	}
	squads, err := panel.ListSquads(ctx)
	if err != nil {
		a.sendKB(ctx, chatID, i18n.T(lang, "squad.fail", err.Error()),
			[][]models.InlineKeyboardButton{manualRow, backRow})
		return
	}
	var rows [][]models.InlineKeyboardButton
	for _, sq := range squads {
		if sq.UUID == "" {
			continue
		}
		name := sq.Name
		if name == "" {
			name = sq.UUID
		}
		if sq.UUID == cur {
			name = "✅ " + name
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(name, "sq:set:"+sq.UUID)})
	}
	if len(rows) == 0 {
		a.sendKB(ctx, chatID, i18n.T(lang, "squad.empty"),
			[][]models.InlineKeyboardButton{manualRow, backRow})
		return
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "squad.clear"), "sq:set:-"), btn(i18n.T(lang, "squad.refresh"), "sq:refresh")},
		manualRow, backRow)
	a.sendKB(ctx, chatID, i18n.T(lang, "squad.title", curLabel), rows)
}

func (a *App) onSquad(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	lang := a.lang(chatID)
	switch action {
	case "pick", "refresh":
		a.showSquadPicker(ctx, chatID)
	case "manual":
		a.getUI(chatID).adminInput = "squad"
		a.askInput(ctx, chatID, i18n.T(lang, "admin.ask_squad"), "menu:users")
	case "set":
		v := arg
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
	}
}

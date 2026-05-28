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

// rememberUser обновляет ник/имя из Telegram у уже существующей записи (новую не создаёт).
func (a *App) rememberUser(ctx context.Context, chatID int64, username, firstName string) {
	if a.store == nil || (username == "" && firstName == "") {
		return
	}
	_ = a.store.SetUserInfo(ctx, chatID, username, firstName)
}

// userBlocked сообщает, ограничен ли доступ к боту для этого chatID (только не-админ).
func (a *App) userBlocked(ctx context.Context, chatID int64) bool {
	if chatID == a.cfg.AdminID || a.store == nil {
		return false
	}
	u, err := a.store.GetUser(ctx, chatID)
	return err == nil && u != nil && u.Blocked
}

// --- админ: раздел «Пользователи» (список / карточка / блок / удаление) ---

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
			[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.back"), "menu:manage")}, homeRow(lang)})
		return
	}
	pages := (total + usersPageSize - 1) / usersPageSize

	var rows [][]models.InlineKeyboardButton
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
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:manage")}, homeRow(lang))

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
	var p2pBtn models.InlineKeyboardButton
	if u.P2PApproved {
		p2pBtn = btn(i18n.T(lang, "btn.p2p_deny"), "usr:p2poff:"+id)
	} else {
		p2pBtn = btn(i18n.T(lang, "btn.p2p_allow"), "usr:p2pon:"+id)
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "user.card", userLabel(u), created, p2p, status), [][]models.InlineKeyboardButton{
		{p2pBtn},
		{toggle, btn(i18n.T(lang, "btn.delete"), "usr:del:"+id)},
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
		a.showUser(ctx, chatID, uid)
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
		a.sendKB(ctx, chatID, i18n.T(lang, "user.card_confirm_del", arg), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.del_confirm"), "usr:delc:"+arg)},
			{btn(i18n.T(lang, "btn.back"), "usr:view:"+arg)},
		})
	case "delc":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		a.adminDeleteUser(ctx, chatID, uid)
		a.showUsers(ctx, chatID, 0)
	}
}

// adminDeleteUser — каскадное удаление пользователя:
//  1. DISABLE его подписки в Remnawave (best-effort: если панель недоступна,
//     не блокируем удаление; чужие аккаунты — не трогаем по правилу безопасности).
//  2. Локально вычищаем payments и p2p_requests этого telegram_id, чтобы после
//     повторной регистрации /start показывал «Купить» (а не «Мои подписки» по
//     старому логу).
//  3. Удаляем саму запись users.
//
// Об ошибке disable админу пишем подсказкой, но удаление НЕ откатываем.
func (a *App) adminDeleteUser(ctx context.Context, adminChat, uid int64) {
	if a.store == nil {
		return
	}
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if panel != nil {
		if _, err := panel.DisableByTelegramID(ctx, uid); err != nil {
			a.notify(ctx, adminChat, "⚠️ "+err.Error())
		}
	}
	a.invalidateSubCache(uid)
	_ = a.store.DeletePaymentsByUser(ctx, uid)
	_ = a.store.DeleteP2PRequestsByUser(ctx, uid)
	_ = a.store.DeleteUser(ctx, uid)
}

// --- админ: лог оплат ---

func payMethodLabel(method string) string {
	switch method {
	case "stars":
		return "⭐"
	case "p2p":
		return "P2P"
	}
	return method
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
	back := []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:manage"), btn(i18n.T(lang, "btn.home"), "menu:home")}
	if total == 0 {
		a.sendKB(ctx, chatID, i18n.T(lang, "payments.empty"), [][]models.InlineKeyboardButton{back})
		return
	}
	pages := (total + usersPageSize - 1) / usersPageSize

	// Подготавливаем колонки одинаковой ширины: дата (10), метод (8), юзер (12),
	// срок (3), сумма (10), статус (10). Внутри <pre> моноширинный шрифт
	// делает столбцы аккуратной таблицей.
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
	kbRows = append(kbRows, back)
	a.sendKBSection(ctx, chatID, assets.SectionPromoCode, sb.String(), kbRows)
}

// padRight дополняет строку пробелами справа до ширины w (на основе видимой
// длины строки, корректно для эмодзи/кириллицы — каждая «руна» = 1 символ).
func padRight(s string, w int) string {
	cur := visualWidth(s)
	if cur >= w {
		return s
	}
	return s + strings.Repeat(" ", w-cur)
}

// visualWidth возвращает количество run в строке (моноширинный шрифт в <pre>
// отдаёт примерно одинаковую ширину для большинства печатных run, включая
// латиницу/кириллицу/цифры/эмодзи — достаточно для выравнивания таблицы).
func visualWidth(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func (a *App) onPayments(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	if action == "page" {
		page, _ := strconv.Atoi(arg)
		a.showPayments(ctx, chatID, page)
	}
}

// --- пользователь: мои подписки ---

func (a *App) showMySubs(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	home := []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.home"), "menu:home")}
	var url string
	ok := false
	if panel != nil {
		url, ok = panel.Subscription(ctx, chatID)
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
	a.sendKBSection(ctx, chatID, assets.SectionMySubscription, i18n.T(lang, "subs.show", url), [][]models.InlineKeyboardButton{home})
}

// --- админ: выбор сквада из панели ---

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

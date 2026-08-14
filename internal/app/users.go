package app

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
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
	// Режим публичности: «публично» пускает всех, «по приглашениям» и «белый
	// список» — только тех, кому доступ уже выдан (приглашение выдаёт тот же
	// флаг, поэтому смена режима никого не выкидывает).
	switch a.accessMode() {
	case model.AccessInvite:
		if a.store != nil && !a.accessGranted(ctx, chatID) {
			a.send(ctx, chatID, i18n.T(a.lang(chatID), "user.need_invite"))
			return true
		}
	case model.AccessWhitelist:
		if a.store != nil && !a.accessGranted(ctx, chatID) {
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
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	if total == 0 {
		// Раздел «Торренты» доступен и на пустом списке: отчёты панели приходят
		// и по тем, кого бот ещё не видел.
		a.sendUsrKB(ctx, chatID, i18n.T(lang, "users.empty"),
			[][]models.InlineKeyboardButton{
				{btn(i18n.T(lang, "btn.torrents"), "torj:home")},
				homeRow(lang),
			})
		return
	}
	pages := (total + usersPageSize - 1) / usersPageSize

	mode := a.accessMode()
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "access.btn_open", i18n.T(lang, "access.mode_"+mode)), "menu:access")},
		{btn(i18n.T(lang, "btn.wl_add_id"), "usr:wladd"), btn(i18n.T(lang, "btn.wl_list"), "usr:wllist")},
		{btn(i18n.T(lang, "btn.torrents"), "torj:home")},
	}
	for _, u := range users {
		label := "👤 " + userLabel(&u)
		if u.Blocked {
			label += " 🚫"
		}
		rows = append(rows, []models.InlineKeyboardButton{
			btn(label, "usr:view:"+strconv.FormatInt(u.TelegramID, 10)),
		})
	}
	nav := paginationRow("usr:page:", page, pages, i18n.T(lang, "btn.prev"), i18n.T(lang, "btn.next"))
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, homeRow(lang))

	a.sendKBSection(ctx, chatID, assets.SectionReferral, i18n.T(lang, "users.title", total, page+1, pages), rows)
}

func (a *App) reconcileWhitelist(ctx context.Context, chatID int64) {
	if a.store == nil {
		return
	}
	if ok, _ := a.store.IsWhitelistID(ctx, chatID); ok {
		_ = a.store.SetWhitelisted(ctx, chatID, true)
		_ = a.store.RemoveWhitelistID(ctx, chatID)
	}
}

// showWhitelist — кто на самом деле имеет доступ. Раньше этот экран показывал
// только предзаполненные ID, а они по устройству опустошаются при первом входе
// (см. reconcileWhitelist) — поэтому список выглядел пустым даже тогда, когда
// доступ был выдан всей базе.
func (a *App) showWhitelist(ctx context.Context, chatID int64, page int) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	if page < 0 {
		page = 0
	}
	users, total, err := a.store.ListWhitelistedUsers(ctx, usersPageSize, page*usersPageSize)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	ids := 0
	if list, err := a.store.ListWhitelistIDs(ctx); err == nil {
		ids = len(list)
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
	if nav := paginationRow("usr:wlpage:", page, pages, i18n.T(lang, "btn.prev"), i18n.T(lang, "btn.next")); len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.wl_add_id"), "usr:wladd"),
		btn(i18n.T(lang, "btn.wl_ids", ids), "usr:wlids"),
	})
	if total > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.wl_clear"), "usr:wlclear")})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:users")})

	title := i18n.T(lang, "wl.granted_title", total, page+1, max(pages, 1))
	if total == 0 {
		title = i18n.T(lang, "wl.granted_empty")
	}
	a.sendUsrKB(ctx, chatID, title, rows)
}

// showWhitelistIDs — предзаполненный список: Telegram ID тех, кто ещё не
// заходил в бота. При первом входе ID отсюда уезжает в доступ пользователя.
func (a *App) showWhitelistIDs(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	ids, err := a.store.ListWhitelistIDs(ctx)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.wl_add_id"), "usr:wladd")},
	}
	for _, id := range ids {
		sid := strconv.FormatInt(id, 10)
		rows = append(rows, []models.InlineKeyboardButton{
			btn("🗑 "+sid, "usr:wldel:"+sid),
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "usr:wllist")})
	title := i18n.T(lang, "wl.list_title", len(ids))
	if len(ids) == 0 {
		title = i18n.T(lang, "wl.list_empty")
	}
	a.sendUsrKB(ctx, chatID, title, rows)
}

// clearWhitelistAll снимает доступ со всех разом. Обратная операция к выдаче
// доступа всей базе — без неё закрытый бот нечем закрыть на самом деле.
func (a *App) clearWhitelistAll(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	n, err := a.store.ClearWhitelistAll(ctx)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	a.log.Info("access: доступ снят со всех", "count", n)
	a.notify(ctx, chatID, i18n.T(lang, "wl.cleared", n))
	a.showWhitelist(ctx, chatID, 0)
}

func (a *App) showUser(ctx context.Context, chatID, uid int64) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	u, err := a.store.GetUser(ctx, uid)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
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
	id := strconv.FormatInt(uid, 10)
	botBlocked := u.Blocked
	status := i18n.T(lang, "user.active")
	if botBlocked {
		status = i18n.T(lang, "user.blocked")
	}
	whitelisted := u.Whitelisted
	if !whitelisted && a.store != nil {
		if ok, _ := a.store.IsWhitelistID(ctx, uid); ok {
			whitelisted = true
		}
	}
	var wlBtn models.InlineKeyboardButton
	if whitelisted {
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
	subExists, subBlocked := false, false
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if panel != nil {
		if url, exp, st, ok := panel.SubscriptionFull(ctx, uid); ok {
			subExists = true
			subBlocked = st == remnawave.StatusDisabled
			if subBlocked {
				subBlock = i18n.T(lang, "user.sub_blocked", a.rewriteSub(url))
			} else {
				subBlock = i18n.T(lang, "user.sub_active", formatExpire(exp, lang), a.rewriteSub(url))
			}
		}
	}
	// Нарушения показываем только тем, у кого они есть: пустая строка «0»
	// в каждой карточке — это шум.
	torLine, torCount := "", 0
	if a.store != nil {
		// Считаем за всё время: гейт по 30 дням прятал бы кнопку у нарушителя
		// со старой историей, и открыть её было бы неоткуда.
		torCount, _ = a.store.CountTorrentReports(ctx, uid, "", "")
		if torCount > 0 {
			last30, _ := a.store.CountTorrentReports(ctx, uid, "",
				time.Now().UTC().Add(-torrentRepeatWindow).Format(time.RFC3339))
			torLine = "\n" + i18n.T(lang, "user.torrents", torCount, last30)
		}
	}

	var actions []models.InlineKeyboardButton
	if !botBlocked || (subExists && !subBlocked) {
		actions = append(actions, btn(i18n.T(lang, "btn.block"), "usr:block:"+id))
	}
	if botBlocked || (subExists && subBlocked) {
		actions = append(actions, btn(i18n.T(lang, "btn.unblock"), "usr:unblock:"+id))
	}
	rows := [][]models.InlineKeyboardButton{{p2pBtn, wlBtn}}
	if len(actions) > 0 {
		rows = append(rows, actions)
	}
	if torCount > 0 {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "btn.torrent_user_log"), "torj:u:"+id),
		})
	}
	// Кнопка допусков к тарифам «по списку» показывается, только когда такие
	// тарифы есть: пустой экран из карточки — это шум.
	if a.hasListPlans(ctx) {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "btn.user_plans"), "usr:plans:"+id),
		})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "btn.delete"), "usr:del:"+id)},
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "btn.link_panel"), "usr:link:"+id)},
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "usr:list"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	)
	a.sendUsrKB(ctx, chatID, i18n.T(lang, "user.card", userLabel(u), created, p2p, status, subBlock)+torLine, rows)
}

func (a *App) userBlockState(ctx context.Context, uid int64) (botBlocked, subExists, subBlocked bool) {
	if a.store != nil {
		if u, _ := a.store.GetUser(ctx, uid); u != nil {
			botBlocked = u.Blocked
		}
	}
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if panel != nil {
		if _, _, st, ok := panel.SubscriptionFull(ctx, uid); ok {
			subExists = true
			subBlocked = st == remnawave.StatusDisabled
		}
	}
	return
}

func (a *App) onUsers(ctx context.Context, chatID int64, val string, srcMsgID int) {
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
	case "block":
		lang := a.lang(chatID)
		uid, _ := strconv.ParseInt(arg, 10, 64)
		botBlocked, subExists, subBlocked := a.userBlockState(ctx, uid)
		var rows [][]models.InlineKeyboardButton
		if !botBlocked && subExists && !subBlocked {
			rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "block.btn_both"), "usr:blockboth:"+arg)})
		}
		if subExists && !subBlocked {
			rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "block.btn_sub"), "usr:blocksub:"+arg)})
		}
		if !botBlocked {
			rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "block.btn_bot"), "usr:blockbot:"+arg)})
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "usr:view:"+arg)})
		a.sendUsrKB(ctx, chatID, i18n.T(lang, "block.ask", arg), rows)
	case "unblock":
		lang := a.lang(chatID)
		uid, _ := strconv.ParseInt(arg, 10, 64)
		botBlocked, subExists, subBlocked := a.userBlockState(ctx, uid)
		var rows [][]models.InlineKeyboardButton
		if botBlocked && subExists && subBlocked {
			rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "unblock.btn_both"), "usr:unblockboth:"+arg)})
		}
		if subExists && subBlocked {
			rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "unblock.btn_sub"), "usr:unblocksub:"+arg)})
		}
		if botBlocked {
			rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "unblock.btn_bot"), "usr:unblockbot:"+arg)})
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "usr:view:"+arg)})
		a.sendUsrKB(ctx, chatID, i18n.T(lang, "unblock.ask", arg), rows)
	case "blockboth", "blocksub", "blockbot":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		a.applyBlock(ctx, chatID, uid, action, srcMsgID)
	case "unblockboth", "unblocksub", "unblockbot":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		a.applyUnblock(ctx, chatID, uid, action, srcMsgID)
	case "wlon", "wloff":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		if a.store != nil {
			on := action == "wlon"
			_ = a.store.SetWhitelisted(ctx, uid, on)
			if !on {
				_ = a.store.RemoveWhitelistID(ctx, uid)
			}
		}
		a.showUser(ctx, chatID, uid)
	case "wlmode":
		// Старая кнопка «вайтлист вкл/выкл» из давних сообщений в чате: молча
		// менять режим по ней нельзя (можно случайно закрыть или открыть бота),
		// поэтому просто открываем экран доступа.
		a.showAccess(ctx, chatID)
	case "wladd":
		a.getUI(chatID).adminInput = "wl_add"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "wl.ask_ids"), "menu:users")
	case "wllist":
		a.showWhitelist(ctx, chatID, 0)
	case "wlpage":
		page, _ := strconv.Atoi(arg)
		a.showWhitelist(ctx, chatID, page)
	case "wlids":
		a.showWhitelistIDs(ctx, chatID)
	case "wlclear":
		lang := a.lang(chatID)
		a.sendUsrKB(ctx, chatID, i18n.T(lang, "wl.clear_ask"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.wl_clear_ok"), "usr:wlclearok")},
			{btn(i18n.T(lang, "btn.back"), "usr:wllist")},
		})
	case "wlclearok":
		a.clearWhitelistAll(ctx, chatID)
	case "wldel":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		if a.store != nil && uid != 0 {
			_ = a.store.RemoveWhitelistID(ctx, uid)
			_ = a.store.SetWhitelisted(ctx, uid, false)
		}
		a.showWhitelistIDs(ctx, chatID)
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
	case "plans":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		a.showUserPlanAccess(ctx, chatID, uid)
	case "pg":
		uidStr, code, _ := strings.Cut(arg, ":")
		uid, _ := strconv.ParseInt(uidStr, 10, 64)
		a.toggleUserPlanAccess(ctx, chatID, uid, code)
	case "del":
		lang := a.lang(chatID)
		a.sendUsrKB(ctx, chatID, i18n.T(lang, "user.del_ask", arg), [][]models.InlineKeyboardButton{
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

func (a *App) applyBlock(ctx context.Context, adminChat, uid int64, mode string, srcMsgID int) {
	if uid == 0 || a.store == nil {
		return
	}
	alang := a.lang(adminChat)
	wantBot := mode == "blockboth" || mode == "blockbot"
	wantSub := mode == "blockboth" || mode == "blocksub"
	didBot, didSub := false, false
	if wantBot {
		if err := a.store.SetBlocked(ctx, uid, true); err == nil {
			didBot = true
		}
	}
	if wantSub {
		a.mu.Lock()
		panel := a.panel
		a.mu.Unlock()
		if panel != nil {
			if _, err := panel.DisableByTelegramID(ctx, uid); err != nil {
				a.notify(ctx, adminChat, "⚠️ "+err.Error())
			} else {
				didSub = true
			}
			a.setAddSubEnabledPanel(ctx, uid, false)
		}
		a.invalidateSubCache(uid)
	}
	if srcMsgID != 0 {
		a.msg.Delete(ctx, adminChat, srcMsgID)
	}
	if eff := effMode("block", didBot, didSub); eff != "" {
		a.notifyBlockState(ctx, uid, eff)
		a.send(ctx, adminChat, i18n.T(alang, "block.done"))
	} else {
		a.send(ctx, adminChat, i18n.T(alang, "block.fail"))
	}
	a.showUser(ctx, adminChat, uid)
}

func (a *App) applyUnblock(ctx context.Context, adminChat, uid int64, mode string, srcMsgID int) {
	if uid == 0 || a.store == nil {
		return
	}
	alang := a.lang(adminChat)
	wantBot := mode == "unblockboth" || mode == "unblockbot"
	wantSub := mode == "unblockboth" || mode == "unblocksub"
	didBot, didSub := false, false
	if wantBot {
		if err := a.store.SetBlocked(ctx, uid, false); err == nil {
			didBot = true
		}
	}
	if wantSub {
		a.mu.Lock()
		panel := a.panel
		a.mu.Unlock()
		if panel != nil {
			if _, err := panel.EnableByTelegramID(ctx, uid); err != nil {
				a.notify(ctx, adminChat, "⚠️ "+err.Error())
			} else {
				didSub = true
			}
			// Mirror of applyBlock: without this B stays DISABLED forever and
			// the subscription merge silently stops for an unblocked user.
			a.setAddSubEnabledPanel(ctx, uid, true)
		}
		a.invalidateSubCache(uid)
	}
	if srcMsgID != 0 {
		a.msg.Delete(ctx, adminChat, srcMsgID)
	}
	if eff := effMode("unblock", didBot, didSub); eff != "" {
		a.notifyUnblockState(ctx, uid, eff)
		a.send(ctx, adminChat, i18n.T(alang, "unblock.done"))
	} else {
		a.send(ctx, adminChat, i18n.T(alang, "unblock.fail"))
	}
	a.showUser(ctx, adminChat, uid)
}

func effMode(prefix string, bot, sub bool) string {
	switch {
	case bot && sub:
		return prefix + "both"
	case sub:
		return prefix + "sub"
	case bot:
		return prefix + "bot"
	}
	return ""
}

func (a *App) notifyBlockState(ctx context.Context, uid int64, mode string) {
	ulang := a.lang(uid)
	if mode == "blocksub" {
		a.notify(ctx, uid, i18n.T(ulang, "block.user_sub"))
		return
	}
	key := "block.user_bot"
	if mode == "blockboth" {
		key = "block.user_both"
	}
	a.msg.SendKB(ctx, uid, a.applyPremium(i18n.T(ulang, key)), nil)
}

func (a *App) notifyUnblockState(ctx context.Context, uid int64, mode string) {
	ulang := a.lang(uid)
	key := "unblock.user_bot"
	switch mode {
	case "unblockboth":
		key = "unblock.user_both"
	case "unblocksub":
		key = "unblock.user_sub"
	}
	a.notify(ctx, uid, i18n.T(ulang, key))
}

func (a *App) adminDeleteUser(ctx context.Context, adminChat, uid int64, deleteSub bool) {
	if a.store == nil {
		return
	}
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if deleteSub && panel != nil {
		// B first: it is resolved through A's username, so deleting A first
		// would leave an orphan add-on user in the panel.
		a.removeAddSub(ctx, uid)
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
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	back := []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")}
	if total == 0 {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "payments.empty"), [][]models.InlineKeyboardButton{back})
		return
	}
	pages := (total + usersPageSize - 1) / usersPageSize

	type row struct{ date, method, user, term, amount, status string }
	rows := make([]row, 0, len(items))
	wMethod, wUser, wAmount, wTerm := len("Method"), len("User"), len("Amount"), len("Term")
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
		// Голое «Nm» с тарифами стало неинформативным: одинаковые сроки у
		// разных тарифов стоят по-разному. Тариф — из снимка сделки; сделки
		// «Базового» и старые (без снимка) остаются как были.
		if code := planCodeOf(p.Snapshot); code != "" && code != model.PlanCodeBase {
			name := p.Snapshot.Name
			if name == "" {
				name = code
			}
			// Имя тарифа — свободный текст админа, а сообщение уходит с
			// parse_mode=HTML (и внутри <pre> тоже): спецсимволы не
			// экранируем сущностями — они ломали бы моноширинное выравнивание
			// (&amp; — 5 рун вместо одной), — а просто выбрасываем.
			name = strings.Map(func(r rune) rune {
				switch r {
				case '<', '>', '&':
					return -1
				}
				return r
			}, name)
			term += "·" + truncRunes(name, 12)
		}
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
		if l := visualWidth(term); l > wTerm {
			wTerm = l
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
		padRight("User", wUser) + "  " + padRight("Term", wTerm) + "  " +
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
		sb.WriteString(padRight(r.term, wTerm))
		sb.WriteString("  ")
		sb.WriteString(padRight(r.amount, wAmount))
		sb.WriteString("  ")
		sb.WriteString(r.status)
	}
	sb.WriteString("</pre>")

	var kbRows [][]models.InlineKeyboardButton
	nav := paginationRow("pay:page:", page, pages, i18n.T(lang, "btn.prev"), i18n.T(lang, "btn.next"))
	if len(nav) > 0 {
		kbRows = append(kbRows, nav)
	}
	kbRows = append(kbRows, []models.InlineKeyboardButton{btn(i18n.T(lang, "paylog.btn"), "pay:log"), btn(i18n.T(lang, "paylog.btn_csv"), "pay:csv")})
	kbRows = append(kbRows, []models.InlineKeyboardButton{btn(i18n.T(lang, "paylog.btn_errors"), "pay:err")})
	kbRows = append(kbRows, back)
	a.sendPayKB(ctx, chatID, sb.String(), kbRows)
}

// truncRunes обрезает строку до n рун с «…»: имена тарифов в таблице платежей
// не должны раздувать колонку.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
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
	case "csv":
		a.exportPayLogCSV(ctx, chatID)
	case "err":
		a.showPayErrors(ctx, chatID, 7)
	case "errf":
		days, _ := strconv.Atoi(arg)
		a.exportPayErrors(ctx, chatID, days)
	}
}

func (a *App) showMySubs(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	home := []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.home"), "menu:home")}
	var url, expireAt, status string
	ok := false
	if panel != nil {
		url, expireAt, status, ok = panel.SubscriptionFull(ctx, chatID)
		if ok {
			url = a.rewriteSub(url)
		}
	}
	if !ok {
		rows := [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.buy"), "menu:buy")}}
		// Автопродление можно было подключить и до того, как подписка кончилась:
		// выключить его должно быть можно и с этого экрана.
		if row := a.autoPayRow(ctx, chatID, lang); row != nil {
			rows = append(rows, row)
		}
		rows = append(rows, home)
		a.sendKBSection(ctx, chatID, assets.SectionMySubscription, i18n.T(lang, "subs.none"), rows)
		return
	}
	rows := [][]models.InlineKeyboardButton{}
	if sup := a.supportURL(); sup != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(lang, "btn.support"), URL: sup}})
	}
	if status == remnawave.StatusDisabled {
		if row := a.autoPayRow(ctx, chatID, lang); row != nil {
			rows = append(rows, row)
		}
		rows = append(rows, home)
		a.sendKBSection(ctx, chatID, assets.SectionMySubscription, i18n.T(lang, "subs.blocked"), rows)
		return
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "dev.btn_reset"), "dev:reset")})
	if row := a.autoPayRow(ctx, chatID, lang); row != nil {
		rows = append(rows, row)
	}
	rows = append(rows, home)
	text := a.subActiveText(ctx, chatID, url, expireAt) + a.devicesLine(ctx, chatID, panel) + a.addSubLine(ctx, chatID)
	a.sendKBSection(ctx, chatID, assets.SectionMySubscription, text, rows)
}

// addSubLine renders the add-on subscription's state ("доп-сервер") under the
// devices line. Without it the only signal a user gets when the add-on traffic
// runs out is a stub entry inside the config. Returns "" when the feature is
// off, the user has no add-on, or the panel can't be read — the screen then
// looks exactly as before.
func (a *App) addSubLine(ctx context.Context, chatID int64) string {
	info, ok := a.addSubStatus(ctx, chatID)
	if !ok {
		return ""
	}
	lang := a.lang(chatID)
	// Название опции — тарифа пользователя (или общее): у каждого тарифа опция
	// может называться по-своему.
	name := a.userAddSubName(ctx, chatID)
	switch {
	case strings.EqualFold(info.Status, remnawave.StatusDisabled):
		return "\n" + i18n.T(lang, "sub.addsub_off", name)
	case info.Exhausted:
		return "\n" + i18n.T(lang, "sub.addsub_out", name)
	case info.Limit <= 0:
		return "\n" + i18n.T(lang, "sub.addsub_unlim", name)
	}
	left := info.Limit - info.Used
	if left < 0 {
		left = 0
	}
	return "\n" + i18n.T(lang, "sub.addsub", name, formatGB(left), formatGB(info.Limit))
}

// formatGB renders a byte count as GB with one decimal (trailing ".0" dropped).
func formatGB(b int64) string {
	if b <= 0 {
		return "0"
	}
	gb := float64(b) / (1024 * 1024 * 1024)
	s := strconv.FormatFloat(gb, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

// devicesLine renders a read-only "connected[/allowed]" devices line for the
// "My subscription" screen. When the user has no explicit device limit
// (unlimited / limit disabled) it shows ONLY the connected count. Returns ""
// when the panel is unavailable or HWID data cannot be fetched, so the screen
// degrades gracefully. View-only: it never registers or removes devices.
func (a *App) devicesLine(ctx context.Context, chatID int64, panel *remnawave.Client) string {
	if panel == nil {
		return ""
	}
	info, ok := panel.DevicesByTelegramID(ctx, chatID)
	if !ok {
		return ""
	}
	val := strconv.Itoa(info.Used)
	if info.HasLimit {
		val += " / " + strconv.Itoa(info.Limit)
	}
	return "\n\n" + i18n.T(a.lang(chatID), "sub.devices", val)
}

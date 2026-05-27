package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

const usersPageSize = 8

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
		label := "👤 " + strconv.FormatInt(u.TelegramID, 10)
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

	a.sendKB(ctx, chatID, i18n.T(lang, "users.title", total, page+1, pages), rows)
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
	a.sendKB(ctx, chatID, i18n.T(lang, "user.card", uid, created, p2p, status), [][]models.InlineKeyboardButton{
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
		key := "user.unblocked_done"
		if action == "block" {
			key = "user.blocked_done"
		}
		a.send(ctx, chatID, i18n.T(a.lang(chatID), key))
		a.showUser(ctx, chatID, uid)
	case "del":
		lang := a.lang(chatID)
		a.sendKB(ctx, chatID, i18n.T(lang, "user.card_confirm_del", arg), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.del_confirm"), "usr:delc:"+arg)},
			{btn(i18n.T(lang, "btn.back"), "usr:view:"+arg)},
		})
	case "delc":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		if a.store != nil {
			_ = a.store.DeleteUser(ctx, uid)
		}
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "user.deleted"))
		a.showUsers(ctx, chatID, 0)
	}
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
		a.send(ctx, chatID, i18n.T(lang, "admin.ask_squad"))
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
		a.send(ctx, chatID, i18n.T(lang, "squad.set_ok"))
		a.showP2PAdmin(ctx, chatID)
	}
}

package app

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Режимы публичности бота: публично / по приглашениям / белый список.
//
// «По приглашениям» — это одноразовые (или на N регистраций) ссылки вида
// t.me/<bot>?start=inv_<код>, которые генерит админ, задавая срок жизни и
// количество регистраций. Активировавший ссылку пользователь получает
// постоянный доступ (тот же флаг whitelisted, что и в белом списке), поэтому
// переключение режима туда-обратно никого не выкидывает.

// inviteCodeLen — длина кода приглашения в символах base32 (без паддинга).
const inviteCodeLen = 12

// accessMode возвращает текущий режим публичности. Только читает: конфиг
// нормализуется при загрузке и при смене режима, иначе каждое входящее
// сообщение писало бы в общий botCfg параллельно с его сохранением.
func (a *App) accessMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.AccessPublic
	}
	switch a.botCfg.AccessMode {
	case model.AccessPublic, model.AccessInvite, model.AccessWhitelist:
		return a.botCfg.AccessMode
	}
	if a.botCfg.WhitelistMode {
		return model.AccessWhitelist
	}
	return model.AccessPublic
}

// setAccessMode переключает режим публичности и сохраняет конфиг. Доступ уже
// зарегистрированным при закрытии бота сохраняется только по явному согласию
// админа (grandfather). Раньше это делалось молча — и на базе, куда только что
// залили импорт, закрытие бота открывало его сразу всей импортированной базе.
func (a *App) setAccessMode(ctx context.Context, mode string, grandfather bool) int64 {
	a.mu.Lock()
	wasPublic := true
	if a.botCfg != nil {
		wasPublic = !a.botCfg.AccessClosed()
		a.botCfg.AccessMode = mode
		// Legacy-флаг выставляем явно: NormalizeAccess при расхождении верит
		// именно ему (расхождение означает, что конфиг писала старая версия).
		a.botCfg.WhitelistMode = mode != model.AccessPublic
		a.botCfg.NormalizeAccess()
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)

	var granted int64
	if grandfather && wasPublic && mode != model.AccessPublic && a.store != nil {
		n, err := a.store.WhitelistAllUsers(ctx)
		if err != nil {
			a.log.Warn("access: выдача доступа существующим пользователям", "err", err)
		} else {
			granted = n
			a.log.Info("access: доступ сохранён существующим пользователям", "count", n, "mode", mode)
		}
	}
	return granted
}

// pendingGrandfather — сколько зарегистрированных останутся без доступа, если
// закрыть бота прямо сейчас. Это число админ и видит в вопросе при закрытии.
func (a *App) pendingGrandfather(ctx context.Context) int {
	if a.store == nil {
		return 0
	}
	_, total, err := a.store.ListUsers(ctx, 1, 0)
	if err != nil {
		return 0
	}
	granted, err := a.store.CountWhitelisted(ctx)
	if err != nil {
		return 0
	}
	if n := total - granted; n > 0 {
		return n
	}
	return 0
}

// askCloseMode спрашивает, что делать с уже зарегистрированными, прежде чем
// закрыть публичного бота.
func (a *App) askCloseMode(ctx context.Context, chatID int64, mode string) {
	lang := a.lang(chatID)
	n := a.pendingGrandfather(ctx)
	text := i18n.T(lang, "access.close_ask", i18n.T(lang, "access.mode_"+mode), n)
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "access.btn_close_keep", n), "acc:close:"+mode+":keep")},
		{btn(i18n.T(lang, "access.btn_close_all"), "acc:close:"+mode+":all")},
		{btn(i18n.T(lang, "btn.back"), "menu:access")},
	}
	a.sendKBSection(ctx, chatID, assets.SectionReferral, text, rows)
}

// accessGranted сообщает, есть ли у пользователя персональный доступ: он в
// белом списке (в т.ч. после активации приглашения) или добавлен по ID заранее.
func (a *App) accessGranted(ctx context.Context, chatID int64) bool {
	if a.store == nil {
		return true
	}
	if u, _ := a.store.GetUser(ctx, chatID); u != nil && u.Whitelisted {
		return true
	}
	ok, _ := a.store.IsWhitelistID(ctx, chatID)
	return ok
}

// MiniAccessDenied — тот же гейт, что и denyAccess, для мини-аппа и веб-кабинета
// (они ходят мимо обработчиков чата, поэтому проверку надо повторить). Админ и
// публичный режим проходят всегда; сообщений пользователю тут не шлём —
// интерфейс сам покажет отказ.
func (a *App) MiniAccessDenied(ctx context.Context, tgID int64) bool {
	if tgID == a.cfg.AdminID {
		return false
	}
	if a.accessMode() == model.AccessPublic {
		return false
	}
	if a.store == nil {
		return false
	}
	// E-mail-аккаунты кабинета (отрицательный синтетический id) вайтлистом
	// Telegram-ID не описываются: их допуском заведует модерация кабинета.
	// В закрытом боте пускаем тех, кого админ одобрил (WebApproved) — как и
	// до апгрейда; остальных заворачиваем, CabinetGate отправит их в обычную
	// очередь на одобрение.
	if tgID < 0 {
		u, _ := a.store.GetUser(ctx, tgID)
		return u == nil || !u.WebApproved
	}
	return !a.accessGranted(ctx, tgID)
}

// newInviteCode генерит непредсказуемый код приглашения.
func newInviteCode() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Практически недостижимо; лучше вернуть время, чем пустую строку.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	if len(code) > inviteCodeLen {
		code = code[:inviteCodeLen]
	}
	return strings.ToLower(code)
}

// inviteLink собирает ссылку-приглашение для кода. Пустая строка — если бот не
// смог узнать свой @username (тогда карточка показывает подсказку, а не огрызок
// ссылки, который админ по ошибке отправит клиенту).
func (a *App) inviteLink(ctx context.Context, code string) string {
	if u := a.botUsername(ctx); u != "" {
		return "https://t.me/" + u + "?start=inv_" + code
	}
	return ""
}

// redeemInvite активирует приглашение по коду из /start. Возвращает текст для
// пользователя (пустой — если делать нечего) и признак успеха.
func (a *App) redeemInvite(ctx context.Context, chatID int64, code string) (string, bool) {
	lang := a.lang(chatID)
	code = strings.ToLower(strings.TrimSpace(code))
	if a.store == nil || code == "" {
		return "", false
	}
	// Приглашения работают только в своём режиме: иначе старая ссылка тихо
	// открывала бы вход в боте, переведённом на белый список.
	if a.accessMode() != model.AccessInvite {
		return "", false
	}
	// Забаненный не должен сжигать активацию чужого приглашения.
	if a.userBlocked(ctx, chatID) {
		return "", false
	}
	if a.accessGranted(ctx, chatID) {
		// Доступ уже есть — приглашение не тратим.
		return "", true
	}
	ok, err := a.store.UseInvite(ctx, code)
	if err != nil {
		a.log.Warn("invite: активация", "code", code, "err", err)
		return i18n.T(lang, "inv.bad"), false
	}
	if !ok {
		return i18n.T(lang, "inv.bad"), false
	}
	_ = a.store.UpsertUser(ctx, chatID)
	if err := a.store.SetWhitelisted(ctx, chatID, true); err != nil {
		// Активация уже списана, но доступа нет — честно говорим об этом и
		// зовём в поддержку, а не рисуем «добро пожаловать».
		a.log.Error("invite: выдача доступа", "tg_id", chatID, "err", err)
		return i18n.T(lang, "inv.grant_failed"), false
	}
	a.log.Info("invite: доступ выдан", "tg_id", chatID, "code", code)
	alang := a.lang(a.cfg.AdminID)
	a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "inv.used_admin", code, a.userLabelByID(ctx, chatID)))
	return i18n.T(lang, "inv.ok"), true
}

// showAccess — админский экран «Доступ»: режим публичности + приглашения.
func (a *App) showAccess(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	mode := a.accessMode()
	modeName := i18n.T(lang, "access.mode_"+mode)
	active, total := a.inviteStats(ctx)
	// Два числа, а не одно: у кого доступ уже есть и сколько ID лежит
	// предзаполненными. Раньше они складывались, и экран «список вайтлиста»
	// (там только предзаполненные) противоречил этому счётчику.
	granted, ids := 0, 0
	if a.store != nil {
		if n, err := a.store.CountWhitelisted(ctx); err == nil {
			granted = n
		}
		if list, err := a.store.ListWhitelistIDs(ctx); err == nil {
			ids = len(list)
		}
	}
	text := i18n.T(lang, "access.title", modeName, i18n.T(lang, "access.hint_"+mode), active, total, granted, ids)

	pick := func(m, key string) models.InlineKeyboardButton {
		label := i18n.T(lang, key)
		if mode == m {
			label = "✅ " + label
		}
		return btn(label, "acc:mode:"+m)
	}
	rows := [][]models.InlineKeyboardButton{
		{pick(model.AccessPublic, "access.btn_public")},
		{pick(model.AccessInvite, "access.btn_invite")},
		{pick(model.AccessWhitelist, "access.btn_whitelist")},
	}
	if mode == model.AccessInvite {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "inv.btn_new"), "acc:new"),
			btn(i18n.T(lang, "inv.btn_list"), "acc:list"),
		})
	} else {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "inv.btn_list"), "acc:list")})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.wl_add_id"), "usr:wladd"),
		btn(i18n.T(lang, "btn.wl_list"), "usr:wllist"),
	})
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:users"), btn(i18n.T(lang, "btn.home"), "menu:home"),
	})
	a.sendKBSection(ctx, chatID, assets.SectionReferral, text, rows)
}

// inviteStats — сколько приглашений сейчас действует и сколько всего.
func (a *App) inviteStats(ctx context.Context) (active, total int) {
	if a.store == nil {
		return 0, 0
	}
	list, err := a.store.ListInvites(ctx)
	if err != nil {
		return 0, 0
	}
	now := time.Now().UTC()
	for i := range list {
		if list[i].Active(now) {
			active++
		}
	}
	return active, len(list)
}

// invitesPageSize — сколько приглашений показываем на одной странице.
const invitesPageSize = 10

// showInvites — список приглашений с состоянием и кнопками отзыва/удаления.
func (a *App) showInvites(ctx context.Context, chatID int64, page int) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	list, err := a.store.ListInvites(ctx)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	if page < 0 {
		page = 0
	}
	pages := (len(list) + invitesPageSize - 1) / invitesPageSize
	if page >= pages && pages > 0 {
		page = pages - 1
	}
	from := page * invitesPageSize
	to := from + invitesPageSize
	if to > len(list) {
		to = len(list)
	}
	if from > len(list) {
		from = len(list)
	}
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "inv.btn_new"), "acc:new")},
	}
	now := time.Now().UTC()
	for i := from; i < to; i++ {
		inv := list[i]
		state := i18n.T(lang, "inv.state_active")
		switch {
		case inv.Revoked:
			state = i18n.T(lang, "inv.state_revoked")
		case !inv.Active(now):
			state = i18n.T(lang, "inv.state_done")
		}
		uses := strconv.Itoa(inv.Used)
		if inv.MaxUses > 0 {
			uses += "/" + strconv.Itoa(inv.MaxUses)
		} else {
			uses += "/∞"
		}
		label := state + " " + inv.Code + " · " + uses
		if inv.ExpiresAt != "" {
			label += " · " + shortDate(inv.ExpiresAt)
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "acc:show:"+inv.Code)})
	}
	if nav := paginationRow("acc:page:", page, pages, i18n.T(lang, "btn.prev"), i18n.T(lang, "btn.next")); len(nav) > 0 {
		rows = append(rows, nav)
	}
	title := i18n.T(lang, "inv.list_title", len(list))
	if len(list) == 0 {
		title = i18n.T(lang, "inv.list_empty")
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:access"), btn(i18n.T(lang, "btn.home"), "menu:home"),
	})
	a.sendKBSection(ctx, chatID, assets.SectionReferral, title, rows)
}

// showInvite — карточка одного приглашения: ссылка + кнопки отзыва/удаления.
func (a *App) showInvite(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	inv, err := a.store.GetInvite(ctx, code)
	if err != nil || inv == nil {
		a.showInvites(ctx, chatID, 0)
		return
	}
	link := a.inviteLink(ctx, inv.Code)
	if link == "" {
		link = i18n.T(lang, "inv.no_link", inv.Code)
	}
	uses := strconv.Itoa(inv.Used)
	if inv.MaxUses > 0 {
		uses += " / " + strconv.Itoa(inv.MaxUses)
	} else {
		uses += " / ∞"
	}
	exp := i18n.T(lang, "inv.no_expiry")
	if inv.ExpiresAt != "" {
		exp = formatExpire(inv.ExpiresAt, lang)
	}
	state := i18n.T(lang, "inv.state_active")
	switch {
	case inv.Revoked:
		state = i18n.T(lang, "inv.state_revoked")
	case !inv.Active(time.Now().UTC()):
		state = i18n.T(lang, "inv.state_done")
	}
	text := i18n.T(lang, "inv.card", state, link, uses, exp)
	rows := [][]models.InlineKeyboardButton{}
	if !inv.Revoked {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "inv.btn_revoke"), "acc:revoke:"+inv.Code)})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "inv.btn_del"), "acc:del:"+inv.Code)},
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "acc:list"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	)
	a.sendKBSection(ctx, chatID, assets.SectionReferral, text, rows)
}

// onAccess обрабатывает кнопки экрана «Доступ» (только админ).
func (a *App) onAccess(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	lang := a.lang(chatID)
	switch action {
	case "show":
		a.showInvite(ctx, chatID, arg)
	case "mode":
		switch arg {
		case model.AccessInvite, model.AccessWhitelist:
			// Закрываем публичного бота — сначала спрашиваем, сохранять ли
			// доступ тем, кто уже зарегистрирован.
			if a.accessMode() == model.AccessPublic {
				a.askCloseMode(ctx, chatID, arg)
				return
			}
			a.setAccessMode(ctx, arg, false)
		case model.AccessPublic:
			a.setAccessMode(ctx, arg, false)
		}
		a.showAccess(ctx, chatID)
	case "close":
		mode, keep, _ := strings.Cut(arg, ":")
		switch mode {
		case model.AccessInvite, model.AccessWhitelist:
			if n := a.setAccessMode(ctx, mode, keep == "keep"); n > 0 {
				a.notify(ctx, chatID, i18n.T(lang, "access.grandfathered", n))
			}
		}
		a.showAccess(ctx, chatID)
	case "new":
		a.getUI(chatID).adminInput = "inv_days"
		a.askInput(ctx, chatID, i18n.T(lang, "inv.ask_days"), "menu:access")
	case "list":
		a.showInvites(ctx, chatID, 0)
	case "page":
		p, _ := strconv.Atoi(arg)
		a.showInvites(ctx, chatID, p)
	case "revoke":
		if a.store != nil && arg != "" {
			_ = a.store.RevokeInvite(ctx, arg)
		}
		a.showInvites(ctx, chatID, 0)
	case "del":
		if a.store != nil && arg != "" {
			_ = a.store.DeleteInvite(ctx, arg)
		}
		a.showInvites(ctx, chatID, 0)
	}
}

// createInviteDays — первый шаг мастера создания приглашения: срок жизни.
func (a *App) createInviteDays(ctx context.Context, chatID int64, text string) {
	lang := a.lang(chatID)
	days, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || days < 0 {
		a.getUI(chatID).adminInput = "inv_days"
		a.askInput(ctx, chatID, i18n.T(lang, "inv.ask_days"), "menu:access")
		return
	}
	ui := a.getUI(chatID)
	ui.inviteDays = days
	ui.adminInput = "inv_uses"
	a.askInput(ctx, chatID, i18n.T(lang, "inv.ask_uses"), "menu:access")
}

// createInviteUses — второй шаг: число регистраций; создаёт приглашение.
func (a *App) createInviteUses(ctx context.Context, chatID int64, text string) {
	lang := a.lang(chatID)
	uses, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || uses < 0 {
		a.getUI(chatID).adminInput = "inv_uses"
		a.askInput(ctx, chatID, i18n.T(lang, "inv.ask_uses"), "menu:access")
		return
	}
	ui := a.getUI(chatID)
	days := ui.inviteDays
	ui.inviteDays = 0
	if a.store == nil {
		return
	}
	inv := &model.Invite{Code: newInviteCode(), MaxUses: uses}
	if days > 0 {
		inv.ExpiresAt = time.Now().UTC().AddDate(0, 0, days).Format(time.RFC3339)
	}
	if err := a.store.CreateInvite(ctx, inv); err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	if a.accessMode() != model.AccessInvite {
		// Ссылка бесполезна, пока режим не «по приглашениям» — подсказываем.
		a.notify(ctx, chatID, i18n.T(lang, "inv.mode_hint"))
	}
	a.showInvite(ctx, chatID, inv.Code)
}

// shortDate печатает дату (без времени) из RFC3339 для компактных кнопок.
func shortDate(raw string) string {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Add(3 * time.Hour).Format("02.01.2006")
	}
	return raw
}

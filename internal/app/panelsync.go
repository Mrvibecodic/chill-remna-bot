package app

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
)

const (
	panelScanPageSize = 250
	panelScanMaxUsers = 10000
)

func (a *App) panelClient() *remnawave.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.panel
}

// storeRef — хранилище под тем же замком, что и панель.
func (a *App) storeRef() storage.Storage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.store
}

func (a *App) syncPanelAccount(ctx context.Context, chatID int64) bool {
	panel := a.panelClient()
	if panel == nil || a.store == nil {
		return false
	}
	if u, _ := a.store.GetUser(ctx, chatID); u != nil && (u.TrialUsedAt != "" || u.SubExpireAt != "") {
		return false
	}
	// Поиск занимается сразу и под замком: его зовут и чат, и запросы
	// мини-аппа и кабинета, и два одновременных входа запускали бы два полных
	// перебора панели.
	if !a.claimPanelSync(chatID) {
		return false
	}
	pu := a.findPanelAccount(ctx, panel, chatID)
	if pu == nil {
		// Аккаунт кабинета ищется дёшево, и итог зависит от подтверждения
		// почты: запомни мы пустой поиск, учётку не нашли бы до перезапуска,
		// а триал успел бы лечь поверх неё.
		if chatID < 0 {
			a.releasePanelSync(chatID)
		}
		return false
	}
	// Аккаунту кабинета telegramId не пишем: его id синтетический, панель
	// сохранила бы округлённое число. Учётку он находит по своей почте.
	if pu.TelegramID == 0 && chatID > 0 {
		if err := panel.LinkTelegramID(ctx, pu.Ref, chatID, false); err != nil {
			a.log.Warn("panel sync: link telegramId", "tg_id", chatID, "panel_ref", pu.Ref.Key(), "err", err)
		}
	}
	// Пока шёл перебор, человек мог взять триал или купить подписку в другой
	// поверхности — тогда найденный срок устарел и записывать его нельзя.
	lk := &a.finalizeUserLk[extLockIndex(strconv.FormatInt(chatID, 10))]
	lk.Lock()
	if u, _ := a.store.GetUser(ctx, chatID); u != nil && (u.TrialUsedAt != "" || u.SubExpireAt != "") {
		lk.Unlock()
		return false
	}
	a.markPanelLinked(ctx, chatID, pu.ExpireAt)
	lk.Unlock()
	a.log.Info("panel sync: account linked", "tg_id", chatID, "panel_user", pu.Username, "panel_ref", pu.Ref.Key())
	return true
}

func (a *App) findPanelAccount(ctx context.Context, panel *remnawave.Client, chatID int64) *remnawave.PanelUser {
	if pu, err := panel.FindByTelegramID(ctx, chatID); err == nil && pu != nil {
		return pu
	}
	// Аккаунт кабинета: его учётку находят только своё имя и почта, а их уже
	// проверил поиск выше. Перебор панели по цифрам синтетического id ловил бы
	// чужие учётки.
	if chatID < 0 {
		return nil
	}
	// tg_<id> — наша схема имён, rs_<id> — схема remnashop: после переезда
	// аккаунты в панели остаются с его именами, и находить их по прямому
	// запросу дешевле, чем сканировать весь список.
	for _, prefix := range []string{"tg_", "rs_"} {
		if pu, err := panel.FindByUsername(ctx, fmt.Sprintf("%s%d", prefix, chatID)); err == nil && pu != nil {
			return pu
		}
	}
	return a.scanPanelByID(ctx, panel, chatID)
}

func (a *App) scanPanelByID(ctx context.Context, panel *remnawave.Client, chatID int64) *remnawave.PanelUser {
	idStr := strconv.FormatInt(chatID, 10)
	var found *remnawave.PanelUser
	matches := 0
	for start := 0; start < panelScanMaxUsers; start += panelScanPageSize {
		users, total, err := panel.ListUsersPage(ctx, start, panelScanPageSize)
		if err != nil {
			a.log.Warn("panel sync: scan", "err", err)
			return nil
		}
		for i := range users {
			if usernameHasID(users[i].Username, idStr) {
				matches++
				if matches == 1 {
					u := users[i]
					found = &u
				}
			}
		}
		if len(users) == 0 || start+len(users) >= total {
			break
		}
	}
	if matches == 1 {
		return found
	}
	if matches > 1 {
		a.log.Warn("panel sync: ambiguous username match, manual link required", "tg_id", chatID, "matches", matches)
	}
	return nil
}

func usernameHasID(username, id string) bool {
	if id == "" {
		return false
	}
	idx := 0
	for {
		i := strings.Index(username[idx:], id)
		if i < 0 {
			return false
		}
		p := idx + i
		after := p + len(id)
		beforeOK := p == 0 || !isASCIIDigit(username[p-1])
		afterOK := after == len(username) || !isASCIIDigit(username[after])
		if beforeOK && afterOK {
			return true
		}
		idx = p + 1
	}
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

func (a *App) markPanelLinked(ctx context.Context, chatID int64, expireAt string) {
	if a.store == nil {
		return
	}
	_ = a.store.UpsertUser(ctx, chatID)
	// Пустая карточка бывает двух разных видов. Человек, которому бот САМ
	// вернул неиспользованный пробный период, выглядит ровно как новичок — а
	// привязка ставит «триал использован» и восстанавливает просроченный
	// срок. Возврат отменялся бы молча, на первом же заходе в бота, ради
	// которого его и делали. Живой срок в панели — другое дело: подписка у
	// человека есть, и записать её надо.
	if !panelSubAlive(expireAt) && a.trialWasReturned(ctx, chatID) {
		a.log.Info("panel sync: возвращённый триал не отбираем", "tg_id", chatID)
		return
	}
	_ = a.store.SetTrialUsed(ctx, chatID, time.Now().UTC().Format(time.RFC3339))
	if expireAt != "" {
		_ = a.store.SetSubExpiry(ctx, chatID, expireAt, "paid")
	}
	a.invalidateSubCache(chatID)
}

// trialWasReturned — возвращал ли бот этому человеку пробный период.
//
// Ошибка чтения считается «да»: соврать в эту сторону значит не привязать
// панельную учётку лишний раз, а в обратную — молча отобрать выданный возврат.
func (a *App) trialWasReturned(ctx context.Context, chatID int64) bool {
	if a.store == nil {
		return false
	}
	n, err := a.store.TrialResets(ctx, chatID)
	if err != nil {
		a.log.Warn("panel sync: счётчик возвратов триала", "tg_id", chatID, "err", err)
		return true
	}
	return n > 0
}

// panelSubAlive — срок из панели ещё не истёк.
func panelSubAlive(expireAt string) bool {
	if expireAt == "" {
		return false
	}
	exp, err := time.Parse(time.RFC3339, expireAt)
	return err == nil && exp.After(time.Now().UTC())
}

func (a *App) adminLinkPanel(ctx context.Context, adminChat, uid int64, input string) {
	lang := a.lang(adminChat)
	panel := a.panelClient()
	if panel == nil || uid == 0 {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_fail", i18n.T(lang, "squads.no_panel")))
		return
	}
	pu, ok := a.resolvePanelInput(ctx, adminChat, panel, input)
	if !ok {
		return
	}
	if uid < 0 {
		a.adminLinkPanelEmail(ctx, adminChat, uid, panel, pu)
		return
	}
	if pu.TelegramID != 0 && pu.TelegramID != uid {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_busy", escapeName(pu.Username), pu.TelegramID))
		return
	}
	if owner := a.emailOwner(ctx, pu.Email); owner != 0 && owner != uid {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_busy_owner", escapeName(pu.Username), a.userLabelByID(ctx, owner)))
		return
	}
	if err := panel.LinkTelegramID(ctx, pu.Ref, uid, true); err != nil {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_fail", html.EscapeString(err.Error())))
		return
	}
	a.markPanelLinked(ctx, uid, pu.ExpireAt)
	a.log.Info("panel sync: manual link", "tg_id", uid, "panel_user", pu.Username, "panel_ref", pu.Ref.Key())
	a.notify(ctx, uid, i18n.T(a.lang(uid), "sync.linked", formatExpire(pu.ExpireAt, a.lang(uid))))
	a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_done", escapeName(pu.Username), formatExpire(pu.ExpireAt, lang)))
}

// adminLinkPanelEmail привязывает учётку к аккаунту кабинета без Telegram: в
// учётку пишется его почта, по ней он свою подписку и находит.
func (a *App) adminLinkPanelEmail(ctx context.Context, adminChat, uid int64, panel *remnawave.Client, pu *remnawave.PanelUser) {
	lang := a.lang(adminChat)
	fail := func(reason string) {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_fail", reason))
	}
	if a.store == nil {
		fail(i18n.T(lang, "user.link_no_email"))
		return
	}
	wu, err := a.store.GetWebUserByTgID(ctx, uid)
	if err != nil || wu == nil || model.NormalizeEmail(wu.Email) == "" {
		fail(i18n.T(lang, "user.link_no_email"))
		return
	}
	email := model.NormalizeEmail(wu.Email)
	// Живой Telegram ID в учётке — это чужой человек. Отрицательное число там
	// бывает только мусором от прежней привязки: живых таких id нет.
	if pu.TelegramID > 0 {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_busy", escapeName(pu.Username), pu.TelegramID))
		return
	}
	if pu.Email != "" && !strings.EqualFold(strings.TrimSpace(pu.Email), email) {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_busy_email", escapeName(pu.Username), escapeName(pu.Email)))
		return
	}
	// Своя учётка у аккаунта уже есть — новая оказалась бы в её тени.
	mine, err := panel.FindByTelegramID(ctx, uid)
	if err != nil {
		fail(html.EscapeString(err.Error()))
		return
	}
	if mine != nil && mine.Ref.Key() != pu.Ref.Key() {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_has_own", escapeName(mine.Username)))
		return
	}
	others, err := panel.UsersByEmail(ctx, email)
	if err != nil {
		fail(html.EscapeString(err.Error()))
		return
	}
	for _, o := range others {
		if o.Ref.Key() != pu.Ref.Key() && o.Tag != remnawave.BotTagAdd {
			a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_email_taken", escapeName(o.Username)))
			return
		}
	}
	if err := panel.LinkEmail(ctx, pu.Ref, email, pu.TelegramID < 0); err != nil {
		fail(html.EscapeString(err.Error()))
		return
	}
	// Админ поручился за человека: без подтверждённой почты учётку не найдёт
	// поиск (см. panelEmailFor).
	if wu.VerifiedAt == "" {
		if err := a.store.SetWebUserVerified(ctx, uid, ""); err != nil {
			a.log.Warn("panel sync: почта не отмечена подтверждённой", "tg_id", uid, "err", err)
			fail(i18n.T(lang, "user.link_verify_fail"))
			return
		}
	}
	a.markPanelLinked(ctx, uid, pu.ExpireAt)
	a.log.Info("panel sync: manual link by email", "tg_id", uid, "panel_user", pu.Username, "panel_ref", pu.Ref.Key())
	a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_done", escapeName(pu.Username), formatExpire(pu.ExpireAt, lang)))
}

// emailOwner — чей аккаунт кабинета находит учётку по этой почте (0 — ничей).
// Неподтверждённая почта учётку не находит (см. panelEmailFor) и не мешает.
func (a *App) emailOwner(ctx context.Context, email string) int64 {
	email = model.NormalizeEmail(email)
	if email == "" || a.store == nil {
		return 0
	}
	if wu, _ := a.store.GetWebUserByEmail(ctx, email); wu != nil && wu.VerifiedAt != "" {
		return wu.TgID
	}
	return 0
}

// resolvePanelInput находит учётку по тому, что ввёл админ. false — ответ
// админу уже отправлен.
func (a *App) resolvePanelInput(ctx context.Context, adminChat int64, panel *remnawave.Client, input string) (*remnawave.PanelUser, bool) {
	lang := a.lang(adminChat)
	input = strings.TrimSpace(input)
	if input == "" {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_not_found"))
		return nil, false
	}
	var pu *remnawave.PanelUser
	var err error
	// Panel 3.0.0 replaced the user uuid with a numeric id, so the admin may
	// paste a username, a uuid or an id. Digits are ambiguous — a username of
	// digits is legal too — so both are resolved and a genuine clash is
	// reported instead of silently linking the wrong account.
	switch {
	case looksLikeUUID(input):
		pu, err = panel.FindByRef(ctx, input)
	case looksLikeUserID(input):
		byID, idErr := panel.FindByRef(ctx, input)
		byName, nameErr := panel.FindByUsername(ctx, input)
		switch {
		case byID != nil && byName != nil && byID.Username != byName.Username:
			a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_ambiguous", byID.Username, byName.Username))
			return nil, false
		case byID != nil:
			pu = byID
		case byName != nil:
			pu = byName
		default:
			err = firstErr(idErr, nameErr)
		}
	default:
		pu, err = panel.FindByUsername(ctx, input)
	}
	if err != nil {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_fail", html.EscapeString(err.Error())))
		return nil, false
	}
	if pu == nil {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_not_found"))
		return nil, false
	}
	return pu, true
}

// adminUnlinkTarget — учётка, которую можно отвязать от пользователя, или
// причина отказа (ключ текста).
func (a *App) adminUnlinkTarget(ctx context.Context, panel *remnawave.Client, uid int64) (*remnawave.PanelUser, string, error) {
	pu, err := panel.FindByTelegramID(ctx, uid)
	if err != nil {
		return nil, "", err
	}
	if pu == nil {
		return nil, "user.unlink_none", nil
	}
	if remnawave.IsBotUsername(pu.Username, uid) {
		return nil, "user.unlink_own", nil
	}
	return pu, "", nil
}

// adminUnlinkPanel отвязывает учётку панели от пользователя бота. В панели
// снимается только то, по чему бот её находит; учётка, срок и трафик остаются.
// В боте стирается срок подписки.
func (a *App) adminUnlinkPanel(ctx context.Context, adminChat, uid int64) {
	lang := a.lang(adminChat)
	panel := a.panelClient()
	if panel == nil || uid == 0 {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.unlink_fail", i18n.T(lang, "squads.no_panel")))
		return
	}
	pu, reason, err := a.adminUnlinkTarget(ctx, panel, uid)
	if err != nil {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.unlink_fail", html.EscapeString(err.Error())))
		return
	}
	if reason != "" {
		a.sendHome(ctx, adminChat, i18n.T(lang, reason))
		return
	}
	// Подтверждали другую учётку (или кнопку нажали из старого сообщения) —
	// показываем, что отвяжется сейчас, и просим подтвердить заново.
	ui := a.getUI(adminChat)
	if ui.unlinkUID != uid || ui.unlinkRef != pu.Ref.Key() {
		a.askUnlinkPanel(ctx, adminChat, uid)
		return
	}
	ui.unlinkUID, ui.unlinkRef = 0, ""
	// Telegram ID снимается у Telegram-пользователя и мусорный — у аккаунта
	// кабинета; почта — если это почта самого пользователя: после привязки
	// Telegram к аккаунту кабинета в учётке остаются обе.
	clearTG := uid > 0 || pu.TelegramID < 0
	clearEmail := pu.Email != "" && strings.EqualFold(strings.TrimSpace(pu.Email), a.userEmail(ctx, uid))
	if err := panel.UnlinkUser(ctx, pu.Ref, clearTG, clearEmail, pu.Tag == remnawave.BotTag); err != nil {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.unlink_fail", html.EscapeString(err.Error())))
		return
	}
	a.claimPanelSync(uid)
	if a.store != nil {
		// Автопривязка снова нашла бы учётку только по цифрам id в её имени —
		// только тогда и нужна отметка о триале. Остальным её не ставим: у
		// кого-то она снята возвратом триала.
		if uid > 0 && usernameHasID(pu.Username, strconv.FormatInt(uid, 10)) {
			if u, _ := a.store.GetUser(ctx, uid); u != nil && u.TrialUsedAt == "" {
				_ = a.store.SetTrialUsed(ctx, uid, time.Now().UTC().Format(time.RFC3339))
			}
		}
		if err := a.store.SetSubExpiry(ctx, uid, "", ""); err != nil {
			a.log.Warn("panel sync: срок после отвязки не стёрт", "tg_id", uid, "err", err)
		}
	}
	a.invalidateSubCache(uid)
	a.log.Info("panel sync: manual unlink", "tg_id", uid, "panel_user", pu.Username, "panel_ref", pu.Ref.Key())
	a.sendHome(ctx, adminChat, i18n.T(lang, "user.unlink_done", escapeName(pu.Username)))
}

// firstErr returns the first non-nil error, so a lookup that failed on both
// paths still reports something to the admin.
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// looksLikeUserID matches the numeric user id used by panel 3.0.0+.
func looksLikeUserID(s string) bool {
	if s == "" || len(s) > 19 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// claimPanelSync занимает поиск аккаунта в панели для пользователя: true —
// поиск ещё не делался в этом процессе и теперь за вызывающим.
func (a *App) claimPanelSync(chatID int64) bool {
	ui := a.getUI(chatID)
	a.mu.Lock()
	defer a.mu.Unlock()
	if ui.panelSyncDone {
		return false
	}
	ui.panelSyncDone = true
	return true
}

func (a *App) releasePanelSync(chatID int64) {
	ui := a.getUI(chatID)
	a.mu.Lock()
	ui.panelSyncDone = false
	a.mu.Unlock()
}

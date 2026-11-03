package app

import (
	"context"
	"errors"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// Защита после восстановления базы из бэкапа.
//
// Восстановленная база не знает об оплатах после даты файла, а панель знает:
// там сроки уже продлены. Без защиты автопродление по старому сроку списало
// бы деньги второй раз, а сверки счетов и звёзд выдали бы те же дни снова.
// Поэтому после восстановления:
//   - сроки подписок сначала сверяются с панелью, и до этого стоят
//     автопродление, напоминания, сверка лимитов и прочие проходы по срокам;
//   - автопродление ждёт, пока админ не включит его сам: согласие на списания
//     восстанавливается из файла, а человек мог отозвать его позже;
//   - первое списание каждого человека после восстановления идёт только
//     после того, как его срок подтвердила панель;
//   - счета и оплаты звёздами, созданные до восстановления и найденные
//     сверкой оплаченными, бот сам не выдаёт — они уходят админу списком;
//   - заявки на перевод из файла приходят админу списком для проверки.

const (
	// guardSyncRetry — пауза между попытками сверить сроки с панелью.
	guardSyncRetry = 5 * time.Minute
	// guardScanMax — потолок перебора пользователей панели.
	guardScanMax = 1_000_000
	// reviewChunk — строк в одном сообщении со списком на проверку.
	reviewChunk = 30
	// stageReview — отметка в журнале платежей: оплата ушла админу.
	stageReview = "restore_review"
)

var errNoPanel = errors.New("панель не подключена")

// reviewItem — строка списка на проверку и что сделать, когда список
// доставлен (закрыть счёт, поставить отметку). До доставки ничего не
// закрывается: потерянное сообщение не должно хоронить оплату.
type reviewItem struct {
	line   string
	onSent func(ctx context.Context)
}

// reviewNoted — эта оплата уже ушла админу на проверку (отметка в журнале
// платежей переживает перезапуск).
func (a *App) reviewNoted(ctx context.Context, extID string) bool {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return true
	}
	// Только по ext_id: выборка по человеку отдала бы его старые записи, и
	// отметка могла бы в неё не попасть.
	logs, err := st.PayLogs(ctx, extID, 0, 0)
	if err != nil {
		// Не знаем — не дёргаем админа повторно каждые две минуты.
		return true
	}
	for _, e := range logs {
		if e.ExtID == extID && e.Stage == stageReview {
			return true
		}
	}
	return false
}

// restoreGuard — копия метки восстановления; nil — не восстанавливали.
func (a *App) restoreGuard() *model.RestoreGuard {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bkState.Guard == nil {
		return nil
	}
	g := *a.bkState.Guard
	g.Changed = slices.Clone(g.Changed)
	return &g
}

// updateGuard правит метку восстановления и сохраняет её.
func (a *App) updateGuard(ctx context.Context, f func(g *model.RestoreGuard)) {
	err := a.editBackupState(ctx, func(bc *model.BackupConfig) {
		if bc.Guard == nil {
			return
		}
		g := *bc.Guard
		f(&g)
		bc.Guard = &g
	})
	if err != nil {
		a.log.Warn("метка восстановления не сохранена", "err", err)
	}
}

// restoreSyncPending — база восстановлена, а сроки ещё не сверены с панелью.
func (a *App) restoreSyncPending() bool {
	g := a.restoreGuard()
	return g != nil && !g.Synced
}

// autoPayHeld — автопродление ждёт, пока админ включит его после
// восстановления базы.
func (a *App) autoPayHeld() bool {
	g := a.restoreGuard()
	return g != nil && g.AutoPayHeld
}

// restoredBefore — момент раньше восстановления базы.
func (a *App) restoredBefore(t time.Time) bool {
	at, ok := a.restoreGuard().RestoreAt()
	return ok && t.Before(at)
}

// restoreChanged — срок этого человека в панели разошёлся с файлом.
func (a *App) restoreChanged(id int64) bool {
	g := a.restoreGuard()
	return g != nil && slices.Contains(g.Changed, id)
}

// noticeRestored пишет админу после перезапуска, что база на месте.
func (a *App) noticeRestored(ctx context.Context) {
	g := a.restoreGuard()
	if g == nil || g.Notified || g.Chat == 0 {
		return
	}
	lang := a.lang(g.Chat)
	text := i18n.T(lang, "bk.back_online", html.EscapeString(fmtBackupTime(g.BackupAt)))
	var rows [][]models.InlineKeyboardButton
	if g.AutoPayHeld {
		text += "\n\n" + i18n.T(lang, "bk.ap_held")
		rows = [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "bk.btn_ap_on"), "bk:apon")}}
	}
	if a.msg.SendKB(ctx, g.Chat, text, rows) == 0 {
		return
	}
	a.updateGuard(ctx, func(g *model.RestoreGuard) { g.Notified = true })
}

// guardTick — сверка сроков с панелью, пока она не прошла.
func (a *App) guardTick(ctx context.Context, now time.Time) {
	if !a.restoreSyncPending() || !a.guardMu.TryLock() {
		return
	}
	defer a.guardMu.Unlock()
	if now.Before(a.guardNextTry) || !a.enterWork() {
		return
	}
	defer a.leaveWork()
	changed, err := a.syncExpiryFromPanel(ctx)
	lang := a.lang(a.cfg.AdminID)
	if err != nil {
		a.guardNextTry = now.Add(guardSyncRetry)
		a.log.Warn("сверка сроков после восстановления не прошла", "err", err)
		if !a.guardFailNoted {
			a.guardFailNoted = true
			a.notify(ctx, a.cfg.AdminID, i18n.T(lang, "bk.sync_fail", a.errText(err)))
		}
		return
	}
	a.updateGuard(ctx, func(g *model.RestoreGuard) {
		g.Synced = true
		g.Changed = changed
	})
	a.log.Info("сроки после восстановления сверены с панелью", "changed", len(changed))
	a.notify(ctx, a.cfg.AdminID, i18n.T(lang, "bk.synced", len(changed)))
	a.reviewRestoredP2P(ctx)
}

// panelUserID — чей это аккаунт в панели. У аккаунтов кабинета по почте
// telegramId в панели пустой, их узнаём по имени tg_<id> (id отрицательный).
func panelUserID(u *remnawave.PanelUser) int64 {
	if u.TelegramID != 0 {
		return u.TelegramID
	}
	if rest, ok := strings.CutPrefix(u.Username, "tg_"); ok {
		if id, err := strconv.ParseInt(rest, 10, 64); err == nil {
			return id
		}
	}
	return 0
}

// syncExpiryFromPanel переносит в базу бота сроки подписок из панели и
// возвращает тех, у кого срок разошёлся.
func (a *App) syncExpiryFromPanel(ctx context.Context) ([]int64, error) {
	panel := a.panelClient()
	a.mu.Lock()
	st := a.store
	suffix := ""
	if a.botCfg != nil {
		suffix = a.botCfg.AddSub.UsernameSuffix
	}
	a.mu.Unlock()
	if panel == nil || st == nil {
		return nil, errNoPanel
	}
	expire := map[int64]string{}
	dup := map[int64]bool{}
	for start := 0; start < guardScanMax; start += panelScanPageSize {
		users, total, err := panel.ListUsersPage(ctx, start, panelScanPageSize)
		if err != nil {
			return nil, err
		}
		for i := range users {
			u := &users[i]
			// Доп-подписка живёт под тем же telegram_id — это не основной срок.
			if u.ExpireAt == "" || u.Tag == remnawave.BotTagAdd ||
				(suffix != "" && strings.HasSuffix(u.Username, suffix)) {
				continue
			}
			id := panelUserID(u)
			if id == 0 {
				continue
			}
			if _, seen := expire[id]; seen {
				dup[id] = true
			}
			expire[id] = u.ExpireAt
		}
		if len(users) == 0 || start+len(users) >= total {
			break
		}
	}
	var changed []int64
	for id, exp := range expire {
		if dup[id] {
			continue
		}
		u, err := st.GetUser(ctx, id)
		if err != nil {
			return nil, err
		}
		if u == nil || sameMoment(u.SubExpireAt, exp) {
			continue
		}
		// Срок в панели позже файла — была покупка после даты бэкапа.
		// Иначе (срок урезали в панели) вид подписки оставляем как был.
		kind := u.NotifyKind
		if later(exp, u.SubExpireAt) || kind == "" {
			kind = "paid"
		}
		if err := st.SetSubExpiry(ctx, id, exp, kind); err != nil {
			return nil, err
		}
		changed = append(changed, id)
	}
	slices.Sort(changed)
	return changed, nil
}

// later — момент a позже момента b (непонятная запись — не позже).
func later(a, b string) bool {
	ta, ea := time.Parse(time.RFC3339, a)
	tb, eb := time.Parse(time.RFC3339, b)
	return ea == nil && (eb != nil || ta.After(tb))
}

// sameMoment — две записи одного момента времени (формат панели и бота
// может отличаться долями секунды).
func sameMoment(a, b string) bool {
	ta, ea := time.Parse(time.RFC3339, a)
	tb, eb := time.Parse(time.RFC3339, b)
	if ea != nil || eb != nil {
		return a == b
	}
	return ta.Equal(tb)
}

// autoPayExpiryConfirmed — можно ли списывать по этому сроку. Пока человек
// не платил после восстановления базы, срок перед списанием сверяется с
// панелью: сверка всех сроков не видит дублей и тех, кого в панели не
// нашла, а списание по сроку из файла — это повторное списание.
func (a *App) autoPayExpiryConfirmed(ctx context.Context, ap *model.AutoPay, exp time.Time, now time.Time) bool {
	at, ok := a.restoreGuard().RestoreAt()
	if !ok {
		return true
	}
	if last, err := time.Parse(time.RFC3339, ap.LastPayAt); err == nil && !last.Before(at) {
		return true
	}
	panel := a.panelClient()
	var pu *remnawave.PanelUser
	if panel != nil {
		pu = a.findPanelAccount(ctx, panel, ap.TelegramID)
	}
	mirror := exp.UTC().Format(time.RFC3339)
	if pu == nil || pu.ExpireAt == "" {
		a.autoPayDefer(ctx, ap, now, autoPayRetryDelay, "после восстановления базы срок не подтверждён панелью")
		return false
	}
	if !sameMoment(pu.ExpireAt, mirror) {
		// Срок разошёлся: обновляем и решаем заново на следующем проходе.
		if a.store != nil {
			_ = a.store.SetSubExpiry(ctx, ap.TelegramID, pu.ExpireAt, "paid")
		}
		return false
	}
	return true
}

// addReview кладёт оплату в список на ручную проверку.
func (a *App) addReview(method, purpose string, months int, amount string, userID int64, extID string, onSent func(context.Context)) {
	lang := a.lang(a.cfg.AdminID)
	what := i18n.T(lang, "bk.review_months", months)
	if purpose == model.P2PPurposeTopUp {
		what = i18n.T(lang, "bk.review_topup")
	}
	line := i18n.T(lang, "bk.review_line", html.EscapeString(methodLabel(method)), what,
		html.EscapeString(amount), userID, html.EscapeString(extID))
	a.reviewMu.Lock()
	a.reviewItems = append(a.reviewItems, reviewItem{line: line, onSent: onSent})
	a.reviewMu.Unlock()
}

// flushReview отправляет админу накопленный список на проверку. Что не
// доставлено — не закрывается и найдётся сверкой снова.
func (a *App) flushReview(ctx context.Context) {
	a.reviewMu.Lock()
	items := a.reviewItems
	a.reviewItems = nil
	a.reviewMu.Unlock()
	if len(items) == 0 {
		return
	}
	lang := a.lang(a.cfg.AdminID)
	for len(items) > 0 {
		n := min(len(items), reviewChunk)
		lines := make([]string, n)
		for i := range n {
			lines[i] = items[i].line
		}
		if a.notifyKB(ctx, a.cfg.AdminID, i18n.T(lang, "bk.review", strings.Join(lines, "\n")), nil) != 0 {
			for i := range n {
				if items[i].onSent != nil {
					items[i].onSent(ctx)
				}
			}
		}
		items = items[n:]
	}
}

// reviewRestoredP2P присылает админу заявки на перевод из файла: их могли
// одобрить и выдать уже после даты бэкапа, а в файле они снова ждут решения.
func (a *App) reviewRestoredP2P(ctx context.Context) {
	at, ok := a.restoreGuard().RestoreAt()
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if !ok || st == nil {
		return
	}
	var lines []string
	for _, status := range []string{model.P2PAwaiting, model.P2PSubmitted} {
		reqs, err := st.ListP2PRequestsByStatus(ctx, status, 500)
		if err != nil {
			a.log.Warn("заявки на перевод после восстановления", "err", err)
			return
		}
		for _, r := range reqs {
			if t, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil && !t.Before(at) {
				continue
			}
			lines = append(lines, fmt.Sprintf("• #%d · <code>%d</code> · %s", r.ID, r.TelegramID, html.EscapeString(r.Price)))
		}
	}
	if len(lines) == 0 {
		return
	}
	lang := a.lang(a.cfg.AdminID)
	for len(lines) > 0 {
		n := min(len(lines), reviewChunk)
		a.notify(ctx, a.cfg.AdminID, i18n.T(lang, "bk.p2p_review", strings.Join(lines[:n], "\n")))
		lines = lines[n:]
	}
}

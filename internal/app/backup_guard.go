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
	"remnabot/internal/storage"
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
//   - оплаты по счетам, выставленным до восстановления (сверка, старые
//     кнопки «Проверить оплату», повторные вебхуки), и оплаты звёздами между
//     датой файла и восстановлением бот сам не выдаёт — они уходят админу
//     списком;
//   - заявки на перевод из файла приходят админу списком для проверки;
//   - тем, чей срок в панели разошёлся с файлом, сверка лимитов не
//     откатывает условия, пока они не купят снова.

const (
	// guardSyncRetry — пауза между попытками сверить сроки с панелью.
	guardSyncRetry = 5 * time.Minute
	// guardScanMax — потолок перебора пользователей панели.
	guardScanMax = 1_000_000
	// reviewChunk — строк в одном сообщении со списком на проверку.
	reviewChunk = 30
	// stageReview — отметка в журнале платежей: оплата ушла админу.
	stageReview = "restore_review"
	// stageHeld — оплату по старому счёту не выдали: ждёт решения админа.
	stageHeld = "restore_held"
	// reviewQuietAfter — после этого срока оплаты до восстановления админу
	// снова не приходят. Журнал, где лежит отметка «уже на проверке», живёт
	// 90 дней, а за два месяца список давно разобран.
	reviewQuietAfter = 60 * 24 * time.Hour
	// starsWindowSlack — запас до даты файла в окне звёзд: оплату за минуту
	// до бэкапа сверка могла выдать уже после него.
	starsWindowSlack = time.Hour
)

var errNoPanel = errors.New("панель не подключена")

// reviewItem — строка списка на проверку и что сделать, когда список
// доставлен (закрыть счёт, поставить отметку). До доставки ничего не
// закрывается: потерянное сообщение не должно хоронить оплату.
type reviewItem struct {
	extID  string
	line   string
	onSent func(ctx context.Context)
}

// restoreHeld — оплата по счёту до восстановления базы ушла админу на
// проверку. Для вызывающих это «уже обработано» (вебхук отвечает 200 и не
// повторяется), для человека — готовый текст.
type restoreHeld struct{ msg string }

func (e restoreHeld) Error() string { return e.msg }
func (e restoreHeld) Unwrap() []error {
	return []error{userText(e), storage.ErrDuplicateExtID}
}

// invoiceMethod — способ, где бот сам выставляет счёт и записывает его. У
// таких оплат по строке счёта видно, выставлен ли он до восстановления.
func invoiceMethod(method string) bool {
	switch method {
	case model.PayMethodYooKassa, model.PayMethodCryptoBot, model.PayMethodHeleket,
		model.PayMethodPlatega, model.PayMethodYooMoney:
		return true
	}
	return false
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
	if at, ok := a.restoreGuard().RestoreAt(); ok && time.Since(at) > reviewQuietAfter {
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

// restoreKeep — кому сверка лимитов не откатывает условия: срок в панели
// разошёлся с файлом (была покупка после даты бэкапа, которой в базе нет), а
// новой покупки после восстановления ещё не было. Считается раз на проход.
func (a *App) restoreKeep(ctx context.Context, st storage.Storage) map[int64]bool {
	g := a.restoreGuard()
	at, ok := g.RestoreAt()
	if !ok || len(g.Changed) == 0 {
		return nil
	}
	keep := make(map[int64]bool, len(g.Changed))
	for _, id := range g.Changed {
		if !a.paidSince(ctx, st, id, at) {
			keep[id] = true
		}
	}
	return keep
}

// paidSince — у человека есть покупка подписки не раньше since. Не смогли
// прочитать — считаем, что нет: так сверка ничего не откатит.
func (a *App) paidSince(ctx context.Context, st storage.Storage, id int64, since time.Time) bool {
	p, err := st.LastPaidSubPayment(ctx, id)
	if err != nil || p == nil {
		return false
	}
	t, ok := parseStamp(p.CreatedAt)
	return ok && !t.Before(since)
}

// parseStamp разбирает время записи базы (RFC3339 или вид SQLite).
func parseStamp(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
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

// guardTick — сверка сроков с панелью, пока она не прошла, и список
// заявок на перевод из файла, пока он не дошёл до админа.
func (a *App) guardTick(ctx context.Context, now time.Time) {
	g := a.restoreGuard()
	if g == nil || (g.Synced && g.P2PNoted) || !a.guardMu.TryLock() {
		return
	}
	defer a.guardMu.Unlock()
	if now.Before(a.guardNextTry) || !a.enterWork() {
		return
	}
	defer a.leaveWork()
	lang := a.lang(a.cfg.AdminID)
	if !g.Synced {
		changed, err := a.syncExpiryFromPanel(ctx)
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
	}
	if a.reviewRestoredP2P(ctx) {
		a.updateGuard(ctx, func(g *model.RestoreGuard) { g.P2PNoted = true })
		return
	}
	a.guardNextTry = now.Add(guardSyncRetry)
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
	// Покупка посреди сверки уже записала новый срок, а панель прочитана
	// раньше: такой срок не трогаем. Время платежа в базе — до секунды.
	started := time.Now().Truncate(time.Second)
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
		if u == nil || sameMoment(u.SubExpireAt, exp) || a.paidSince(ctx, st, id, started) {
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

// addReview кладёт оплату в список на ручную проверку. Оплата, которая уже
// ждёт отправки, второй раз не добавляется.
func (a *App) addReview(method, purpose string, months int, amount string, userID int64, extID string, onSent func(context.Context)) {
	lang := a.lang(a.cfg.AdminID)
	what := i18n.T(lang, "bk.review_months", months)
	if purpose == model.P2PPurposeTopUp {
		what = i18n.T(lang, "bk.review_topup")
	}
	line := i18n.T(lang, "bk.review_line", html.EscapeString(methodLabel(method)), what,
		html.EscapeString(amount), userID, html.EscapeString(extID))
	a.reviewMu.Lock()
	defer a.reviewMu.Unlock()
	for _, it := range a.reviewItems {
		if it.extID == extID {
			return
		}
	}
	a.reviewItems = append(a.reviewItems, reviewItem{extID: extID, line: line, onSent: onSent})
}

// flushReview отправляет админу накопленный список на проверку. Что не
// доставлено — остаётся в очереди до следующего раза.
func (a *App) flushReview(ctx context.Context) {
	a.reviewMu.Lock()
	items := a.reviewItems
	a.reviewItems = nil
	a.reviewMu.Unlock()
	if len(items) == 0 {
		return
	}
	lang := a.lang(a.cfg.AdminID)
	var left []reviewItem
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
		} else {
			left = append(left, items[:n]...)
		}
		items = items[n:]
	}
	if len(left) == 0 {
		return
	}
	a.reviewMu.Lock()
	a.reviewItems = append(left, a.reviewItems...)
	a.reviewMu.Unlock()
}

// holdRestoredPayment не даёт выдать оплату по счёту, выставленному до
// восстановления базы: его могли оплатить и выдать уже после даты файла, а
// база этого не помнит. Так ведут себя старые кнопки «Проверить оплату» и
// повторные вебхуки. Оплата уходит админу; true — выдавать нельзя.
func (a *App) holdRestoredPayment(ctx context.Context, method, purpose string, months int, amount string, userID int64, extID string) bool {
	at, ok := a.restoreGuard().RestoreAt()
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if !ok || st == nil || extID == "" || !invoiceMethod(method) {
		return false
	}
	// Уже выданная оплата — обычный дубль, его гасит барьер ниже.
	if done, _ := st.PaymentByExtID(ctx, extID); done {
		return false
	}
	// Счёт после восстановления записан всегда. Нет строки — значит, счёт
	// выставлен после даты файла и до восстановления.
	p, _ := st.PendingByExtID(ctx, extID)
	if p != nil {
		if t, ok := parseStamp(p.CreatedAt); !ok || !t.Before(at) {
			return false
		}
	}
	a.payLog(ctx, method, extID, userID, stageHeld, "счёт до восстановления базы: %s — не выдано, решает админ", amount)
	if !a.reviewNoted(ctx, extID) {
		var pid int64
		if p != nil {
			pid = p.ID
		}
		a.addReview(method, purpose, months, amount, userID, extID, func(c context.Context) {
			a.payLog(c, method, extID, userID, stageReview,
				"оплата по счёту до восстановления базы: %s — на ручную проверку", amount)
			if pid != 0 {
				_ = st.ResolvePending(c, pid)
			}
		})
		a.flushReview(ctx)
	}
	return true
}

// restoreHeldErr — ответ человеку на оплату, которую решает админ.
func (a *App) restoreHeldErr(userID int64) error {
	return restoreHeld{msg: i18n.T(a.lang(userID), "bk.pay_held")}
}

// reviewRestoredP2P присылает админу заявки на перевод из файла: их могли
// одобрить и выдать уже после даты бэкапа, а в файле они снова ждут решения.
// true — список дошёл (или посылать нечего).
func (a *App) reviewRestoredP2P(ctx context.Context) bool {
	at, ok := a.restoreGuard().RestoreAt()
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if !ok || st == nil {
		return true
	}
	var lines []string
	for _, status := range []string{model.P2PAwaiting, model.P2PSubmitted} {
		reqs, err := st.ListP2PRequestsByStatus(ctx, status, 500)
		if err != nil {
			a.log.Warn("заявки на перевод после восстановления", "err", err)
			return false
		}
		for _, r := range reqs {
			if t, ok := parseStamp(r.CreatedAt); ok && !t.Before(at) {
				continue
			}
			lines = append(lines, fmt.Sprintf("• #%d · <code>%d</code> · %s", r.ID, r.TelegramID, html.EscapeString(r.Price)))
		}
	}
	lang := a.lang(a.cfg.AdminID)
	for len(lines) > 0 {
		n := min(len(lines), reviewChunk)
		if a.notifyKB(ctx, a.cfg.AdminID, i18n.T(lang, "bk.p2p_review", strings.Join(lines[:n], "\n")), nil) == 0 {
			return false
		}
		lines = lines[n:]
	}
	return true
}

// restoredStarsWindow — оплата звёздами могла быть выдана между датой файла
// и восстановлением, и база этого не помнит. Раньше даты файла каждая
// выданная оплата в файле есть.
func (a *App) restoredStarsWindow(t time.Time) bool {
	g := a.restoreGuard()
	at, ok := g.RestoreAt()
	if !ok || !t.Before(at) {
		return false
	}
	from, err := time.Parse(time.RFC3339, g.BackupAt)
	return err != nil || !t.Before(from.Add(-starsWindowSlack))
}

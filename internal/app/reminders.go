package app

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

const remindTick = 30 * time.Minute

func (a *App) RunReminders(ctx context.Context) {
	t := time.NewTicker(remindTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.remindOnce(ctx)
		}
	}
}

func (a *App) remindOnce(ctx context.Context) {
	a.mu.Lock()
	st := a.store
	var rc model.RemindersConfig
	if a.botCfg != nil {
		rc = a.botCfg.Reminders
		// Список дней перебирается ниже без замка, а админка правит его на
		// месте (append по тому же массиву) — здесь нужна своя копия.
		rc.DaysList = append([]int(nil), rc.DaysList...)
	}
	a.mu.Unlock()
	if st == nil {
		return
	}
	users, err := st.UsersForNotify(ctx)
	if err != nil {
		a.log.Warn("reminders: list", "err", err)
		return
	}
	now := time.Now().UTC()
	for i := range users {
		a.remindUser(ctx, st, rc, &users[i], now)
	}
}

func (a *App) remindUser(ctx context.Context, st interface {
	MarkNotified(context.Context, int64, string) error
}, rc model.RemindersConfig, u *model.User, now time.Time) {
	// Заблокированному напоминание о конце подписки — спам: бот ему уже
	// отказал во всём остальном.
	if u.Blocked {
		return
	}
	// Аккаунт кабинета по почте живёт с нулевым (или отрицательным
	// служебным) telegram_id — чата с ним нет, отправлять некуда.
	if u.TelegramID <= 0 {
		return
	}
	localExp, err := time.Parse(time.RFC3339, u.SubExpireAt)
	if err != nil || !localExp.After(now) {
		return
	}
	sent := parseCSVInts(u.NotifySent)

	// Дешёвый отбор по базе бота: панель дёргаем только для тех, кому по
	// локальным данным уже пора. Иначе тик стоил бы запроса на каждого
	// подписчика.
	if !remindDue(rc, u.NotifyKind, daysUntil(localExp, now), sent) {
		return
	}

	// Истина о сроке — в панели: локальное поле отстаёт, если подписку
	// правили там руками. Без сверки один и тот же бот в один день пишет
	// «кончается через 2 дня» и «активных подписок нет».
	exp := localExp
	panelExp, ok, retry := a.panelExpiry(ctx, u.TelegramID)
	if !ok {
		if !retry {
			a.closeRemindWindows(ctx, st, rc, u, sent, daysUntil(localExp, now))
		}
		return
	}
	if !panelExp.IsZero() {
		exp = panelExp
	}
	if !exp.After(now) {
		return
	}
	left := daysUntil(exp, now)

	if u.NotifyKind == "trial" {
		w := rc.TrialDaysBefore
		if !rc.TrialEnabled || w <= 0 || left > w || sent[w] {
			return
		}
		// Окно закрывается только после реальной доставки: иначе сбой сети
		// съедал единственное напоминание навсегда. Но не бесконечно — см.
		// remindSendTries.
		key := remindFailKey(u.TelegramID, w, exp)
		if !a.sendReminder(ctx, u.TelegramID, "remind.trial", left) {
			if !a.remindFailed(key) {
				return
			}
			a.log.Warn("напоминание не доставлено, окно закрыто", "tg_id", u.TelegramID, "window", w)
		} else {
			a.remindOK(key)
		}
		sent[w] = true
		_ = st.MarkNotified(ctx, u.TelegramID, joinCSVInts(sent))
		return
	}

	target := remindTarget(rc, left, sent)
	if target == -1 {
		return
	}
	key := remindFailKey(u.TelegramID, target, exp)
	if !a.sendReminder(ctx, u.TelegramID, "remind.sub", left) {
		if !a.remindFailed(key) {
			return
		}
		a.log.Warn("напоминание не доставлено, окно закрыто", "tg_id", u.TelegramID, "window", target)
	} else {
		a.remindOK(key)
	}
	// Пропущенные окна побольше закрываем вместе с этим: иначе следующий
	// тик выбрал бы их целью и прислал второе письмо о том же сроке.
	for _, w := range rc.DaysList {
		if w >= target {
			sent[w] = true
		}
	}
	_ = st.MarkNotified(ctx, u.TelegramID, joinCSVInts(sent))
}

// closeRemindWindows закрывает окна, до которых человек «дожил», без отправки.
// Нужно там, где решение отрицательное и оно окончательное: иначе запись
// осталась бы горячей и каждые полчаса ходила в панель до самого истечения
// подписки.
func (a *App) closeRemindWindows(ctx context.Context, st interface {
	MarkNotified(context.Context, int64, string) error
}, rc model.RemindersConfig, u *model.User, sent map[int]bool, left int) {
	changed := false
	if u.NotifyKind == "trial" {
		if w := rc.TrialDaysBefore; w > 0 && left <= w && !sent[w] {
			sent[w], changed = true, true
		}
	} else {
		for _, w := range rc.DaysList {
			if left <= w && !sent[w] {
				sent[w], changed = true, true
			}
		}
	}
	if changed {
		_ = st.MarkNotified(ctx, u.TelegramID, joinCSVInts(sent))
	}
}

// remindSendTries — сколько раз подряд пытаемся доставить одно напоминание,
// прежде чем закрыть окно без него. Сбой сети не должен съедать единственное
// сообщение, но человек, заблокировавший бота, отвечает отказом всегда — без
// потолка запись дёргала бы Telegram каждые полчаса неделю подряд.
const remindSendTries = 3

// remindFailTTL — через сколько запись счётчика неудач считается мусором.
// Счётчик живёт только на время одного окна одного периода подписки.
const remindFailTTL = 48 * time.Hour

type remindFail struct {
	n  int
	at time.Time
}

// remindFailKey включает дату окончания подписки: при продлении notify_sent
// обнуляется, и счётчик прошлого периода не должен закрывать окно нового
// почти с первой попытки.
func remindFailKey(tgID int64, window int, exp time.Time) string {
	return strconv.FormatInt(tgID, 10) + ":" + strconv.Itoa(window) + ":" + exp.UTC().Format("2006-01-02")
}

// remindFailed считает неудачные попытки и говорит, исчерпан ли лимит.
func (a *App) remindFailed(key string) bool {
	now := time.Now()
	a.thrMu.Lock()
	defer a.thrMu.Unlock()
	if a.remindFails == nil {
		a.remindFails = map[string]remindFail{}
	}
	// Записи выбывают сами: окно может закрыться и мимо счётчика (панель
	// ответила «такого нет», сработало соседнее окно), и тогда убрать его
	// адресно уже некому.
	for k, v := range a.remindFails {
		if now.Sub(v.at) > remindFailTTL {
			delete(a.remindFails, k)
		}
	}
	f := a.remindFails[key]
	f.n++
	f.at = now
	if f.n >= remindSendTries {
		delete(a.remindFails, key)
		return true
	}
	a.remindFails[key] = f
	return false
}

// remindOK снимает счётчик неудач: окно закрыто по-настоящему.
func (a *App) remindOK(key string) {
	a.thrMu.Lock()
	delete(a.remindFails, key)
	a.thrMu.Unlock()
}

// remindDue — есть ли вообще повод писать этому человеку при таком остатке
// дней. Отдельно от отправки, чтобы отбор шёл по базе, а не по панели.
func remindDue(rc model.RemindersConfig, kind string, left int, sent map[int]bool) bool {
	if kind == "trial" {
		w := rc.TrialDaysBefore
		return rc.TrialEnabled && w > 0 && left <= w && !sent[w]
	}
	if !rc.Enabled || len(rc.DaysList) == 0 {
		return false
	}
	return remindTarget(rc, left, sent) != -1
}

// remindTarget — ближайшее незакрытое окно для остатка left, или -1.
func remindTarget(rc model.RemindersConfig, left int, sent map[int]bool) int {
	target := -1
	for _, w := range rc.DaysList {
		if left <= w && !sent[w] && (target == -1 || w < target) {
			target = w
		}
	}
	return target
}

// panelExpiry возвращает срок подписки по данным панели.
//
// ok=false — напоминать нельзя. retry=true при этом означает «ответа не было»
// (панель молчит): окно не закрываем, следующий тик попробует снова.
// retry=false — ответ получен и он отрицательный: окно закрываем, иначе
// запись дёргала бы панель каждые полчаса до самого истечения подписки.
// Нулевое время при ok=true — панель не настроена, истина в базе бота.
func (a *App) panelExpiry(ctx context.Context, tgID int64) (exp time.Time, ok, retry bool) {
	panel := a.panelClient()
	if panel == nil {
		return time.Time{}, true, false
	}
	_, expireAt, status, found, err := panel.SubscriptionState(ctx, tgID)
	if err != nil {
		// Панель молчит — ответа нет, решение откладываем.
		return time.Time{}, false, true
	}
	if !found {
		// Подписки в панели нет: напоминать не о чем, и это не изменится
		// само — окно закрываем, чтобы не спрашивать панель вечно.
		return time.Time{}, false, false
	}
	// Отсеиваем только то, о чём напоминать заведомо незачем. Белый список
	// статусов здесь опасен: пустое поле или новый статус в следующей версии
	// панели молча выключили бы напоминания всем.
	if status == remnawave.StatusExpired {
		return time.Time{}, false, false
	}
	pexp, perr := time.Parse(time.RFC3339, expireAt)
	if perr != nil {
		return time.Time{}, true, false
	}
	return pexp.UTC(), true, false
}

func (a *App) sendReminder(ctx context.Context, chatID int64, key string, daysLeft int) bool {
	lang := a.lang(chatID)
	rows := [][]models.InlineKeyboardButton{}
	if row := a.miniAppButtonRow(lang); row != nil {
		rows = append(rows, row)
	}
	// «Продлить», а не «Купить»: подписчику тарифа по ссылке кнопка обязана
	// открыть ЕГО тариф (showRenew), иначе напоминание продавало бы «Базовый».
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.buy"), "menu:renew")})
	text := i18n.T(lang, key, daysLeft)
	// Если у человека включено автопродление, напоминание «подписка кончается»
	// без этой строки читается как «надо срочно платить руками».
	if a.autoPayOn(ctx, chatID) {
		text += "\n\n" + i18n.T(lang, "ap.remind_hint", a.autoPayDaysText(lang))
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "ap.btn_manage"), "ap:show")})
	}
	return a.notifyKB(ctx, chatID, text, rows) != 0
}

func daysUntil(exp, now time.Time) int {
	d := exp.Sub(now)
	if d <= 0 {
		return 0
	}
	days := int(d / (24 * time.Hour))
	if d%(24*time.Hour) != 0 {
		days++
	}
	return days
}

func parseCSVInts(s string) map[int]bool {
	out := map[int]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			if n, err := strconv.Atoi(p); err == nil {
				out[n] = true
			}
		}
	}
	return out
}

func joinCSVInts(m map[int]bool) string {
	var xs []int
	for k := range m {
		xs = append(xs, k)
	}
	sort.Ints(xs)
	var ss []string
	for _, x := range xs {
		ss = append(ss, strconv.Itoa(x))
	}
	return strings.Join(ss, ",")
}

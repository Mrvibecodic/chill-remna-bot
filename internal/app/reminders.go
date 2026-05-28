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
)

// remindTick — как часто проверяем сроки. Раз в 30 минут достаточно для
// окон в днях и не нагружает БД.
const remindTick = 30 * time.Minute

// RunReminders — фоновый тикер напоминаний: до конца триала (перейти на платный)
// и до конца платной подписки (за DaysList дней — продлить). Сроки берём из
// локально сохранённого users.sub_expire_at (обновляется при каждой выдаче),
// без обращения к панели на каждый тик. Работает до отмены ctx.
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
	exp, err := time.Parse(time.RFC3339, u.SubExpireAt)
	if err != nil || !exp.After(now) {
		return // нет валидного срока или уже истекло — не напоминаем
	}
	left := daysUntil(exp, now)
	sent := parseCSVInts(u.NotifySent)

	if u.NotifyKind == "trial" {
		w := rc.TrialDaysBefore
		if !rc.TrialEnabled || w <= 0 || left > w || sent[w] {
			return
		}
		a.sendReminder(ctx, u.TelegramID, "remind.trial", left)
		sent[w] = true
		_ = st.MarkNotified(ctx, u.TelegramID, joinCSVInts(sent))
		return
	}

	// Платная подписка.
	if !rc.Enabled || len(rc.DaysList) == 0 {
		return
	}
	// Самое релевантное «ещё не отправленное» окно — наименьшее из тех, в которые
	// уже попали (left <= w). Помечаем отправленными все окна >= него (большие
	// окна к этому моменту неактуальны — например, бот лежал).
	target := -1
	for _, w := range rc.DaysList {
		if left <= w && !sent[w] && (target == -1 || w < target) {
			target = w
		}
	}
	if target == -1 {
		return
	}
	a.sendReminder(ctx, u.TelegramID, "remind.sub", left)
	for _, w := range rc.DaysList {
		if w >= target {
			sent[w] = true
		}
	}
	_ = st.MarkNotified(ctx, u.TelegramID, joinCSVInts(sent))
}

// sendReminder шлёт постоянное напоминание с кнопкой «Купить/Продлить».
func (a *App) sendReminder(ctx context.Context, chatID int64, key string, daysLeft int) {
	lang := a.lang(chatID)
	a.notifyKB(ctx, chatID, i18n.T(lang, key, daysLeft), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
	})
}

// daysUntil — округление вверх до целых суток (0.5 дня → 1).
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

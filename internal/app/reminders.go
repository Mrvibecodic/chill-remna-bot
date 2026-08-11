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
	exp, err := time.Parse(time.RFC3339, u.SubExpireAt)
	if err != nil || !exp.After(now) {
		return
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

	if !rc.Enabled || len(rc.DaysList) == 0 {
		return
	}

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

func (a *App) sendReminder(ctx context.Context, chatID int64, key string, daysLeft int) {
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
	a.notifyKB(ctx, chatID, text, rows)
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

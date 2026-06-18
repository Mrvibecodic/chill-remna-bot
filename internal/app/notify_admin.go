package app

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func (a *App) remindersCfg() model.RemindersConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.RemindersConfig{}
	}
	return a.botCfg.Reminders
}

func (a *App) showNotifyAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	rc := a.remindersCfg()
	onoff := func(b bool) string {
		if b {
			return i18n.T(lang, "admin.on")
		}
		return i18n.T(lang, "admin.off")
	}
	body := i18n.T(lang, "notify.title",
		onoff(rc.TrialEnabled), strconv.Itoa(rc.TrialDaysBefore),
		onoff(rc.Enabled), formatReminderWindows(rc, lang))

	var win []models.InlineKeyboardButton
	for _, w := range model.ReminderWindows {
		mark := "⬜"
		if rc.HasReminderDay(w) {
			mark = "✅"
		}
		win = append(win, btn(mark+" "+strconv.Itoa(w)+i18n.T(lang, "notify.day_suffix"), "ntf:w:"+strconv.Itoa(w)))
	}
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "notify.btn_trial"), "ntf:trial"), btn(i18n.T(lang, "notify.btn_trial_days"), "ntf:trialdays")},
		{btn(i18n.T(lang, "notify.btn_sub"), "ntf:sub")},
		win,
		{btn(i18n.T(lang, "btn.back"), "menu:marketing"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	}
	a.sendMktKB(ctx, chatID, body, rows)
}

func formatReminderWindows(rc model.RemindersConfig, lang string) string {
	if !rc.Enabled || len(rc.DaysList) == 0 {
		return i18n.T(lang, "admin.none")
	}
	ds := append([]int(nil), rc.DaysList...)
	sort.Sort(sort.Reverse(sort.IntSlice(ds)))
	var ss []string
	for _, d := range ds {
		ss = append(ss, strconv.Itoa(d))
	}
	return strings.Join(ss, ", ")
}

func (a *App) onNotifyAdmin(ctx context.Context, chatID int64, val string) {
	action, arg, _ := cut3(val)
	switch action {
	case "trial":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Reminders.TrialEnabled = !a.botCfg.Reminders.TrialEnabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showNotifyAdmin(ctx, chatID)
	case "sub":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Reminders.Enabled = !a.botCfg.Reminders.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showNotifyAdmin(ctx, chatID)
	case "w":
		w, _ := strconv.Atoi(arg)
		a.toggleReminderWindow(w)
		_ = a.saveBotConfig(ctx)
		a.showNotifyAdmin(ctx, chatID)
	case "trialdays":
		a.getUI(chatID).adminInput = "ntf_trial_days"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "notify.ask_trial_days"), "menu:notify")
	}
}

func (a *App) toggleReminderWindow(w int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	cur := a.botCfg.Reminders.DaysList
	for i, x := range cur {
		if x == w {
			a.botCfg.Reminders.DaysList = append(cur[:i], cur[i+1:]...)
			return
		}
	}
	a.botCfg.Reminders.DaysList = append(cur, w)
}

package app

import (
	"context"
	"strconv"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// --- админ: «🎁 Триал» (отдельный блок в Настройках подписки) ---
//
// Триал = разовая бесплатная активация подписки с собственными лимитами
// (срок в днях, GB трафика, HWID, сквады). Все параметры задаются здесь,
// дефолтов нет: пока Enabled=false или Days=0 — кнопка «🎁 Триал» у юзера
// не показывается.

func (a *App) trialCfg() (enabled bool, days, gb, hwid int, intSq []string, extSq string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	t := a.botCfg.Trial
	return t.Enabled, t.Days, t.TrafficGB, t.DeviceLimit, append([]string(nil), t.InternalSquads...), t.ExternalSquadUUID
}

func (a *App) showTrialAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	enabled, days, gb, hwid, intSq, extSq := a.trialCfg()
	statusKey := "admin.off"
	if enabled {
		statusKey = "admin.on"
	}
	gbStr := i18n.T(lang, "trial.unlimited")
	if gb > 0 {
		gbStr = strconv.Itoa(gb) + " GB"
	}
	hwidStr := i18n.T(lang, "pricing.hwid_default")
	if hwid > 0 {
		hwidStr = strconv.Itoa(hwid)
	}
	internalCSV, externalName := a.squadNames(ctx, intSq, extSq)
	body := i18n.T(lang, "trial.title",
		i18n.T(lang, statusKey), days, gbStr, hwidStr, internalCSV, externalName)

	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "admin.btn_toggle"), "trial:toggle"), btn(i18n.T(lang, "trial.btn_quick"), "trial:quick")},
		{btn(i18n.T(lang, "trial.btn_days"), "trial:days"), btn(i18n.T(lang, "trial.btn_gb"), "trial:gb")},
		{btn(i18n.T(lang, "trial.btn_hwid"), "trial:hwid"), btn(i18n.T(lang, "trial.btn_squads"), "trial:squads")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	}
	a.sendKB(ctx, chatID, body, rows)
}

// onTrialAdmin — диспетчер callback-ов «trial:*».
func (a *App) onTrialAdmin(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	action, arg, _ := cut3(val)
	switch action {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Trial.Enabled = !a.botCfg.Trial.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showTrialAdmin(ctx, chatID)
	case "days":
		a.getUI(chatID).adminInput = "trial_days"
		a.askInput(ctx, chatID, i18n.T(lang, "trial.ask_days"), "menu:trial")
	case "gb":
		a.getUI(chatID).adminInput = "trial_gb"
		a.askInput(ctx, chatID, i18n.T(lang, "trial.ask_gb"), "menu:trial")
	case "hwid":
		a.sendKB(ctx, chatID, i18n.T(lang, "trial.ask_hwid"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "pricing.dev_1"), "trial:hwidset:1"),
				btn(i18n.T(lang, "pricing.dev_3"), "trial:hwidset:3"),
				btn(i18n.T(lang, "pricing.dev_custom"), "trial:hwidset:custom")},
			{btn(i18n.T(lang, "pricing.dev_default"), "trial:hwidset:0")},
			{btn(i18n.T(lang, "btn.back"), "menu:trial"), btn(i18n.T(lang, "btn.home"), "menu:home")},
		})
	case "hwidset":
		if arg == "custom" {
			a.getUI(chatID).adminInput = "trial_hwid"
			a.askInput(ctx, chatID, i18n.T(lang, "trial.ask_hwid_custom"), "menu:trial")
			return
		}
		n, _ := strconv.Atoi(arg)
		a.setTrialHWID(n)
		_ = a.saveBotConfig(ctx)
		a.showTrialAdmin(ctx, chatID)
	case "squads":
		a.showTrialSquads(ctx, chatID)
	case "intsq":
		a.toggleTrialInternal(arg)
		_ = a.saveBotConfig(ctx)
		a.showTrialSquads(ctx, chatID)
	case "extsq":
		a.toggleTrialExternal(arg)
		_ = a.saveBotConfig(ctx)
		a.showTrialSquads(ctx, chatID)
	case "quick":
		a.startTrialQuick(ctx, chatID)
	}
}

func (a *App) showTrialSquads(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	back := []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:trial"),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	}
	if panel == nil {
		a.sendKB(ctx, chatID, i18n.T(lang, "squads.no_panel"), [][]models.InlineKeyboardButton{back})
		return
	}
	intSquads, _ := panel.ListSquads(ctx)
	extSquads, _ := panel.ListExternalSquads(ctx)
	_, _, _, _, activeInt, activeExt := a.trialCfg()
	isActive := func(uuid string) bool {
		for _, u := range activeInt {
			if u == uuid {
				return true
			}
		}
		return false
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(intSquads)+len(extSquads)+2)
	for _, sq := range intSquads {
		mark := "⬜"
		if isActive(sq.UUID) {
			mark = "✅"
		}
		rows = append(rows, []models.InlineKeyboardButton{
			btn(mark+" 🏠 "+sq.Name, "trial:intsq:"+sq.UUID),
		})
	}
	if len(extSquads) > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn("— 📡 External —", "trial:noop")})
		for _, sq := range extSquads {
			mark := "⚪"
			if activeExt == sq.UUID {
				mark = "🟢"
			}
			rows = append(rows, []models.InlineKeyboardButton{
				btn(mark+" 📡 "+sq.Name, "trial:extsq:"+sq.UUID),
			})
		}
	}
	rows = append(rows, back)
	a.sendKB(ctx, chatID, i18n.T(lang, "trial.squads_title", len(intSquads), len(extSquads), len(activeInt)), rows)
}

// --- быстрая настройка (FSM на uiState.adminInput) ---
// Шаги: trial_q_days → trial_q_gb → trial_q_hwid → готово (включить).

func (a *App) startTrialQuick(ctx context.Context, chatID int64) {
	a.getUI(chatID).adminInput = "trial_q_days"
	a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "trial.q_days"), "menu:trial")
}

// --- setters для handleAdminText ---

func (a *App) toggleTrialInternal(uuid string) {
	if uuid == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	cur := a.botCfg.Trial.InternalSquads
	for i, u := range cur {
		if u == uuid {
			a.botCfg.Trial.InternalSquads = append(cur[:i], cur[i+1:]...)
			return
		}
	}
	a.botCfg.Trial.InternalSquads = append(cur, uuid)
}

func (a *App) toggleTrialExternal(uuid string) {
	if uuid == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	if a.botCfg.Trial.ExternalSquadUUID == uuid {
		a.botCfg.Trial.ExternalSquadUUID = ""
	} else {
		a.botCfg.Trial.ExternalSquadUUID = uuid
	}
}

func (a *App) setTrialDays(n int) {
	if n < 0 {
		n = 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg != nil {
		a.botCfg.Trial.Days = n
	}
}

func (a *App) setTrialGB(n int) {
	if n < 0 {
		n = 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg != nil {
		a.botCfg.Trial.TrafficGB = n
	}
}

func (a *App) setTrialHWID(n int) {
	if n < 0 {
		n = 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg != nil {
		a.botCfg.Trial.DeviceLimit = n
	}
}

// --- юзер: активация триала ---

func (a *App) trialAvailable(ctx context.Context, chatID int64) bool {
	a.mu.Lock()
	enabled := a.botCfg != nil && a.botCfg.Trial.Enabled && a.botCfg.Trial.Days > 0
	a.mu.Unlock()
	if !enabled || a.store == nil {
		return false
	}
	u, _ := a.store.GetUser(ctx, chatID)
	if u == nil {
		return true
	}
	return u.TrialUsedAt == ""
}

func (a *App) activateTrial(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if !a.trialAvailable(ctx, chatID) {
		a.send(ctx, chatID, i18n.T(lang, "trial.not_available"))
		return
	}
	a.mu.Lock()
	panel := a.panel
	tr := a.botCfg.Trial
	strategy := a.botCfg.Pricing.ResetStrategy()
	a.mu.Unlock()
	if panel == nil {
		a.send(ctx, chatID, i18n.T(lang, "trial.fail", "panel offline"))
		return
	}
	link, expireAt, err := panel.CreateOrUpdateUserDays(ctx, chatID, tr.Days, remnawave.UserLimits{
		TrafficBytes:   int64(tr.TrafficGB) * 1024 * 1024 * 1024,
		DeviceLimit:    tr.DeviceLimit,
		InternalSquads: tr.InternalSquads,
		ExternalSquad:  tr.ExternalSquadUUID,
		Strategy:       strategy,
	})
	if err != nil {
		a.send(ctx, chatID, i18n.T(lang, "trial.fail", err.Error()))
		return
	}
	link = a.rewriteSub(link)
	if a.store != nil {
		_ = a.store.SetTrialUsed(ctx, chatID, time.Now().UTC().Format(time.RFC3339))
		_ = a.store.AddPayment(ctx, &model.Payment{
			TelegramID: chatID, Method: "trial", Months: 0, Amount: "—",
			Status: model.PaymentPaid,
		})
		_ = a.store.SetSubExpiry(ctx, chatID, expireAt, "trial")
	}
	a.invalidateSubCache(chatID)
	a.notify(ctx, chatID, i18n.T(lang, "trial.activated", tr.Days, link))
}

package app

import (
	"context"
	"strconv"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

func (a *App) showAddSubAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	c := a.botCfg.AddSub
	a.mu.Unlock()

	state := i18n.T(lang, "addsub.off")
	if c.Enabled {
		state = i18n.T(lang, "addsub.on")
	}
	traffic := i18n.T(lang, "addsub.unlimited")
	if c.TrafficGB > 0 {
		traffic = strconv.Itoa(c.TrafficGB) + " GB"
	}
	toggleLabel := i18n.T(lang, "addsub.btn_enable")
	if c.Enabled {
		toggleLabel = i18n.T(lang, "addsub.btn_disable")
	}
	title := i18n.T(lang, "addsub.title", state, traffic, len(c.InternalSquads))
	a.sendKB(ctx, chatID, title, [][]models.InlineKeyboardButton{
		{btn(toggleLabel, "addsub:toggle")},
		{btn(i18n.T(lang, "addsub.btn_gb"), "addsub:gb"), btn(i18n.T(lang, "addsub.btn_squads"), "addsub:squads")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onAddSubAdmin(ctx context.Context, chatID int64, val string) {
	action, arg, _ := cut3(val)
	switch action {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.AddSub.Enabled = !a.botCfg.AddSub.Enabled
			a.botCfg.AddSub.Init = true
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showAddSubAdmin(ctx, chatID)
	case "gb":
		a.getUI(chatID).adminInput = "addsub_gb"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "addsub.ask_gb"), "menu:addsub")
	case "squads", "refresh", "noop":
		a.showAddSubSquads(ctx, chatID)
	case "int":
		a.toggleAddSubInternal(ctx, chatID, arg)
		a.showAddSubSquads(ctx, chatID)
	}
}

func (a *App) showAddSubSquads(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	panel := a.panel
	activeInt := append([]string(nil), a.botCfg.AddSub.InternalSquads...)
	a.mu.Unlock()

	back := []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:addsub"),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	}
	if panel == nil {
		a.sendKB(ctx, chatID, i18n.T(lang, "squads.no_panel"), [][]models.InlineKeyboardButton{back})
		return
	}
	intSquads, err := panel.ListSquads(ctx)
	if err != nil {
		a.sendKB(ctx, chatID, i18n.T(lang, "squads.err", err.Error()), [][]models.InlineKeyboardButton{back})
		return
	}

	isActiveInt := func(uuid string) bool {
		for _, u := range activeInt {
			if u == uuid {
				return true
			}
		}
		return false
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(intSquads)+2)
	for _, sq := range intSquads {
		mark := "⬜"
		if isActiveInt(sq.UUID) {
			mark = "✅"
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(mark+" 🏠 "+sq.Name, "addsub:int:"+sq.UUID)})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "squads.btn_refresh"), "addsub:refresh")})
	rows = append(rows, back)
	a.sendKB(ctx, chatID, i18n.T(lang, "addsub.squads_title", len(intSquads), len(activeInt)), rows)
}

func (a *App) toggleAddSubInternal(ctx context.Context, chatID int64, uuid string) {
	if uuid == "" {
		return
	}
	a.mu.Lock()
	if a.botCfg != nil {
		cur := a.botCfg.AddSub.InternalSquads
		idx := -1
		for i, u := range cur {
			if u == uuid {
				idx = i
				break
			}
		}
		if idx >= 0 {
			a.botCfg.AddSub.InternalSquads = append(cur[:idx], cur[idx+1:]...)
		} else {
			a.botCfg.AddSub.InternalSquads = append(cur, uuid)
		}
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
}

package app

import (
	"context"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

func (a *App) showSquads(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()

	back := []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:pay"),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	}
	if panel == nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "squads.no_panel"),
			[][]models.InlineKeyboardButton{back})
		return
	}

	intSquads, err := panel.ListSquads(ctx)
	if err != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "squads.err", err.Error()),
			[][]models.InlineKeyboardButton{back})
		return
	}
	extSquads, errE := panel.ListExternalSquads(ctx)
	if errE != nil {

		extSquads = nil
	}

	a.mu.Lock()
	activeInt := append([]string(nil), a.botCfg.Plan.ActiveInternalSquads...)
	activeExt := a.botCfg.Plan.ExternalSquadUUID
	a.mu.Unlock()
	isActiveInt := func(uuid string) bool {
		for _, u := range activeInt {
			if u == uuid {
				return true
			}
		}
		return false
	}

	rows := make([][]models.InlineKeyboardButton, 0, len(intSquads)+len(extSquads)+3)

	for _, sq := range intSquads {
		mark := "⬜"
		if isActiveInt(sq.UUID) {
			mark = "✅"
		}
		rows = append(rows, []models.InlineKeyboardButton{
			btn(mark+" 🏠 "+sq.Name, "sqd:int:"+sq.UUID),
		})
	}
	if len(extSquads) > 0 {

		rows = append(rows, []models.InlineKeyboardButton{btn("— 📡 External —", "sqd:noop")})
		for _, sq := range extSquads {
			mark := "⚪"
			if activeExt == sq.UUID {
				mark = "🟢"
			}
			rows = append(rows, []models.InlineKeyboardButton{
				btn(mark+" 📡 "+sq.Name, "sqd:ext:"+sq.UUID),
			})
		}
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "squads.btn_refresh"), "sqd:refresh")})
	rows = append(rows, back)

	a.sendPayKB(ctx, chatID, i18n.T(lang, "squads.title", len(intSquads), len(extSquads), len(activeInt), display(activeExt, lang)), rows)
}

func display(v, lang string) string {
	if v == "" {
		return i18n.T(lang, "admin.none")
	}
	return v
}

func (a *App) onSquads(ctx context.Context, chatID int64, val string) {
	action, arg, _ := cut3(val)
	switch action {
	case "int":
		a.toggleInternalSquad(ctx, chatID, arg)
	case "ext":
		a.toggleExternalSquad(ctx, chatID, arg)
	case "refresh", "noop":

	}
	a.showSquads(ctx, chatID)
}

// Глобальные сквады «Продаж» — это сквады уровня тарифа «Базовый»: правка идёт
// в тариф, конфиг обновляется зеркалом (см. internal/app/plans_sync.go).
func (a *App) toggleInternalSquad(ctx context.Context, chatID int64, uuid string) {
	if err := a.togglePlanSquad(ctx, "", 0, uuid, false); err != nil {
		a.planInputFailed(ctx, chatID, err)
	}
}

func (a *App) toggleExternalSquad(ctx context.Context, chatID int64, uuid string) {
	if err := a.togglePlanSquad(ctx, "", 0, uuid, true); err != nil {
		a.planInputFailed(ctx, chatID, err)
	}
}

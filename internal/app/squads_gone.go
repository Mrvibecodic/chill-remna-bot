package app

import (
	"context"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// liveSquads — свежий список сквадов панели. Без панели ничего не известно,
// и ни один сквад не считается удалённым.
func (a *App) liveSquads(ctx context.Context) remnawave.LiveSquads {
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if panel == nil {
		return remnawave.LiveSquads{}
	}
	return panel.LiveSquads(ctx)
}

// goneSquadRows — кнопки «удалён в панели» для выбранных сквадов, которых в
// панели больше нет. Нажатие идёт в обычный тумблер и убирает сквад из набора.
// extCB == nil — экран без внешних сквадов.
func goneSquadRows(lang string, live remnawave.LiveSquads, selInt []string, selExt string, intCB, extCB func(string) string) [][]models.InlineKeyboardButton {
	var rows [][]models.InlineKeyboardButton
	add := func(icon, uuid, data string) {
		// Telegram отвергает всё сообщение, если данные кнопки длиннее 64 байт.
		if len(data) > 64 {
			return
		}
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "squads.gone", icon, shortSquadID(uuid)), data),
		})
	}
	for _, u := range selInt {
		if live.IntGone(u) {
			add("🏠", u, intCB(u))
		}
	}
	if extCB != nil && live.ExtGone(selExt) {
		add("📡", selExt, extCB(selExt))
	}
	return rows
}

// goneSquadsHint — пояснение под заголовком экрана, когда есть удалённые сквады.
func goneSquadsHint(lang string, rows [][]models.InlineKeyboardButton) string {
	if len(rows) == 0 {
		return ""
	}
	return i18n.T(lang, "squads.gone_hint")
}

func shortSquadID(uuid string) string {
	r := []rune(uuid)
	if len(r) <= 8 {
		return uuid
	}
	return string(r[:8]) + "…"
}

// pruneGoneSquads убирает из набора сквады, удалённые в панели.
func pruneGoneSquads(live remnawave.LiveSquads, ints []string, ext string) ([]string, string) {
	kept, gone := live.KeepInt(ints)
	if len(gone) > 0 {
		ints = kept
	}
	if live.ExtGone(ext) {
		ext = ""
	}
	return ints, ext
}

// prunePlanSquads — то же для уровня тарифа (months == 0) или переопределения
// срока. Переопределение, в котором не осталось сквадов, снимается целиком —
// срок снова наследует сквады тарифа, как при ручном снятии последнего.
func prunePlanSquads(live remnawave.LiveSquads, p *model.Plan, months int) {
	if months == 0 {
		p.IntSquads, p.ExtSquad = pruneGoneSquads(live, p.IntSquads, p.ExtSquad)
		return
	}
	d := durationFor(p, months, false)
	if d == nil {
		return
	}
	if d.IntSquads != nil {
		kept, _ := pruneGoneSquads(live, *d.IntSquads, "")
		if len(kept) == 0 {
			d.IntSquads = nil
		} else {
			d.IntSquads = &kept
		}
	}
	if d.ExtSquad != nil && live.ExtGone(*d.ExtSquad) {
		d.ExtSquad = nil
	}
}

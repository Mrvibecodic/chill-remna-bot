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
	panel := a.panelClient()
	if panel == nil {
		return remnawave.LiveSquads{}
	}
	return panel.LiveSquads(ctx)
}

// squadPick — что отмечено на экране выбора сквадов и куда ведут кнопки.
type squadPick struct {
	selInt []string
	selExt string
	// intCB и extCB строят данные кнопки по UUID сквада.
	intCB, extCB func(string) string
	// extHead — данные строки-заголовка над внешними; пусто — без заголовка.
	extHead string
	// extRadio — внешний сквад отмечается кружком (выбирается один), иначе
	// галочкой, как внутренние.
	extRadio bool
}

// squadPickRows — кнопки выбора сквадов для любого экрана настроек: сначала
// внутренние, затем внешние (exts == nil — их на экране нет), в конце те, что
// выбраны, но в панели уже удалены. gone возвращается отдельно — по нему
// экран дописывает подсказку.
func squadPickRows(lang string, live remnawave.LiveSquads, ints []remnawave.Squad, exts []remnawave.ExternalSquad, o squadPick) (rows, gone [][]models.InlineKeyboardButton) {
	on := make(map[string]bool, len(o.selInt))
	for _, u := range o.selInt {
		on[u] = true
	}
	rows = make([][]models.InlineKeyboardButton, 0, len(ints)+len(exts)+3)
	for _, sq := range ints {
		mark := "⬜"
		if on[sq.UUID] {
			mark = "✅"
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(mark+" 🏠 "+sq.Name, o.intCB(sq.UUID))})
	}
	if len(exts) > 0 && o.extHead != "" {
		rows = append(rows, []models.InlineKeyboardButton{btn("— 📡 External —", o.extHead)})
	}
	off, sel := "⬜", "✅"
	if o.extRadio {
		off, sel = "⚪", "🟢"
	}
	for _, sq := range exts {
		mark := off
		if o.selExt == sq.UUID {
			mark = sel
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(mark+" 📡 "+sq.Name, o.extCB(sq.UUID))})
	}
	gone = goneSquadRows(lang, live, o.selInt, o.selExt, o.intCB, o.extCB)
	return append(rows, gone...), gone
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

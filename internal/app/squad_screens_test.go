package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// squadScreens открывает все шесть экранов выбора сквадов и возвращает их
// кнопки строками «подпись|данные».
func squadScreens(t *testing.T) map[string][]string {
	t.Helper()
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	a.botCfg.Plan.ActiveInternalSquads = []string{"s1", "s-gone"}
	a.botCfg.Plan.ExternalSquadUUID = "e1"
	a.botCfg.Trial.InternalSquads = []string{"s1"}
	a.botCfg.Trial.ExternalSquadUUID = "e1"
	a.botCfg.AddSub = model.AddSubConfig{Enabled: true, InternalSquads: []string{"squad-main"}}
	a.panel = (&goneSquadsPanel{}).start(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, data := range []string{"menu:squads", "prc:sqm:1", "trial:squads", "addsub:squads", "pln:asq:" + model.PlanCodeBase, "pln:sqm:0:" + model.PlanCodeBase} {
		planTap(t, a, data)
		fm.mu.Lock()
		for _, row := range fm.lastKB {
			for _, b := range row {
				out[data] = append(out[data], b.Text+"|"+b.CallbackData)
			}
		}
		fm.mu.Unlock()
	}
	return out
}

// Шесть экранов выбора сквадов рисуют кнопки одинаково: внутренние галочками,
// внешний — кружком там, где он один на выбор, удалённые в панели — отдельной
// строкой; данные кнопок у каждого экрана свои.
func TestSquadScreensButtons(t *testing.T) {
	h := planEditHash(model.PlanCodeBase)
	want := map[string][]string{
		"menu:squads": {"✅ 🏠 Squad-1|sqd:int:s1", "⬜ 🏠 Main|sqd:int:squad-main", "— 📡 External —|sqd:noop",
			"🟢 📡 Ext-1|sqd:ext:e1", "❌ 🏠 s-gone — удалён в панели|sqd:int:s-gone"},
		"prc:sqm:1": {"⬜ 🏠 Squad-1|prc:sqi:1:s1", "⬜ 🏠 Main|prc:sqi:1:squad-main", "— 📡 External —|prc:sqm:1",
			"⚪ 📡 Ext-1|prc:sqe:1:e1"},
		"trial:squads": {"✅ 🏠 Squad-1|trial:intsq:s1", "⬜ 🏠 Main|trial:intsq:squad-main", "— 📡 External —|trial:noop",
			"🟢 📡 Ext-1|trial:extsq:e1"},
		"addsub:squads":                 {"⬜ 🏠 Squad-1|addsub:int:s1", "✅ 🏠 Main|addsub:int:squad-main"},
		"pln:asq:" + model.PlanCodeBase: {"⬜ 🏠 Squad-1|plq:a:0:" + h + ":s1", "⬜ 🏠 Main|plq:a:0:" + h + ":squad-main"},
		"pln:sqm:0:" + model.PlanCodeBase: {"✅ 🏠 Squad-1|plq:i:0:" + h + ":s1", "⬜ 🏠 Main|plq:i:0:" + h + ":squad-main",
			"✅ 📡 Ext-1|plq:e:0:" + h + ":e1", "❌ 🏠 s-gone — удалён в панели|plq:i:0:" + h + ":s-gone"},
	}
	got := squadScreens(t)
	for screen, rows := range want {
		g := got[screen]
		if len(g) < len(rows) || strings.Join(g[:len(rows)], "\n") != strings.Join(rows, "\n") {
			t.Errorf("экран %s:\n%s\nожидалось начало:\n%s", screen, strings.Join(g, "\n"), strings.Join(rows, "\n"))
		}
	}
}

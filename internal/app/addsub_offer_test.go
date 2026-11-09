package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// Карточка тарифа показывает у доп-подписки её трафик и страны — после
// условий самого тарифа; мини-апп и ЛК получают то же.
func TestAddSubOfferTerms(t *testing.T) {
	gb := func(v int) *int { return &v }
	for _, tc := range []struct {
		name      string
		ownGB     *int
		ownSquads []string
		hide      bool
		traffic   string
		countries string // "" — строки нет
		miniGB    int
		miniN     int
	}{
		{"свои условия", gb(50), []string{"s1"}, false,
			"📊 Трафик: 50 ГБ в месяц", "🌍 Страны (2): 🇷🇺 Россия, 🇰🇿 Казахстан", 50, 2},
		{"как в настройках", nil, nil, false,
			"📊 Трафик: 30 ГБ в месяц", "🌍 Страны (1): 🇩🇪 Германия", 30, 1},
		{"свои сквады удалены в панели", gb(0), []string{"gone"}, false,
			"📊 Трафик: безлимит", "🌍 Страны (1): 🇩🇪 Германия", 0, 1},
		{"строки убраны в текстах", gb(50), []string{"s1"}, true, "", "", 50, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fm, p := storefrontApp(t)
			ctx := context.Background()
			a.botCfg.AddSub = model.AddSubConfig{Enabled: true, Init: true, TrafficGB: 30, InternalSquads: []string{"g1"}}
			p.AddSubTrafficGB, p.AddSubSquads = tc.ownGB, tc.ownSquads
			if err := a.store.SavePlan(ctx, p); err != nil {
				t.Fatal(err)
			}
			a.infraCache = &infraCacheEntry{
				fetchedAt: time.Now(),
				squads: []remnawave.SquadFull{
					{UUID: "sq-vip", InboundsCount: 1, InboundUUIDs: []string{"ib1"}},
					{UUID: "s1", InboundsCount: 1, InboundUUIDs: []string{"ib2"}},
					{UUID: "g1", InboundsCount: 1, InboundUUIDs: []string{"ib3"}},
				},
				hosts: []remnawave.Host{
					{Remark: "🇳🇱 Нидерланды", InboundUUID: "ib1"},
					{Remark: "🇷🇺 Россия", InboundUUID: "ib2"},
					{Remark: "🇰🇿 Казахстан", InboundUUID: "ib2"},
					{Remark: "🇩🇪 Германия", InboundUUID: "ib3"},
				},
			}
			if tc.hide {
				if rej := i18n.SetOverrides(map[string]map[string]string{"ru": {"buy.addsub_traffic": "", "buy.addsub_countries": ""}}); rej != nil {
					t.Fatal(rej)
				}
			}
			const uid = int64(1000000001)
			a.handleCallback(ctx, cb(uid, "plo:"+p.Code))
			out := fm.joined()
			line := strings.Index(out, "входит в тариф")
			if line < 0 {
				t.Fatalf("опции доп-подписки нет:\n%s", out)
			}
			if main := strings.Index(out, "Нидерланды"); main < 0 || main > line {
				t.Fatalf("доп-подписка должна идти после условий тарифа:\n%s", out)
			}
			tail := out[line:]
			for _, want := range []string{tc.traffic, tc.countries} {
				if want != "" && !strings.Contains(tail, want) {
					t.Fatalf("нет %q:\n%s", want, tail)
				}
			}
			if tc.hide && (strings.Contains(tail, "📊") || strings.Contains(tail, "🌍")) {
				t.Fatalf("убранные строки показаны:\n%s", tail)
			}

			found := false
			for _, mp := range a.MiniPlans(ctx, uid).Plans {
				if mp.Code != p.Code {
					continue
				}
				found = true
				if mp.AddSubTrafficGB == nil || *mp.AddSubTrafficGB != tc.miniGB || len(mp.AddSubCountries) != tc.miniN {
					t.Fatalf("мини-апп: трафик %v, стран %d", mp.AddSubTrafficGB, len(mp.AddSubCountries))
				}
			}
			if !found {
				t.Fatal("тарифа нет в мини-аппе")
			}
		})
	}
}

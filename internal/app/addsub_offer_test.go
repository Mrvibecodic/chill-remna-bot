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
		noInfra   bool // список сквадов и хостов панели не получен
		traffic   string
		countries string // "" — строки нет
		miniGB    int    // -1 — в мини-апп не присылается
		miniN     int
	}{
		{"свои условия", gb(50), []string{"s1"}, false, false,
			"📊 Трафик: 50 ГБ в месяц", "🌍 Страны (2): 🇷🇺 Россия, 🇰🇿 Казахстан", 50, 2},
		{"как в настройках", nil, nil, false, false,
			"📊 Трафик: 30 ГБ в месяц", "🌍 Страны (1): 🇩🇪 Германия", 30, 1},
		{"свои сквады удалены в панели", gb(0), []string{"gone"}, false, false,
			"📊 Трафик: безлимит", "🌍 Страны (1): 🇩🇪 Германия", 0, 1},
		{"строки убраны в текстах", gb(50), []string{"s1"}, true, false, "", "", -1, 0},
		{"панель не ответила", gb(50), []string{"s1"}, false, true, "📊 Трафик: 50 ГБ в месяц", "", 50, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fm, p := storefrontApp(t)
			ctx := context.Background()
			a.botCfg.AddSub = model.AddSubConfig{Enabled: true, Init: true, TrafficGB: 30, InternalSquads: []string{"g1"}}
			p.AddSubTrafficGB, p.AddSubSquads = tc.ownGB, tc.ownSquads
			if err := a.store.SavePlan(ctx, p); err != nil {
				t.Fatal(err)
			}
			if !tc.noInfra {
				a.infraCache = addSubOfferInfra()
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
			if main := strings.Index(out, "📱"); main < 0 || main > line {
				t.Fatalf("доп-подписка должна идти после условий тарифа:\n%s", out)
			}
			tail := out[line:]
			for _, want := range []string{tc.traffic, tc.countries} {
				if want != "" && !strings.Contains(tail, want) {
					t.Fatalf("нет %q:\n%s", want, tail)
				}
			}
			if tc.countries == "" && strings.Contains(tail, "🌍") {
				t.Fatalf("строки стран быть не должно:\n%s", tail)
			}
			if tc.hide && strings.Contains(tail, "📊") {
				t.Fatalf("убранные строки показаны:\n%s", tail)
			}

			found := false
			for _, mp := range a.MiniPlans(ctx, uid).Plans {
				if mp.Code != p.Code {
					continue
				}
				found = true
				gotGB := -1 // -1 — трафик не прислан
				if mp.AddSubTrafficGB != nil {
					gotGB = *mp.AddSubTrafficGB
				}
				if gotGB != tc.miniGB || len(mp.AddSubCountries) != tc.miniN {
					t.Fatalf("мини-апп: трафик %v, стран %d", mp.AddSubTrafficGB, len(mp.AddSubCountries))
				}
			}
			if !found {
				t.Fatal("тарифа нет в мини-аппе")
			}
		})
	}
}

func addSubOfferInfra() *infraCacheEntry {
	return &infraCacheEntry{
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
}

// Названия стран приходят из имён хостов панели: в карточке тарифа (HTML)
// они экранируются — и у тарифа, и у доп-подписки.
func TestOfferCountriesEscaped(t *testing.T) {
	a, fm, p := storefrontApp(t)
	ctx := context.Background()
	a.botCfg.AddSub = model.AddSubConfig{Enabled: true, Init: true, InternalSquads: []string{"sq-vip"}}
	a.infraCache = &infraCacheEntry{
		fetchedAt: time.Now(),
		squads:    []remnawave.SquadFull{{UUID: "sq-vip", InboundsCount: 1, InboundUUIDs: []string{"ib1"}}},
		hosts:     []remnawave.Host{{Remark: "🇬🇧 UK & <Ireland>", InboundUUID: "ib1"}},
	}
	a.handleCallback(ctx, cb(int64(1000000001), "plo:"+p.Code))
	out := fm.joined()
	if strings.Contains(out, "<Ireland>") || strings.Count(out, "UK &amp; &lt;Ireland&gt;") != 2 {
		t.Fatalf("страны не экранированы (ждали у тарифа и у доп-подписки):\n%s", out)
	}
}

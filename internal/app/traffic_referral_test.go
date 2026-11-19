package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// refPanel — панель с несколькими учётками по номеру Telegram.
type refPanel struct {
	mu     sync.Mutex
	limit  map[int64]int64
	failTo int64
	loseTo int64
}

func newRefPanel(t *testing.T) (*refPanel, string) {
	t.Helper()
	p := &refPanel{limit: map[int64]int64{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p.mu.Lock()
		defer p.mu.Unlock()
		user := func(id int64) map[string]any {
			return map[string]any{
				"uuid": fmt.Sprintf("u%d", id), "tag": "CHILLBOT", "username": fmt.Sprintf("tg_%d", id), "telegramId": id,
				"expireAt": "2099-12-31T00:00:00.000Z", "trafficLimitBytes": p.limit[id], "trafficLimitStrategy": "NO_RESET",
				"subscriptionUrl": "https://sub.example.com/x", "status": "ACTIVE",
			}
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/users/by-username/tg_"):
			var id int64
			_, _ = fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/api/users/by-username/"), "tg_%d", &id)
			if _, ok := p.limit[id]; !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"User not found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"response": user(id)})
		case strings.Contains(r.URL.Path, "/by-telegram-id/"):
			var id int64
			_, _ = fmt.Sscan(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], &id)
			list := []map[string]any{}
			if _, ok := p.limit[id]; ok {
				list = append(list, user(id))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"response": list})
		case r.URL.Path == "/api/users" && (r.Method == http.MethodPatch || r.Method == http.MethodPost):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			var id int64
			if v, ok := body["telegramId"].(float64); ok {
				id = int64(v)
			} else if u, ok := body["uuid"].(string); ok {
				_, _ = fmt.Sscanf(u, "u%d", &id)
			} else if n, ok := body["username"].(string); ok {
				_, _ = fmt.Sscanf(n, "tg_%d", &id)
			}
			if id == p.failTo && r.Method == http.MethodPatch {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
				return
			}
			if v, ok := body["trafficLimitBytes"].(float64); ok {
				p.limit[id] = int64(v)
			} else if _, ok := p.limit[id]; !ok {
				p.limit[id] = 0
			}
			if id == p.loseTo && r.Method == http.MethodPatch {
				p.loseTo = 0
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"response": user(id)})
		default:
			_, _ = w.Write([]byte(`{"response":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return p, srv.URL
}

func (p *refPanel) gb(id int64) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.limit[id] / gib
}

const refFriend = int64(1000000001)

// refApp — трафик-тариф 150 ГБ с бонусом 10 ГБ «кому»; пригласивший —
// trafficUID, приглашённый — refFriend; у обоих деньги на балансе.
func refApp(t *testing.T, to string) (*App, *fakeStore, *refPanel) {
	t.Helper()
	p, url := newRefPanel(t)
	a, fs := snapApp(t, url)
	a.botCfg.Referral = model.ReferralConfig{Init: true, Enabled: true, BonusKind: model.ReferralBonusBalance, BonusValue: 50, OnFirstPay: true}
	ctx := context.Background()
	for _, id := range []int64{trafficUID, refFriend} {
		_ = fs.UpsertUser(ctx, id)
		_ = fs.AddBalance(ctx, id, 1000000)
	}
	_ = fs.SetReferredBy(ctx, refFriend, trafficUID)
	plan := &model.Plan{
		Code: "trf150", Name: "Трафик 150", Enabled: true, Kind: model.PlanKindTraffic,
		TrafficGB: 150, DeviceLimit: 3, Currency: "₽", Availability: model.PlanAvailAll,
		Traffic: &model.PlanTraffic{Price: "450", Packs: []model.PlanPack{{ID: "pk5000", GB: 50, Price: "160"}}, RefGB: 10, RefTo: to},
	}
	if err := fs.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	return a, fs, p
}

func refTexts(a *App) string {
	m := a.msg.(*fakeMsg)
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.texts, "\n---\n")
}

func TestTrafficRef_Both(t *testing.T) {
	a, fs, p := refApp(t, model.RefToBoth)
	ctx := context.Background()
	buyTrafficAs(t, a, trafficUID, "trf150", "")
	before, _ := fs.GetUser(ctx, trafficUID)

	buyTrafficAs(t, a, refFriend, "trf150", "")
	if p.gb(refFriend) != 160 || p.gb(trafficUID) != 160 {
		t.Fatalf("лимиты: приглашённый %d, пригласивший %d", p.gb(refFriend), p.gb(trafficUID))
	}
	out := refTexts(a)
	for _, k := range []string{"ref.gb_got_invitee", "ref.gb_got_referrer"} {
		if !strings.Contains(out, i18n.T("ru", k, "Трафик 150", 10)) {
			t.Fatalf("нет сообщения %s:\n%s", k, out)
		}
	}
	if after, _ := fs.GetUser(ctx, trafficUID); after.Balance != before.Balance || after.RefEarned != 0 {
		t.Fatalf("трафик-тариф дал пригласившему деньги: %d → %d", before.Balance, after.Balance)
	}
	if logs, _ := fs.PayLogs(ctx, "", refFriend, 100); stageCount(logs, "ref_gb") != 2 {
		t.Fatalf("в журнале не обе записи ref_gb: %+v", logs)
	}

	// Каждая покупка тарифа даёт бонус; пакеты — нет.
	buyTrafficAs(t, a, refFriend, "trf150", "")
	if p.gb(trafficUID) != 170 {
		t.Fatalf("повторная покупка: пригласивший %d", p.gb(trafficUID))
	}
	buyTrafficAs(t, a, refFriend, "trf150", "pk5000")
	if p.gb(trafficUID) != 170 || p.gb(refFriend) != 210 {
		t.Fatalf("пакет дал бонус: пригласивший %d, приглашённый %d", p.gb(trafficUID), p.gb(refFriend))
	}
}

func stageCount(logs []model.PayLogEntry, stage string) int {
	n := 0
	for _, l := range logs {
		if l.Stage == stage {
			n++
		}
	}
	return n
}

func TestTrafficRef_ReferrerWithoutTraffic(t *testing.T) {
	a, _, p := refApp(t, model.RefToReferrer)
	buyTrafficAs(t, a, refFriend, "trf150", "")
	if p.gb(refFriend) != 150 {
		t.Fatalf("приглашённому начислено при «пригласившему»: %d", p.gb(refFriend))
	}
	if _, ok := p.limit[trafficUID]; ok {
		t.Fatal("пригласившему без тарифа заведена учётка")
	}
	if out := refTexts(a); !strings.Contains(out, i18n.T("ru", "ref.gb_skipped", "Трафик 150", 10)) {
		t.Fatalf("нет объяснения пригласившему:\n%s", out)
	}
}

// Пригласивший на безлимите — бонус не переносится, приходит объяснение.
func TestTrafficRef_ReferrerUnlimited(t *testing.T) {
	a, _, p := refApp(t, model.RefToReferrer)
	buyTrafficAs(t, a, trafficUID, "trf150", "")
	p.mu.Lock()
	p.limit[trafficUID] = 0
	p.mu.Unlock()
	buyTrafficAs(t, a, refFriend, "trf150", "")
	if p.gb(trafficUID) != 0 || !strings.Contains(refTexts(a), i18n.T("ru", "ref.gb_skipped", "Трафик 150", 10)) {
		t.Fatalf("безлимит пригласившего: %d", p.gb(trafficUID))
	}
}

func TestTrafficRef_InviteeOnly(t *testing.T) {
	a, _, p := refApp(t, model.RefToInvitee)
	buyTrafficAs(t, a, trafficUID, "trf150", "")
	buyTrafficAs(t, a, refFriend, "trf150", "")
	if p.gb(refFriend) != 160 || p.gb(trafficUID) != 150 {
		t.Fatalf("лимиты: приглашённый %d, пригласивший %d", p.gb(refFriend), p.gb(trafficUID))
	}
	if strings.Contains(refTexts(a), i18n.T("ru", "ref.gb_skipped", "Трафик 150", 10)) {
		t.Fatal("пригласившему пришло объяснение при «приглашённому»")
	}
}

func TestTrafficRef_NotGiven(t *testing.T) {
	cases := map[string]func(a *App, fs *fakeStore){
		"рефералка выключена": func(a *App, _ *fakeStore) { a.botCfg.Referral.Enabled = false },
		"бонус 0": func(_ *App, fs *fakeStore) {
			pl, _ := fs.GetPlan(context.Background(), "trf150")
			pl.Traffic.RefGB = 0
			_ = fs.SavePlan(context.Background(), pl)
		},
		"не приглашён": func(_ *App, fs *fakeStore) { fs.users[refFriend].ReferredBy = 0 },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			a, fs, p := refApp(t, model.RefToBoth)
			buyTrafficAs(t, a, trafficUID, "trf150", "")
			setup(a, fs)
			buyTrafficAs(t, a, refFriend, "trf150", "")
			if p.gb(refFriend) != 150 || p.gb(trafficUID) != 150 {
				t.Fatalf("бонус начислен: приглашённый %d, пригласивший %d", p.gb(refFriend), p.gb(trafficUID))
			}
		})
	}
}

func TestTrafficRef_GrantAndRetry(t *testing.T) {
	a, fs, p := refApp(t, model.RefToBoth)
	ctx := context.Background()
	buyTrafficAs(t, a, trafficUID, "trf150", "")
	pl, _ := fs.GetPlan(ctx, "trf150")
	snap := a.trafficSnapshotOf(pl, nil)

	if _, err := a.closeTrafficDeal(ctx, refFriend, model.PayMethodGrant, "—", "grant-1", snap, true); err != nil {
		t.Fatal(err)
	}
	if p.gb(trafficUID) != 150 || p.gb(refFriend) != 150 {
		t.Fatalf("выдача администратором дала бонус: %d / %d", p.gb(trafficUID), p.gb(refFriend))
	}
	for range 2 {
		_, _ = a.closeTrafficDeal(ctx, refFriend, model.PayMethodYooKassa, "450 ₽", "yk-ref-1", snap, false)
	}
	if p.gb(trafficUID) != 160 || p.gb(refFriend) != 160 {
		t.Fatalf("повтор доставки оплаты: %d / %d", p.gb(trafficUID), p.gb(refFriend))
	}
}

func TestTrafficRef_PanelErrorToAdmin(t *testing.T) {
	a, fs, p := refApp(t, model.RefToReferrer)
	ctx := context.Background()
	buyTrafficAs(t, a, trafficUID, "trf150", "")
	p.failTo = trafficUID
	buyTrafficAs(t, a, refFriend, "trf150", "")
	out := refTexts(a)
	if !strings.Contains(out, "⚠️ Реферальный бонус +10 ГБ") {
		t.Fatalf("администратору не сообщили о сбое:\n%s", out)
	}
	if strings.Contains(out, i18n.T("ru", "ref.gb_skipped", "Трафик 150", 10)) {
		t.Fatal("сбой панели выдан пригласившему как отказ")
	}
	if logs, _, _ := fs.PayLogsFiltered(ctx, payErrorStageList(), "", 100); stageCount(logs, "ref_gb_error") != 1 {
		t.Fatalf("сбой бонуса не попал в сбои оплат: %+v", logs)
	}

	// Панель прибавила, но ответ потерян — это начисление, не сбой.
	p.failTo = 0
	p.loseTo = trafficUID
	buyTrafficAs(t, a, refFriend, "trf150", "")
	if p.gb(trafficUID) != 160 || !strings.Contains(refTexts(a), i18n.T("ru", "ref.gb_got_referrer", "Трафик 150", 10)) {
		t.Fatalf("потерянный ответ панели: лимит %d", p.gb(trafficUID))
	}
}

func TestTrafficRef_ReferralScreen(t *testing.T) {
	a, fs, _ := refApp(t, model.RefToReferrer)
	ctx := context.Background()
	note := a.referralTrafficNote(ctx, refFriend, "ru")
	want := i18n.T("ru", "ref.traffic_gb_referrer", "Трафик 150", 10) + "\n" + i18n.T("ru", "ref.traffic_only_gb")
	if note != want {
		t.Fatalf("строки экрана:\n%q\nждали\n%q", note, want)
	}
	if mini := a.MiniReferral(ctx, refFriend).Note; mini != stripHTMLTags(want) || !strings.Contains(mini, "\n") {
		t.Fatalf("мини-апп: %q", mini)
	}
	pl, _ := fs.GetPlan(ctx, "trf150")
	for _, mode := range []string{model.PlanAvailLink, model.PlanAvailList} {
		pl.Availability = mode
		_ = fs.SavePlan(ctx, pl)
		if note := a.referralTrafficNote(ctx, refFriend, "ru"); note != "" {
			t.Fatalf("скрытый тариф (%s) виден на экране рефералки: %q", mode, note)
		}
	}
	pl.Availability = model.PlanAvailAll
	_ = fs.SavePlan(ctx, pl)
	buyTrafficAs(t, a, trafficUID, "trf150", "")
	pl.Availability = model.PlanAvailLink
	_ = fs.SavePlan(ctx, pl)
	if note := a.referralTrafficNote(ctx, trafficUID, "ru"); !strings.Contains(note, "Трафик 150") {
		t.Fatalf("владелец тарифа по ссылке не видит его бонус: %q", note)
	}
	pl.Availability = model.PlanAvailAll
	pl.Traffic.RefGB = 0
	_ = fs.SavePlan(ctx, pl)
	if note := a.referralTrafficNote(ctx, refFriend, "ru"); note != i18n.T("ru", "ref.no_traffic") {
		t.Fatalf("без бонуса: %q", note)
	}
}

func TestTrafficRef_AdminSettings(t *testing.T) {
	a, fs, _ := refApp(t, "")
	fm := a.msg.(*fakeMsg)
	ctx := context.Background()
	pl, _ := fs.GetPlan(ctx, "trf150")
	if pl.Traffic.RefTo != model.RefToReferrer {
		t.Fatalf("кому по умолчанию: %q", pl.Traffic.RefTo)
	}
	planTap(t, a, "pln:open:trf150")
	if !strings.Contains(fm.last(), "🎁 Рефералка: +10 ГБ, пригласившему") || !strings.Contains(strings.Join(fm.cbData, " "), "pln:rf:trf150") {
		t.Fatalf("карточка: %q", fm.last())
	}
	assertPhoneFit(t, fm.lastKB)

	a.botCfg.Referral.Enabled = false
	planTap(t, a, "pln:rf:trf150")
	if !strings.Contains(fm.last(), i18n.T("ru", "plans.ref_program_off")) {
		t.Fatalf("нет предупреждения о выключенной программе: %q", fm.last())
	}
	assertPhoneFit(t, fm.lastKB)

	planTap(t, a, "pln:rft:b:trf150")
	if fm.lastKB[1][0].Text != "👤 Пригласившему" || fm.lastKB[3][0].Text != "✅ 👥 Обоим" {
		t.Fatalf("отметка «обоим»: %v", fm.lastKB)
	}
	planTap(t, a, "pln:tin:r:trf150")
	a.handleMessage(ctx, msgText(planAdmin, "0"))
	pl, _ = fs.GetPlan(ctx, "trf150")
	if pl.Traffic.RefGB != 0 || pl.Traffic.RefTo != model.RefToBoth || !strings.Contains(fm.last(), "Сейчас: выключена") {
		t.Fatalf("после правок: %+v %q", pl.Traffic, fm.last())
	}
	planTap(t, a, "pln:tin:r:trf150")
	a.handleMessage(ctx, msgText(planAdmin, "-1"))
	if !strings.Contains(fm.last(), i18n.T("ru", "plans.ref_gb_invalid")) {
		t.Fatalf("неверный ввод не отклонён: %q", fm.last())
	}
	planTap(t, a, "pln:tin:r:trf150")
	planTap(t, a, "pln:open:trf150")
	if in := a.getUI(planAdmin).adminInput; in != "" {
		t.Fatalf("ввод не снят навигацией: %q", in)
	}
	a.handleMessage(ctx, msgText(planAdmin, "33"))
	if pl, _ = fs.GetPlan(ctx, "trf150"); pl.Traffic.RefGB != 0 {
		t.Fatalf("брошенный вопрос записал бонус: %d", pl.Traffic.RefGB)
	}
	planTap(t, a, "pln:tin:r:trf150")
	a.handleMessage(ctx, msgText(planAdmin, "25"))
	if pl, _ = fs.GetPlan(ctx, "trf150"); pl.Traffic.RefGB != 25 || pl.Traffic.RefTo != model.RefToBoth {
		t.Fatalf("ввод гигабайт: %+v", pl.Traffic)
	}
}

// Человек закрыл мини-апп сразу после оплаты — бонус всё равно доходит.
func TestTrafficRef_SurvivesCancel(t *testing.T) {
	a, fs, p := refApp(t, model.RefToInvitee)
	buyTrafficAs(t, a, refFriend, "trf150", "")
	pl, _ := fs.GetPlan(context.Background(), "trf150")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.trafficRefBonus(ctx, refFriend, model.PayMethodYooKassa, "yk-ref-c", a.trafficSnapshotOf(pl, nil))
	if p.gb(refFriend) != 170 {
		t.Fatalf("бонус потерян при отменённом запросе: %d", p.gb(refFriend))
	}
}

// Аккаунты кабинета по почте (отрицательный номер) — и пригласивший, и
// приглашённый.
func TestTrafficRef_CabinetAccounts(t *testing.T) {
	a, fs, p := refApp(t, model.RefToBoth)
	ctx := context.Background()
	const cabRef, cabFriend = int64(-1000000014), int64(-1000000015)
	for _, id := range []int64{cabRef, cabFriend} {
		_ = fs.UpsertUser(ctx, id)
		_ = fs.AddBalance(ctx, id, 1000000)
	}
	_ = fs.SetReferredBy(ctx, cabFriend, cabRef)
	buyTrafficAs(t, a, cabRef, "trf150", "")
	buyTrafficAs(t, a, cabFriend, "trf150", "")
	if p.gb(cabRef) != 160 || p.gb(cabFriend) != 160 {
		t.Fatalf("кабинетные аккаунты: пригласивший %d, приглашённый %d", p.gb(cabRef), p.gb(cabFriend))
	}
}

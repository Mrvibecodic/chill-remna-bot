package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func adminTap(t *testing.T, a *App, data string) {
	t.Helper()
	a.handleCallback(context.Background(), cb(100, data))
}

// Выдача трафик-тарифа без срока и пакета к нему — кнопками админки.
func TestGrantTraffic_PlanThenPack(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	fm := a.msg.(*fakeMsg)
	uid := "555"

	adminTap(t, a, "usr:gr:"+uid)
	if strings.Contains(strings.Join(fm.allCallbackData(), " "), "usr:grk:") {
		t.Fatal("пакет предложен человеку без трафик-тарифа")
	}
	if !hasCallback(fm, "usr:grp:"+uid+":"+planEditHash(plan.Code)) {
		t.Fatalf("трафик-тарифа нет в выдаче: %v", fm.lastKB)
	}
	assertPhoneFit(t, fm.lastKB)

	adminTap(t, a, "usr:grp:"+uid+":"+planEditHash(plan.Code))
	if !hasCallback(fm, "usr:grt:"+uid+":0") || hasCallback(fm, "usr:grd:"+uid) {
		t.Fatalf("сроки трафик-тарифа: %v", fm.lastKB)
	}
	assertPhoneFit(t, fm.lastKB)
	// Срок в месяцах трафик-тарифу подделать нельзя.
	adminTap(t, a, "usr:grt:"+uid+":1")
	if !strings.Contains(fm.joined(), "Выбор устарел") {
		t.Fatal("трафик-тариф выдаётся на месяц")
	}

	adminTap(t, a, "usr:grp:"+uid+":"+planEditHash(plan.Code))
	adminTap(t, a, "usr:grt:"+uid+":0")
	confirm := fm.last()
	for _, want := range []string{"Проверьте выдачу", "бессрочно", "заменится"} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("подтверждение без %q: %s", want, confirm)
		}
	}
	assertPhoneFit(t, fm.lastKB)
	adminTap(t, a, "usr:grok:"+uid+":"+grantNonceFor(t, fm, uid))
	if exp, limit, _, _ := panel.state(); exp != remnawave.EternalExpire || limit != 150*gib {
		t.Fatalf("выдача тарифа: %s %d", exp, limit)
	}
	if got := grantPayments(fs); len(got) != 1 || !got[0].Snapshot.IsTraffic() {
		t.Fatalf("запись выдачи: %+v", got)
	}
	if !strings.Contains(fm.joined(), "Срока у него нет") || !strings.Contains(fm.joined(), "бессрочно.") {
		t.Fatalf("сообщения о выдаче:\n%s", fm.joined())
	}

	panel.setUsed(40 * gib)
	adminTap(t, a, "usr:gr:"+uid)
	if len(fm.lastKB) == 0 || fm.lastKB[0][0].CallbackData != "usr:grk:"+uid {
		t.Fatalf("пакета нет первой строкой: %v", fm.lastKB)
	}
	adminTap(t, a, "usr:grk:"+uid)
	if !hasCallback(fm, "usr:grk:"+uid+":pk5000") {
		t.Fatalf("пакеты: %v", fm.lastKB)
	}
	assertPhoneFit(t, fm.lastKB)
	adminTap(t, a, "usr:grk:"+uid+":pk5000")
	if c := fm.last(); !strings.Contains(c, "+ 50 ГБ") || !strings.Contains(c, "150 ГБ → <b>200 ГБ</b>") {
		t.Fatalf("подтверждение пакета: %s", c)
	}
	assertPhoneFit(t, fm.lastKB)
	adminTap(t, a, "usr:grok:"+uid+":"+grantNonceFor(t, fm, uid))
	if _, limit, used, _ := panel.state(); limit != 200*gib || used != 40*gib {
		t.Fatalf("выдача пакета: лимит %d, расход %d", limit, used)
	}
	if !strings.Contains(fm.joined(), "Пакет выдан") || !strings.Contains(fm.joined(), "Вам добавлен трафик") {
		t.Fatalf("сообщения о пакете:\n%s", fm.joined())
	}
	got := grantPayments(fs)
	packs := 0
	for _, p := range got {
		if p.Snapshot.PackGB == 50 {
			packs++
		}
	}
	if len(got) != 2 || packs != 1 {
		t.Fatalf("записи выдач: %d, пакетов %d", len(got), packs)
	}

	// Выключенный тариф: пакеты владельцу выдаются.
	plan.Enabled = false
	_ = fs.SavePlan(ctx, plan)
	adminTap(t, a, "usr:gr:"+uid)
	if !hasCallback(fm, "usr:grk:"+uid) {
		t.Fatal("пакет к выключенному тарифу не предложен")
	}
	// Удалённый тариф: пакетов нет.
	_ = fs.DeletePlan(ctx, plan.Code)
	adminTap(t, a, "usr:gr:"+uid)
	if hasCallback(fm, "usr:grk:"+uid) {
		t.Fatal("пакет к удалённому тарифу")
	}
	adminTap(t, a, "usr:grk:"+uid+":pk5000")
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("пакет к удалённому тарифу выдан: %d", limit)
	}
}

// Дни промокодом и рефералкой к бессрочной подписке не прибавляются: код
// остаётся неиспользованным, срок — бессрочным.
func TestTrafficPromoDaysRefused(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	buyTrafficWithBalance(t, a, plan.Code, "")
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "DAYS7", Kind: model.PromoKindDays, Value: 7})
	msg, ok := a.redeemPromo(ctx, trafficUID, "days7")
	if ok || !strings.Contains(msg, "не подходит") {
		t.Fatalf("дни к трафик-тарифу: %v %s", ok, msg)
	}
	if done, _ := fs.PromoRedeemedBy(ctx, "DAYS7", trafficUID); done {
		t.Fatal("код потрачен при отказе")
	}
	if ok, found := a.addReferralDays(ctx, trafficUID, 7); ok || found {
		t.Fatalf("реферальные дни к бессрочной: ok=%v found=%v", ok, found)
	}
	if exp, _, _, _ := panel.state(); exp != remnawave.EternalExpire {
		t.Fatalf("срок стал срочным: %s", exp)
	}
	// Трафик промокодом — работает.
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GB10", Kind: model.PromoKindTraffic, Value: 10})
	if msg, ok := a.redeemPromo(ctx, trafficUID, "gb10"); !ok {
		t.Fatalf("промокод на трафик: %s", msg)
	}
	if _, limit, _, _ := panel.state(); limit != 160*gib {
		t.Fatalf("трафик промокодом: %d", limit)
	}
	if u, _ := fs.GetUser(ctx, trafficUID); u.TrafficBonus != nil {
		t.Fatalf("запись разового подарка без сброса: %+v", u.TrafficBonus)
	}
}

// Сторож отката: срочная сделка со сроком после бессрочного у человека, у
// которого был трафик-тариф, — в списке; ручной «вечный» срок без трафика —
// нет. Сроки бот не трогает.
func TestTrafficRollbackNotice(t *testing.T) {
	a, fs, _, plan := trafficDealApp(t)
	ctx := context.Background()
	fm := a.msg.(*fakeMsg)
	buyTrafficWithBalance(t, a, plan.Code, "")
	// Прошлая версия продала срочный тариф поверх бессрочного.
	timed := &model.PlanSnapshot{Code: "term01", Months: 1}
	_ = fs.AddPayment(ctx, &model.Payment{TelegramID: trafficUID, Method: model.PayMethodBalance, Months: 1, Amount: "199 ₽",
		Status: model.PaymentPaid, ExtID: "old-1", Snapshot: timed})
	_ = fs.SetUserSnapshot(ctx, trafficUID, timed)
	_ = fs.SetSubExpiry(ctx, trafficUID, "2099-08-01T00:00:00Z", "paid")
	// Ручной «вечный» срок у человека без трафик-тарифа.
	_ = fs.UpsertUser(ctx, 1000000002)
	_ = fs.SetUserSnapshot(ctx, 1000000002, timed)
	_ = fs.SetSubExpiry(ctx, 1000000002, "2099-12-31T00:00:00Z", "paid")
	// Отклонённая заявка на трафик-тариф сделкой не считается.
	_ = fs.AddPayment(ctx, &model.Payment{TelegramID: 1000000002, Method: model.PayMethodP2P, Amount: "450 ₽",
		Status: model.PaymentRejected, Snapshot: a.trafficSnapshotOf(plan, nil)})

	a.noticeTrafficRollback(ctx)
	msg := fm.joined()
	if !strings.Contains(msg, "после отката") || !strings.Contains(msg, "01.08.2099") {
		t.Fatalf("сторож не прислал список:\n%s", msg)
	}
	if strings.Contains(msg, "31.12.2099") {
		t.Fatal("в списке ручной срок без трафик-тарифа")
	}
	if u, _ := fs.GetUser(ctx, trafficUID); u.SubExpireAt != "2099-08-01T00:00:00Z" {
		t.Fatalf("сторож поправил срок сам: %s", u.SubExpireAt)
	}
}

// В платежах человека трафик-тариф — «бессрочно», пакет — «пакет + N ГБ»,
// а не «1 мес» и не пополнение баланса.
func TestTrafficPayLabels(t *testing.T) {
	a, fs, _, plan := trafficDealApp(t)
	ctx := context.Background()
	buyTrafficWithBalance(t, a, plan.Code, "")
	buyTrafficWithBalance(t, a, plan.Code, "pk5000")
	pays, _, _ := fs.ListUserPayments(ctx, trafficUID, 10, 0)
	var labels []string
	for i := range pays {
		labels = append(labels, a.userPayLabel(ctx, "ru", &pays[i], map[string]string{}))
	}
	all := strings.Join(labels, " | ")
	if !strings.Contains(all, "бессрочно") || !strings.Contains(all, "+50 ГБ") {
		t.Fatalf("подписи платежей: %s", all)
	}
	if strings.Contains(all, "1 мес") || strings.Contains(all, "Пополнение") {
		t.Fatalf("трафик-сделки подписаны как срок или пополнение: %s", all)
	}
}

// Сверка лимитов у трафик-тарифа: поднимает урезанный в панели лимит и не
// трогает бессрочный срок (раньше отсчёт «0 дней от сегодня» выключал бы
// подписку).
func TestTrafficSubRepairKeepsEternal(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	buyTrafficWithBalance(t, a, plan.Code, "")
	panel.mu.Lock()
	panel.limit = 100 * gib
	panel.mu.Unlock()
	u, _ := fs.GetUser(ctx, trafficUID)
	if !a.repairTarget(ctx, a.panelClient(), trafficUID, u.Snapshot, false) {
		t.Fatal("сверка не подняла лимит")
	}
	if exp, limit, _, _ := panel.state(); exp != remnawave.EternalExpire || limit != 150*gib {
		t.Fatalf("после сверки: срок %s, лимит %d", exp, limit)
	}
	if !a.repairTarget(ctx, a.panelClient(), trafficUID, u.Snapshot, true) {
		t.Fatal("полная переприменка не прошла")
	}
	if exp, _, _, strat := panel.state(); exp != remnawave.EternalExpire || strat != "NO_RESET" {
		t.Fatalf("после полной переприменки: срок %s, сброс %s", exp, strat)
	}
}

// Заголовок звёздного счёта у Telegram — до 32 знаков: имя тарифа в него не
// идёт (оно в описании), пакет — коротко.
func TestTrafficStarsTitle(t *testing.T) {
	long := &model.PlanSnapshot{Code: "trf150", Name: strings.Repeat("Очень длинное имя ", 4), Kind: model.PlanKindTraffic}
	for _, s := range []*model.PlanSnapshot{long, {Code: "trf150", Kind: model.PlanKindTraffic, PackID: "pk5000", PackGB: 1000000}} {
		title := trafficStarsTitle("ru", s)
		if n := len([]rune(title)); n == 0 || n > starsTitleMax {
			t.Fatalf("заголовок %q: %d знаков", title, n)
		}
	}
	if desc := trafficInvoiceDesc("ru", long); !strings.Contains(desc, "Очень длинное имя") {
		t.Fatalf("описание без имени тарифа: %s", desc)
	}
}

package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

const gib = int64(1024 * 1024 * 1024)

// trafficPanel — одна учётка панели с состоянием: срок, потолок, расход,
// стратегия; помнит записи.
type trafficPanel struct {
	mu       sync.Mutex
	exists   bool
	expire   string
	limit    int64
	used     int64
	strategy string
	resets   int
	// loseReply — следующая запись применяется, но ответ теряется (500).
	loseReply bool
	// refuse — следующая запись отклоняется и не применяется (400).
	refuse bool
	// blindAfterWrite — после следующей записи панель не отвечает на одно
	// чтение (502): исход записи узнать нельзя.
	blindAfterWrite bool
	blind           int
	writes          []map[string]any
	srv             *httptest.Server
}

func newTrafficPanel(t *testing.T) *trafficPanel {
	t.Helper()
	p := &trafficPanel{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p.mu.Lock()
		defer p.mu.Unlock()
		user := func() map[string]any {
			return map[string]any{
				"uuid": "u1", "tag": "CHILLBOT", "username": "tg_555", "telegramId": 555,
				"expireAt": p.expire, "trafficLimitBytes": p.limit, "usedTrafficBytes": p.used,
				"trafficLimitStrategy": p.strategy, "subscriptionUrl": "https://sub.example.com/x", "status": "ACTIVE",
			}
		}
		switch {
		case strings.Contains(r.URL.Path, "/by-telegram-id/") && p.blind > 0:
			p.blind--
			w.WriteHeader(http.StatusBadGateway)
		case strings.Contains(r.URL.Path, "/by-telegram-id/"):
			list := []map[string]any{}
			if p.exists {
				list = append(list, user())
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"response": list})
		case strings.Contains(r.URL.Path, "reset-traffic"):
			p.resets++
			p.used = 0
			_, _ = w.Write([]byte(`{"response":{}}`))
		case r.URL.Path == "/api/users" && (r.Method == http.MethodPatch || r.Method == http.MethodPost):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if p.blindAfterWrite {
				p.blindAfterWrite, p.blind = false, 1
			}
			if p.refuse {
				p.refuse = false
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"refused"}`))
				return
			}
			p.writes = append(p.writes, body)
			p.exists = true
			if v, ok := body["expireAt"].(string); ok {
				p.expire = v
			}
			if v, ok := body["trafficLimitBytes"].(float64); ok {
				p.limit = int64(v)
			}
			if v, ok := body["trafficLimitStrategy"].(string); ok {
				p.strategy = v
			}
			if p.loseReply {
				p.loseReply = false
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"response": user()})
		default:
			_, _ = w.Write([]byte(`{"response":[]}`))
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *trafficPanel) state() (expire string, limit, used int64, strategy string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.expire, p.limit, p.used, p.strategy
}

func (p *trafficPanel) setUsed(v int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.used = v
}

const trafficUID = int64(555)

// trafficDealApp — бот с трафик-тарифом на 150 ГБ за 450 ₽ и пакетом 50 ГБ за
// 160 ₽; у покупателя 10 000 ₽ на балансе.
func trafficDealApp(t *testing.T) (*App, *fakeStore, *trafficPanel, *model.Plan) {
	t.Helper()
	p := newTrafficPanel(t)
	a, fs := snapApp(t, p.srv.URL)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, trafficUID)
	_ = fs.AddBalance(ctx, trafficUID, 1000000)
	plan := &model.Plan{
		Code: "trf150", Name: "Трафик 150", Enabled: true, Kind: model.PlanKindTraffic,
		TrafficGB: 150, DeviceLimit: 3, Currency: "₽", Availability: model.PlanAvailAll,
		Traffic: &model.PlanTraffic{Price: "450", Stars: 300, Packs: []model.PlanPack{{ID: "pk5000", GB: 50, Price: "160", Stars: 110}}},
	}
	if err := fs.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	got, _ := fs.GetPlan(ctx, plan.Code)
	return a, fs, p, got
}

// buyTrafficWithBalance — выбор и оплата с баланса в чате. Время выбора у
// каждой покупки своё (выбор пишется с точностью до секунды, а человек между
// двумя покупками заново выбирает пакет).
func buyTrafficWithBalance(t *testing.T, a *App, code, packID string) {
	t.Helper()
	buyTrafficAs(t, a, trafficUID, code, packID)
}

func buyTrafficAs(t *testing.T, a *App, uid int64, code, packID string) {
	t.Helper()
	ctx := context.Background()
	buySeq++
	if err := a.storeRef().SetPurchaseIntent(ctx, &model.PurchaseIntent{
		TelegramID: uid, PlanCode: code, PackID: packID,
		CreatedAt: time.Now().UTC().Add(-time.Duration(buySeq) * time.Second).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	a.payFromBalance(ctx, uid)
}

var buySeq int

func TestTrafficDeal_BuyPlanThenPacks(t *testing.T) {
	a, fs, panel, _ := trafficDealApp(t)
	ctx := context.Background()

	buyTrafficWithBalance(t, a, "trf150", "")
	exp, limit, _, strat := panel.state()
	if exp != remnawave.EternalExpire || limit != 150*gib || strat != "NO_RESET" {
		t.Fatalf("выдача трафик-тарифа: срок %s, лимит %d, сброс %s", exp, limit, strat)
	}
	u, _ := fs.GetUser(ctx, trafficUID)
	if u.Balance != 1000000-45000 || !u.Snapshot.IsTraffic() || u.Snapshot.Months != 0 || u.Snapshot.BoughtDays != 0 || u.SubExpireAt != remnawave.EternalExpire {
		t.Fatalf("после покупки: баланс %d, снимок %+v, срок %s", u.Balance, u.Snapshot, u.SubExpireAt)
	}
	if last, _ := fs.LastPaidSubPayment(ctx, trafficUID); last == nil || last.Months != trafficPaymentMonths || !last.Snapshot.IsTraffic() {
		t.Fatalf("платёж трафик-тарифа не виден как покупка подписки: %+v", last)
	}

	// Пакет: лимит += 50 ГБ, расход не трогается, срок и снимок прежние.
	panel.setUsed(140 * gib)
	resetsBefore := panel.resets
	buyTrafficWithBalance(t, a, "trf150", "pk5000")
	exp, limit, used, _ := panel.state()
	if limit != 200*gib || used != 140*gib || exp != remnawave.EternalExpire || panel.resets != resetsBefore {
		t.Fatalf("пакет: лимит %d, расход %d, срок %s, сбросов %d→%d", limit, used, exp, resetsBefore, panel.resets)
	}
	u2, _ := fs.GetUser(ctx, trafficUID)
	if u2.Snapshot.PackID != "" || u2.Snapshot.Fingerprint() != u.Snapshot.Fingerprint() {
		t.Fatalf("пакет переписал снимок пользователя: %+v", u2.Snapshot)
	}
	if u2.Balance != u.Balance-16000 {
		t.Fatalf("за пакет списано %d", u.Balance-u2.Balance)
	}
	if last, _ := fs.LastPaidSubPayment(ctx, trafficUID); last == nil || last.Snapshot.PackID != "" {
		t.Fatalf("пакет стал «последней покупкой подписки»: %+v", last)
	}

	// Второй пакет подряд — отдельная покупка (бессрочный срок не меняется,
	// и ключ сделки не может держаться на нём).
	buyTrafficWithBalance(t, a, "trf150", "pk5000")
	if _, limit, _, _ := panel.state(); limit != 250*gib {
		t.Fatalf("второй пакет подряд не выдан: лимит %d", limit)
	}
}

// Повтор доставки той же оплаты пакета не прибавляет второй раз — даже если
// первая доставка потеряла ответ панели и платёж не записан.
func TestTrafficDeal_PackRetryNotDoubled(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	buyTrafficWithBalance(t, a, "trf150", "")
	snap := a.trafficSnapshotOf(plan, plan.Pack("pk5000"))

	if _, err := a.closeTrafficDeal(ctx, trafficUID, model.PayMethodYooKassa, "160 ₽", "yk-pack-1", snap, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.closeTrafficDeal(ctx, trafficUID, model.PayMethodYooKassa, "160 ₽", "yk-pack-1", snap, false); err == nil {
		t.Fatal("повтор той же оплаты должен опознаться как дубль")
	}
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("повтор прибавил второй раз: %d", limit)
	}

	// Ответ панели потерян: прибавка применена, платёж не записан. Повтор по
	// журналу видит, что целевой потолок уже стоит.
	snap2 := *snap
	a.payLog(ctx, model.PayMethodYooKassa, "yk-pack-2", trafficUID, packTargetStage, "+50 GB: limit=%d", 250*gib)
	panel.mu.Lock()
	panel.limit = 250 * gib
	panel.mu.Unlock()
	if _, err := a.closeTrafficDeal(ctx, trafficUID, model.PayMethodYooKassa, "160 ₽", "yk-pack-2", &snap2, false); err != nil {
		t.Fatal(err)
	}
	if _, limit, _, _ := panel.state(); limit != 250*gib {
		t.Fatalf("повтор после потерянного ответа прибавил ещё раз: %d", limit)
	}
	if done, _ := fs.PaymentByExtID(ctx, "yk-pack-2"); !done {
		t.Fatal("платёж повтора не записан")
	}
}

// Прошлая доставка записала целевой потолок, но панель его не применила.
// Нынешний потолок совпадает с началом записанного числа (1 ГБ и 10 ГБ в
// байтах) — это не «уже применено», пакет прибавляется.
func TestTrafficDeal_PackTargetExactMatch(t *testing.T) {
	a, _, _, _ := trafficDealApp(t)
	ctx := context.Background()
	a.payLog(ctx, model.PayMethodYooKassa, "yk-pack-9", trafficUID, packTargetStage, "+9 GB: limit=%d", 10*gib)
	if done, _ := a.packPast(ctx, "yk-pack-9", 1*gib); done {
		t.Fatal("1 ГБ принят за применённый потолок 10 ГБ")
	}
	if done, _ := a.packPast(ctx, "yk-pack-9", 10*gib); !done {
		t.Fatal("применённый потолок не опознан")
	}
}

// Оплата пакета пришла, а тарифа у человека уже нет: стоимость пакета на
// баланс, панель не трогается.
func TestTrafficDeal_PackRefundWhenNotOwner(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	buyTrafficWithBalance(t, a, "trf150", "")
	snap := a.trafficSnapshotOf(plan, plan.Pack("pk5000"))
	_ = fs.DeletePlan(ctx, plan.Code)
	before, _ := fs.GetUser(ctx, trafficUID)

	d, err := a.closeTrafficDeal(ctx, trafficUID, model.PayMethodYooKassa, "160 ₽", "yk-pack-r", snap, false)
	if err != nil || !d.refunded {
		t.Fatalf("возврат не проведён: %+v %v", d, err)
	}
	after, _ := fs.GetUser(ctx, trafficUID)
	if after.Balance != before.Balance+16000 {
		t.Fatalf("на баланс зачислено %d", after.Balance-before.Balance)
	}
	if _, limit, _, _ := panel.state(); limit != 150*gib {
		t.Fatalf("пакет добавлен к чужому тарифу: %d", limit)
	}
	// Повтор доставки — не второй возврат.
	_, _ = a.closeTrafficDeal(ctx, trafficUID, model.PayMethodYooKassa, "160 ₽", "yk-pack-r", snap, false)
	if again, _ := fs.GetUser(ctx, trafficUID); again.Balance != after.Balance {
		t.Fatal("возврат пакета на баланс повторился")
	}
	// Пакет из намерения тоже не продаётся: тарифа нет.
	_ = a.setBuyIntentTraffic(ctx, trafficUID, plan.Code, "pk5000", "")
	if s, _ := a.saleFor(ctx, trafficUID); s != nil {
		t.Fatal("пакет удалённого тарифа продаётся")
	}
}

// Ответ панели на прибавку пакета потерян, а прибавка применена: пакет выдан
// сразу, и сверке нечего повторять — даже если до неё лимит успеет измениться.
func TestTrafficDeal_PackLostReply(t *testing.T) {
	a, fs, panel, _ := trafficDealApp(t)
	ctx := context.Background()
	buyTrafficWithBalance(t, a, "trf150", "")
	before, _ := fs.GetUser(ctx, trafficUID)
	panel.mu.Lock()
	panel.loseReply = true
	panel.mu.Unlock()
	buyTrafficWithBalance(t, a, "trf150", "pk5000")
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("лимит после пакета: %d", limit)
	}
	after, _ := fs.GetUser(ctx, trafficUID)
	if after.Balance != before.Balance-16000 {
		t.Fatalf("пакет не проведён: списано %d", before.Balance-after.Balance)
	}
}

// Пакет чужого трафик-тарифа владельцу другого тарифа не прибавляется.
func TestTrafficDeal_PackOfOtherPlanRefunded(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	buyTrafficWithBalance(t, a, "trf150", "")
	other := *plan
	other.Code = "trf300"
	if err := fs.SavePlan(ctx, &other); err != nil {
		t.Fatal(err)
	}
	got, _ := fs.GetPlan(ctx, other.Code)
	d, err := a.closeTrafficDeal(ctx, trafficUID, model.PayMethodYooKassa, "160 ₽", "yk-pack-o", a.trafficSnapshotOf(got, got.Pack("pk5000")), false)
	if err != nil || !d.refunded {
		t.Fatalf("пакет чужого тарифа принят: %+v %v", d, err)
	}
	if _, limit, _, _ := panel.state(); limit != 150*gib {
		t.Fatalf("лимит изменился: %d", limit)
	}
}

// Со срочного на трафик и обратно: ничего не переносится, срочный тариф после
// бессрочного считается от сегодня.
func TestTrafficDeal_SwitchesCarryNothing(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	term := &model.Plan{Code: "term01", Name: "Месяц", Enabled: true, TrafficGB: 500, Currency: "₽",
		Strategy: "MONTH", Durations: []model.PlanDuration{{Months: 1, Base: "300"}}}
	_ = fs.SavePlan(ctx, term)
	term, _ = fs.GetPlan(ctx, term.Code)

	// Срочный куплен полгода назад на 12 месяцев (большой остаток).
	if _, _, err := a.finalizePurchase(ctx, trafficUID, 1, "balance", "300 ₽", "", a.planSnapshotOf(term, term.Duration(1), 1)); err != nil {
		t.Fatal(err)
	}
	panel.mu.Lock()
	panel.expire = time.Now().UTC().AddDate(0, 11, 0).Format(time.RFC3339)
	panel.mu.Unlock()

	if _, err := a.closeTrafficDeal(ctx, trafficUID, "balance", "450 ₽", "bal-trf-1", a.trafficSnapshotOf(plan, nil), false); err != nil {
		t.Fatal(err)
	}
	u, _ := fs.GetUser(ctx, trafficUID)
	if u.Snapshot.BoughtDays != 0 || u.Snapshot.WindowPaidK != 0 || u.Snapshot.SwitchDays != 0 {
		t.Fatalf("окно срочного перенесено в трафик-сделку: %+v", u.Snapshot)
	}

	// Обратно на срочный: срок — месяц от сегодня, без зачёта.
	if _, _, err := a.finalizePurchase(ctx, trafficUID, 1, "balance", "300 ₽", "bal-term-2", a.planSnapshotOf(term, term.Duration(1), 1)); err != nil {
		t.Fatal(err)
	}
	exp, _, _, strat := panel.state()
	e, _ := time.Parse(time.RFC3339, exp)
	if d := time.Until(e).Hours() / 24; d < 27 || d > 32 || strat != "MONTH" {
		t.Fatalf("срочный после трафика: до конца %.1f дн., сброс %s", d, strat)
	}
	u, _ = fs.GetUser(ctx, trafficUID)
	if u.Snapshot.SwitchDays != 0 {
		t.Fatalf("зачёт при переходе с трафика: %+v", u.Snapshot)
	}
}

// Покупка трафик-тарифа выключает автопродление и ставит доп-подписке свой
// период сброса.
func TestTrafficDeal_AutoPayOff(t *testing.T) {
	a, fs, _, plan := trafficDealApp(t)
	ctx := context.Background()
	_ = fs.SetAutoPay(ctx, &model.AutoPay{TelegramID: trafficUID, Method: model.PayMethodYooKassa, MethodID: "pm-1", Enabled: true, Months: 1})
	if _, err := a.closeTrafficDeal(ctx, trafficUID, "balance", "450 ₽", "bal-trf-ap", a.trafficSnapshotOf(plan, nil), false); err != nil {
		t.Fatal(err)
	}
	ap, _ := fs.GetAutoPay(ctx, trafficUID)
	if ap == nil || ap.Enabled || ap.MethodID != "pm-1" {
		t.Fatalf("автопродление после трафик-тарифа: %+v", ap)
	}
	if !strings.Contains(a.msg.(*fakeMsg).joined(), "Автопродление выключено") {
		t.Fatal("человеку не сказали, что автопродление выключено")
	}
}

// Тариф продаётся по обычным правилам; пакет — только владельцу, при любом
// режиме доступности и у выключенного тарифа.
func TestTrafficDeal_SaleRules(t *testing.T) {
	a, fs, _, plan := trafficDealApp(t)
	ctx := context.Background()
	sale := func(packID string) *sale {
		t.Helper()
		_ = a.setBuyIntentTraffic(ctx, trafficUID, plan.Code, packID, "")
		s, err := a.saleFor(ctx, trafficUID)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := sale(""); s == nil || !s.traffic() || s.Pack != nil || a.saleBase(s) != "450" || a.saleStars(s) != 300 {
		t.Fatalf("продажа тарифа: %+v", s)
	}
	if s := sale("pk5000"); s != nil {
		t.Fatal("пакет продан не владельцу")
	}
	buyTrafficWithBalance(t, a, plan.Code, "")

	// Выключенный тариф «только по списку», владельца в списке нет: сам
	// тариф не продаётся, пакеты — да.
	p, _ := fs.GetPlan(ctx, plan.Code)
	p.Enabled = false
	p.Availability = model.PlanAvailList
	_ = fs.SavePlan(ctx, p)
	if s := sale(""); s != nil {
		t.Fatal("выключенный тариф продаётся")
	}
	s := sale("pk5000")
	if s == nil || s.Pack == nil || a.saleBase(s) != "160" || a.saleStars(s) != 110 || a.saleFiat(s, model.PayMethodP2P) != "160" {
		t.Fatalf("пакет владельцу: %+v", s)
	}
	snap := a.saleSnapshot(s)
	if !snap.IsTraffic() || snap.PackGB != 50 || snap.Price != "160" || snap.Months != 0 {
		t.Fatalf("снимок пакета: %+v", snap)
	}
	if s := sale("nope00"); s != nil {
		t.Fatal("несуществующий пакет продаётся")
	}
}

// Срок 2099, выставленный админом руками (не трафик-тариф): покупка месяца
// продлевает его от конца, как до появления трафик-тарифов.
func TestManualEternalKeepsExtending(t *testing.T) {
	a, fs, panel, _ := trafficDealApp(t)
	ctx := context.Background()
	term := &model.Plan{Code: "term01", Name: "Месяц", Enabled: true, TrafficGB: 500, Currency: "₽",
		Strategy: "MONTH", Durations: []model.PlanDuration{{Months: 1, Base: "300"}}}
	_ = fs.SavePlan(ctx, term)
	term, _ = fs.GetPlan(ctx, term.Code)
	if _, _, err := a.finalizePurchase(ctx, trafficUID, 1, "balance", "300 ₽", "", a.planSnapshotOf(term, term.Duration(1), 1)); err != nil {
		t.Fatal(err)
	}
	panel.mu.Lock()
	panel.expire = "2099-12-31T00:00:00Z"
	panel.mu.Unlock()
	if _, _, err := a.finalizePurchase(ctx, trafficUID, 1, "balance", "300 ₽", "bal-term-9", a.planSnapshotOf(term, term.Duration(1), 1)); err != nil {
		t.Fatal(err)
	}
	if exp, _, _, _ := panel.state(); exp != "2100-01-31T00:00:00Z" {
		t.Fatalf("ручной бессрочный срок: %s", exp)
	}
	if ok, _ := a.addReferralDays(ctx, trafficUID, 7); !ok {
		t.Fatal("бонусные дни ручному бессрочному сроку не начислены")
	}
}

func TestTrafficDeal_BreachNoticeHasNoMonths(t *testing.T) {
	a, fs, _, plan := trafficDealApp(t)
	ctx := context.Background()
	snap := a.trafficSnapshotOf(plan, nil)
	plan.Enabled = false
	_ = fs.SavePlan(ctx, plan)
	if _, err := a.closeTrafficDeal(ctx, trafficUID, model.PayMethodYooKassa, "450 RUB", "pay-breach", snap, false); err != nil {
		t.Fatal(err)
	}
	fm := a.msg.(*fakeMsg)
	fm.mu.Lock()
	defer fm.mu.Unlock()
	for _, txt := range fm.texts {
		if strings.Contains(txt, "недоступен покупателю") {
			if strings.Contains(txt, "мес") || !strings.Contains(txt, "трафик-тариф") {
				t.Fatalf("уведомление о пробое: %q", txt)
			}
			return
		}
	}
	t.Fatal("нет уведомления о пробое гейта")
}

func TestTrafficStars_ChangedTermsText(t *testing.T) {
	a, fs, _, plan := trafficDealApp(t)
	ctx := context.Background()
	payload := trafficStarsPayload(plan.Code, "", trafficUID)
	plan.Traffic.Stars = 999
	_ = fs.SavePlan(ctx, plan)
	a.trafficStarsPaid(ctx, trafficUID, payload, "charge-1", 300, false)
	fm := a.msg.(*fakeMsg)
	fm.mu.Lock()
	defer fm.mu.Unlock()
	var admin, client bool
	for _, txt := range fm.texts {
		admin = admin || strings.Contains(txt, "изменился или удалён")
		client = client || strings.Contains(txt, "вернёт звёзды")
		if strings.Contains(txt, "срок подписки") {
			t.Fatalf("текст про срок у трафика: %q", txt)
		}
	}
	if !admin || !client {
		t.Fatalf("тексты: %q", fm.texts)
	}
}

// Панель отказала на первой оплате пакета, а до её повтора потолок дошёл до
// того же числа другой покупкой: повтор прибавляет свой пакет, а не считает
// его уже выданным.
func TestTrafficDeal_PackRefusedThenSamePack(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	buyTrafficWithBalance(t, a, "trf150", "")
	snap := a.trafficSnapshotOf(plan, plan.Pack("pk5000"))

	panel.mu.Lock()
	panel.refuse = true
	panel.mu.Unlock()
	if _, err := a.closeTrafficDeal(ctx, trafficUID, model.PayMethodYooKassa, "160 ₽", "yk-pack-a", snap, false); err == nil {
		t.Fatal("отказ панели принят за выдачу")
	}
	buyTrafficWithBalance(t, a, "trf150", "pk5000")
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("второй пакет не выдан: %d", limit)
	}

	again := *snap
	if _, err := a.closeTrafficDeal(ctx, trafficUID, model.PayMethodYooKassa, "160 ₽", "yk-pack-a", &again, false); err != nil {
		t.Fatal(err)
	}
	if _, limit, _, _ := panel.state(); limit != 250*gib {
		t.Fatalf("оплачено два пакета, лимит %d ГБ", limit/gib)
	}
	if done, _ := fs.PaymentByExtID(ctx, "yk-pack-a"); !done {
		t.Fatal("платёж повтора не записан")
	}
}

// Панель не ответила ни на запись пакета, ни на проверку. Повтор решает по
// нынешнему потолку — прибавляет, если записи не было, и не прибавляет, если
// она прошла, — а администратор получает просьбу сверить лимит.
func TestTrafficDeal_PackUnsure(t *testing.T) {
	for _, applied := range []bool{false, true} {
		a, fs, panel, plan := trafficDealApp(t)
		ctx := context.Background()
		fm := a.msg.(*fakeMsg)
		buyTrafficWithBalance(t, a, "trf150", "")
		snap := a.trafficSnapshotOf(plan, plan.Pack("pk5000"))

		panel.mu.Lock()
		panel.blindAfterWrite = true
		panel.refuse, panel.loseReply = !applied, applied
		panel.mu.Unlock()
		if _, err := a.closeTrafficDeal(ctx, trafficUID, model.PayMethodYooKassa, "160 ₽", "yk-unsure", snap, false); err == nil {
			t.Fatalf("applied=%v: неизвестный исход принят за выдачу", applied)
		}
		if done, _ := fs.PaymentByExtID(ctx, "yk-unsure"); done {
			t.Fatalf("applied=%v: платёж записан при неизвестном исходе", applied)
		}

		again := *snap
		if _, err := a.closeTrafficDeal(ctx, trafficUID, model.PayMethodYooKassa, "160 ₽", "yk-unsure", &again, false); err != nil {
			t.Fatal(err)
		}
		if _, limit, _, _ := panel.state(); limit != 200*gib {
			t.Fatalf("applied=%v: лимит после повтора %d ГБ", applied, limit/gib)
		}
		if n := strings.Count(fm.joined(), "Сверьте лимит трафика"); n != 1 {
			t.Fatalf("applied=%v: администратора о неизвестном исходе позвали %d раз", applied, n)
		}
	}
}

// Пакет только за звёзды на баланс не вернуть: администратор получает одно
// сообщение и кнопку возврата звёзд, покупатель — своё, запись остаётся
// отказом, сверка звёзд к этой оплате не возвращается.
func TestTrafficStars_PackManualOnce(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	fm := a.msg.(*fakeMsg)
	a.botCfg.Stars.Enabled = true
	buyTrafficWithBalance(t, a, plan.Code, "")
	plan.Traffic.Packs[0].Price = ""
	if err := fs.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	panel.mu.Lock()
	panel.limit = 0
	panel.mu.Unlock()

	tx := models.StarTransaction{ID: "ch-manual", Amount: 110, Date: int(time.Now().Unix()),
		Source: &models.TransactionPartner{Type: models.TransactionPartnerTypeUser, User: &models.TransactionPartnerUser{
			User: models.User{ID: trafficUID}, InvoicePayload: trafficStarsPayload(plan.Code, "pk5000", trafficUID)}}}
	for i := 0; i < 3; i++ {
		a.reconcileStarTx(ctx, &tx)
	}
	all := fm.joined()
	for _, want := range []string{"вернуть деньги на баланс бот не смог", "Можно вернуть звёзды", "пакет трафика добавить не удалось"} {
		if n := strings.Count(all, want); n != 1 {
			t.Fatalf("«%s» отправлено %d раз", want, n)
		}
	}
	pays, _, _ := fs.ListUserPayments(ctx, trafficUID, 50, 0)
	var rec *model.Payment
	for i := range pays {
		if pays[i].ExtID == "ch-manual" {
			rec = &pays[i]
		}
	}
	if rec == nil || rec.Status != model.PaymentRejected || payKindOf(rec) == payKindTopUp {
		t.Fatalf("невыданный пакет записан не отказом: %+v", rec)
	}
	rec.Status = model.PaymentRefunded
	if payKindOf(rec) == payKindTopUp || !payIsPack(rec) {
		t.Fatal("после возврата звёзд невыданный пакет показан пополнением")
	}
	logs, _ := fs.PayLogs(ctx, "ch-manual", 0, 0)
	n := 0
	for i := range logs {
		if logs[i].Stage == "reconcile" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("строк сверки в журнале: %d", n)
	}

	// Оплата, не сошедшаяся с условиями, строк «найдена сверкой» не копит.
	odd := tx
	odd.ID, odd.Amount = "ch-odd", 111
	for i := 0; i < 3; i++ {
		a.reconcileStarTx(ctx, &odd)
	}
	logs, _ = fs.PayLogs(ctx, "ch-odd", 0, 0)
	for i := range logs {
		if logs[i].Stage == "reconcile" {
			t.Fatal("несошедшаяся оплата записана как найденная сверкой")
		}
	}
}

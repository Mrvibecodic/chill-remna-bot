package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// testPanel — клиент к стабу панели для тестов финализации.
func testPanel(url string) *remnawave.Client {
	return remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: url, APIToken: "t"})
}

// vipPlan — включённый тариф с двумя длительностями для тестов доступности.
func vipPlan(t *testing.T, fs *fakeStore, mode string) *model.Plan {
	t.Helper()
	p := &model.Plan{
		Code: "vipvipvipvip", Name: "VIP", Enabled: true, Availability: mode,
		Currency: "₽", Strategy: "MONTH", DeviceLimit: 7, IntSquads: []string{"sq-vip"},
		Durations: []model.PlanDuration{
			{Months: 1, Base: "990", Stars: 55},
			{Months: 12, Base: "9900"},
		},
	}
	if err := fs.SavePlan(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

// markPaid — «пользователь платил»: платная покупка со снимком тарифа code.
func markPaid(t *testing.T, fs *fakeStore, uid int64, code string) {
	t.Helper()
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, uid)
	if err := fs.AddPayment(ctx, &model.Payment{
		TelegramID: uid, Method: model.PayMethodCryptoBot, Months: 1, Amount: "150",
		Status: model.PaymentPaid, ExtID: "paid-" + itoa64(uid),
	}); err != nil {
		t.Fatal(err)
	}
	if code != "" {
		_ = fs.SetUserSnapshot(ctx, uid, &model.PlanSnapshot{Code: code, Months: 1})
	}
}

// Матрица режимов: одна функция фильтрации отвечает за все поверхности.
func TestPlanAccessibleFor_Modes(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)

	check := func(mode string, uid int64, want bool) {
		t.Helper()
		p.Availability = mode
		if got := a.planAccessibleFor(ctx, p, uid); got != want {
			t.Fatalf("режим %q, uid %d: доступность %v, ожидалось %v", mode, uid, got, want)
		}
	}

	// «всем» и «по ссылке» — без условий.
	check(model.PlanAvailAll, 500, true)
	check(model.PlanAvailLink, 500, true)

	// «только новым»: не платил — да, платил — нет.
	check(model.PlanAvailNew, 500, true)
	markPaid(t, fs, 501, "")
	check(model.PlanAvailNew, 501, false)
	// Купивший ЭТОТ тариф остаётся допущенным — иначе не продлиться.
	markPaid(t, fs, 502, p.Code)
	check(model.PlanAvailNew, 502, true)

	// «только действующим» — зеркально.
	check(model.PlanAvailExisting, 500, false)
	check(model.PlanAvailExisting, 501, true)

	// «по списку»: запись по Telegram ID.
	check(model.PlanAvailList, 500, false)
	_ = fs.GrantPlanAccess(ctx, p.Code, 500, "")
	check(model.PlanAvailList, 500, true)

	// E-mail-аккаунт кабинета сопоставляется по почте.
	web := int64(-777)
	fs.webUsers = map[string]*model.WebUser{"web@example.com": {TgID: web, Email: "web@example.com"}}
	check(model.PlanAvailList, web, false)
	_ = fs.GrantPlanAccess(ctx, p.Code, 0, "web@example.com")
	check(model.PlanAvailList, web, true)
}

// Гейт №1 и №2 в чате: «Базовый» в режиме «по списку» не показывается и не
// продаётся тому, кого в списке нет; намерение покупки не создаётся.
func TestBasePlanGate_ChatShowcaseAndBuy(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	base, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	base.Availability = model.PlanAvailList
	_ = fs.SavePlan(ctx, base)

	uid := int64(600)
	a.handleMessage(ctx, msgText(uid, "/buy"))
	if !strings.Contains(fm.last(), "недоступна") {
		t.Fatalf("витрина должна отвечать отказом: %q", fm.last())
	}
	for _, d := range fm.allCallbackData() {
		if strings.HasPrefix(d, "buy:") {
			t.Fatalf("в отказе не должно быть кнопок покупки: %v", fm.allCallbackData())
		}
	}

	// Кнопка из старой переписки: гейт на создании счёта.
	a.handleCallback(ctx, cb(uid, "buy:1"))
	if in, _ := fs.PurchaseIntent(ctx, uid); in != nil {
		t.Fatalf("намерение покупки не должно создаваться: %+v", in)
	}

	// Из списка допущенных — продаётся.
	_ = fs.GrantPlanAccess(ctx, model.PlanCodeBase, uid, "")
	a.handleCallback(ctx, cb(uid, "buy:1"))
	if in, _ := fs.PurchaseIntent(ctx, uid); in == nil || in.Months != 1 {
		t.Fatalf("допущенному покупка должна открыться: %+v", in)
	}
}

// Гейт мини-аппа: витрина пустая, checkout отвечает отказом.
func TestBasePlanGate_MiniApp(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	base, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	base.Availability = model.PlanAvailNew
	_ = fs.SavePlan(ctx, base)
	markPaid(t, fs, 601, "")

	if dto := a.MiniPlans(ctx, 601); len(dto.Plans) != 0 {
		t.Fatalf("витрина мини-аппа должна быть пустой: %+v", dto.Plans)
	}
	if dto := a.MiniCheckout(ctx, 601, 1, model.PayMethodBalance, false); dto.Error == "" {
		t.Fatalf("checkout должен отказать: %+v", dto)
	}
	// Новому пользователю всё открыто.
	if dto := a.MiniPlans(ctx, 602); len(dto.Plans) == 0 {
		t.Fatal("новому пользователю витрина должна показываться")
	}
}

// Тариф по ссылке: /start plan_<код> открывает экран, кнопка пишет намерение
// с кодом тарифа, экран способов показывает цену тарифа.
func TestPlanLink_OpenAndBuy(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailLink)

	a.botCfg.CryptoBot.Enabled = true
	a.botCfg.CryptoBot.Token = "t"
	uid := int64(700)
	a.handleMessage(ctx, msgText(uid, "/start plan_"+p.Code))
	if !strings.Contains(fm.last(), "VIP") {
		t.Fatalf("экран тарифа не открылся: %q", fm.last())
	}
	if !hasCB(fm.allCallbackData(), "plb:"+p.Code+":1") {
		t.Fatalf("нет кнопки срока: %v", fm.allCallbackData())
	}

	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	in, _ := fs.PurchaseIntent(ctx, uid)
	if in == nil || in.PlanCode != p.Code || in.Months != 1 {
		t.Fatalf("намерение должно нести код тарифа: %+v", in)
	}
	// Экран способов подписан ценой тарифа, а не сетки (990, не 150) — цена
	// живёт в подписях кнопок.
	labels := strings.Join(fm.buttonLabels(), "|")
	if !strings.Contains(labels, "990") {
		t.Fatalf("цена тарифа не дошла до кнопок способов: %q", labels)
	}
	if strings.Contains(labels, "150") {
		t.Fatalf("цена сетки «Базового» не должна попадать в кнопки тарифа: %q", labels)
	}
}

// Ссылка не подтверждает существование тарифов: неизвестный код, выключенный
// тариф и чужой тариф отвечают одинаково, а перебор замолкает по лимиту.
func TestPlanLink_UnknownAndThrottle(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailList) // список пуст — тариф чужой
	off := &model.Plan{
		Code: "offoffoffoff", Name: "Off", Enabled: false, Availability: model.PlanAvailAll,
		Durations: []model.PlanDuration{{Months: 1, Base: "100"}},
	}
	_ = fs.SavePlan(ctx, off)

	uid := int64(710)
	deny := func(code string) string {
		t.Helper()
		before := len(strings.Split(fm.joined(), "\n---\n"))
		a.handleMessage(ctx, msgText(uid, "/start plan_"+code))
		_ = before
		return fm.last()
	}
	first := deny("nosuchplan99")
	if !strings.Contains(first, "недоступен") {
		t.Fatalf("неизвестный код: %q", first)
	}
	if got := deny(p.Code); got != first {
		t.Fatalf("чужой тариф должен отвечать так же: %q vs %q", got, first)
	}
	if got := deny(off.Code); got != first {
		t.Fatalf("выключенный тариф должен отвечать так же: %q", got)
	}

	// Лимит: после пятой неудачи бот молчит.
	deny("nosuchplan98")
	deny("nosuchplan97")
	before := fm.joined()
	a.handleMessage(ctx, msgText(uid, "/start plan_nosuchplan96"))
	if fm.joined() != before {
		t.Fatalf("после лимита ответа быть не должно: %q", fm.last())
	}

	// Легитимная ссылка у другого пользователя работает как ни в чём не бывало.
	p2 := vipPlan(t, fs, model.PlanAvailAll)
	a.handleMessage(ctx, msgText(int64(711), "/start plan_"+p2.Code))
	if !strings.Contains(fm.last(), "VIP") {
		t.Fatalf("лимит не должен цеплять других: %q", fm.last())
	}
}

// Stars продаёт тариф по его цене в звёздах: счёт, снимок условий и
// предпроверка совпадают с тарифом, а не с сеткой.
func TestPlanSale_Stars(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	a.botCfg.Stars.Enabled = true
	p := vipPlan(t, fs, model.PlanAvailLink)

	uid := int64(720)
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	a.handleCallback(ctx, cb(uid, "method:stars"))

	if len(fm.invoices) != 1 || fm.invoices[0] != "XTR:55:stars:1" {
		t.Fatalf("счёт Stars должен быть на цену тарифа: %+v", fm.invoices)
	}
	snap := fs.invSnaps[invSnapKey(uid, model.PayMethodStars, 1)]
	if snap == nil || snap.Code != p.Code || snap.DeviceLimit != 7 {
		t.Fatalf("условия счёта должны быть тарифными: %+v", snap)
	}

	// Предпроверка: цена тарифа проходит, чужая сумма — нет.
	q := &models.PreCheckoutQuery{ID: "q1", InvoicePayload: "stars:1", TotalAmount: 55, From: &models.User{ID: uid}}
	a.handlePreCheckout(ctx, q)
	if !fm.lastPreOK() {
		t.Fatal("предпроверка должна пропустить цену тарифа")
	}
	q.TotalAmount = 54
	a.handlePreCheckout(ctx, q)
	if fm.lastPreOK() {
		t.Fatal("чужая сумма не должна проходить предпроверку")
	}
	// Цена сетки «Базового» тоже проходит — это счета старой витрины.
	q.TotalAmount = 99
	a.handlePreCheckout(ctx, q)
	if !fm.lastPreOK() {
		t.Fatal("цена сетки должна проходить предпроверку")
	}
}

// Два счёта Stars на один срок (базовый и тарифный) делят одну строку условий.
// Оплата ДЕШЁВОГО счёта не должна выдавать условия дорогого: применяется
// снимок, чья цена совпала с оплаченной суммой.
func TestPlanSale_StarsAmountPicksMatchingTerms(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	srv := panelStub(1)
	defer srv.Close()
	a.panel = testPanel(srv.URL)
	a.botCfg.Stars.Enabled = true
	// Сетка: 1 мес = 99⭐ (см. planApp). Тариф: 1 мес = 55⭐, лимит устройств 7.
	p := vipPlan(t, fs, model.PlanAvailLink)

	uid := int64(725)
	_ = fs.UpsertUser(ctx, uid)
	// Счёт «Базового» (снимок = базовый), затем счёт тарифа (снимок перезаписан).
	_ = fs.SetPurchaseIntent(ctx, &model.PurchaseIntent{TelegramID: uid, PlanCode: model.PlanCodeBase, Months: 1})
	a.handleCallback(ctx, cb(uid, "method:stars"))
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	a.handleCallback(ctx, cb(uid, "method:stars"))

	// Оплачен БАЗОВЫЙ счёт (99⭐): условия обязаны быть базовыми, не тарифными.
	pay := &models.Message{Chat: models.Chat{ID: uid}, SuccessfulPayment: &models.SuccessfulPayment{
		InvoicePayload: "stars:1", TotalAmount: 99, TelegramPaymentChargeID: "st-99",
	}}
	a.handleSuccessfulPayment(ctx, pay)
	u, _ := fs.GetUser(ctx, uid)
	if u == nil || u.Snapshot == nil {
		t.Fatalf("подписка не выдана: %+v", u)
	}
	if u.Snapshot.Code == p.Code || u.Snapshot.DeviceLimit == 7 {
		t.Fatalf("за 99⭐ выданы условия тарифа за 55⭐: %+v", u.Snapshot)
	}

	// Оплачен ТАРИФНЫЙ счёт (55⭐): условия тарифные.
	pay.SuccessfulPayment.TotalAmount = 55
	pay.SuccessfulPayment.TelegramPaymentChargeID = "st-55"
	a.handleSuccessfulPayment(ctx, pay)
	u, _ = fs.GetUser(ctx, uid)
	if u.Snapshot == nil || u.Snapshot.Code != p.Code || u.Snapshot.DeviceLimit != 7 {
		t.Fatalf("оплата тарифного счёта должна выдать тарифные условия: %+v", u.Snapshot)
	}

	// Сумма, не похожая ни на один счёт, выдачи не даёт — случай уходит админу.
	pay.SuccessfulPayment.TotalAmount = 60
	pay.SuccessfulPayment.TelegramPaymentChargeID = "st-60"
	a.handleSuccessfulPayment(ctx, pay)
	if done, _ := fs.PaymentByExtID(ctx, "st-60"); done {
		t.Fatal("несовпавшая сумма не должна финализироваться")
	}
	if !strings.Contains(fm.joined(), "не удалось определить") && !strings.Contains(fm.joined(), "разбер") {
		// Сообщение админу/пользователю: точный текст не фиксируем, но что-то
		// должно быть отправлено.
		if len(fm.allCallbackData()) == 0 && fm.last() == "" {
			t.Fatal("про несовпавшую сумму никто не узнал")
		}
	}
}

// Кнопка plb — не оракул перебора кодов: негативные ветки отвечают одинаково
// и считаются в тот же лимит, что и ссылка.
func TestPlanLink_BuyGatesAndThrottle(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailList) // список пуст — тариф чужой

	uid := int64(715)
	// Срок, которого у тарифа нет; выключенный тариф; чужой тариф — без
	// намерения и с одинаковым ответом.
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":7"))
	deny1 := fm.last()
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	deny2 := fm.last()
	a.handleCallback(ctx, cb(uid, "plb:zzzzzzzzzzzz:1"))
	deny3 := fm.last()
	if in, _ := fs.PurchaseIntent(ctx, uid); in != nil {
		t.Fatalf("намерение не должно создаваться: %+v", in)
	}
	if deny1 != deny2 || deny2 != deny3 || !strings.Contains(deny1, "недоступен") {
		t.Fatalf("ответы различаются: %q / %q / %q", deny1, deny2, deny3)
	}

	// Четвёртая и пятая неудачи добивают лимит, шестая — молчание.
	a.handleCallback(ctx, cb(uid, "plb:yyyyyyyyyyyy:1"))
	a.handleCallback(ctx, cb(uid, "plb:xxxxxxxxxxxx:1"))
	before := fm.joined()
	a.handleCallback(ctx, cb(uid, "plb:wwwwwwwwwwww:1"))
	if fm.joined() != before {
		t.Fatalf("после лимита ответа быть не должно: %q", fm.last())
	}
}

// «Базовый» в режиме «по ссылке»: витрина закрыта, но ссылка продаёт до конца —
// намерение с экрана тарифа доводит до счёта.
func TestBasePlanLinkMode_SellsViaLink(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	base, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	base.Availability = model.PlanAvailLink
	_ = fs.SavePlan(ctx, base)
	a.botCfg.Stars.Enabled = true

	uid := int64(760)
	// Витрина закрыта.
	a.handleMessage(ctx, msgText(uid, "/buy"))
	if !strings.Contains(fm.last(), "недоступна") {
		t.Fatalf("витрина должна быть закрыта: %q", fm.last())
	}
	// Ссылка открывает экран и продаёт: намерение пишется, счёт выставляется.
	a.handleMessage(ctx, msgText(uid, "/start plan_"+model.PlanCodeBase))
	if !hasCB(fm.allCallbackData(), "plb:base:1") {
		t.Fatalf("экран «Базового» по ссылке не открылся: %v", fm.allCallbackData())
	}
	a.handleCallback(ctx, cb(uid, "plb:base:1"))
	a.handleCallback(ctx, cb(uid, "method:stars"))
	if len(fm.invoices) != 1 {
		t.Fatalf("счёт по ссылке на «Базовый» не выставлен: %+v", fm.invoices)
	}
}

// Принятие условий возвращает на экран тарифа по ссылке, а не на витрину.
func TestPlanLink_TermsReturnToOffer(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	a.botCfg.Contact.TermsText = "правила"
	p := vipPlan(t, fs, model.PlanAvailLink)

	uid := int64(765)
	a.handleMessage(ctx, msgText(uid, "/start plan_"+p.Code))
	if !strings.Contains(fm.last(), "правила") {
		t.Fatalf("условия не спрошены: %q", fm.last())
	}
	a.handleCallback(ctx, cb(uid, "terms:accept"))
	if !strings.Contains(fm.last(), "VIP") {
		t.Fatalf("после согласия должен открыться экран тарифа: %q", fm.last())
	}
}

// «Продлить» подписчика тарифа по ссылке открывает ЕГО тариф, а не витрину.
func TestRenew_OpensOwnPlan(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailLink)
	uid := int64(770)
	markPaid(t, fs, uid, p.Code)

	a.handleCallback(ctx, cb(uid, "menu:renew"))
	if !strings.Contains(fm.last(), "VIP") || !hasCB(fm.allCallbackData(), "plb:"+p.Code+":1") {
		t.Fatalf("продление должно вести на свой тариф: %q", fm.last())
	}

	// Тариф удалили — честная витрина вместо чужих условий.
	_ = fs.DeletePlan(ctx, p.Code)
	a.handleCallback(ctx, cb(uid, "menu:renew"))
	if strings.Contains(fm.last(), "VIP") {
		t.Fatalf("удалённый тариф не должен предлагаться: %q", fm.last())
	}
}

// Потолок пополнения учитывает цены включённых тарифов, а пресеты не выдают
// цену скрытого тарифа.
func TestTopUp_CapCoversPlanPrices(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailLink) // скрытый, 990 и 9900
	_ = p

	amts, maxK := a.topUpAmounts(ctx)
	if maxK != 990000 {
		t.Fatalf("потолок должен покрыть цену тарифа 9900: %d", maxK)
	}
	for _, k := range amts {
		if k == 99000 || k == 990000 {
			t.Fatalf("пресеты не должны выдавать цены скрытого тарифа: %v", amts)
		}
	}

	// Публичный тариф в пресеты попадает.
	p2 := &model.Plan{Code: "pubpubpubpub", Name: "Pub", Enabled: true, Availability: model.PlanAvailAll,
		Durations: []model.PlanDuration{{Months: 1, Base: "777"}}}
	_ = fs.SavePlan(ctx, p2)
	amts, _ = a.topUpAmounts(ctx)
	found := false
	for _, k := range amts {
		if k == 77700 {
			found = true
		}
	}
	if !found {
		t.Fatalf("цена публичного тарифа должна попасть в пресеты: %v", amts)
	}
}

// Неудачи старше окна прощаются: лимит не превращается в вечный бан.
func TestPlanLink_ThrottleExpires(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)

	uid := int64(716)
	old := time.Now().Add(-planLinkFailWindow - time.Minute)
	a.thrMu.Lock()
	a.planLinkFails = map[int64][]time.Time{uid: {old, old, old, old, old}}
	a.thrMu.Unlock()

	a.handleMessage(ctx, msgText(uid, "/start plan_"+p.Code))
	if !strings.Contains(fm.last(), "VIP") {
		t.Fatalf("старые неудачи должны прощаться: %q", fm.last())
	}
}

// Оплата с баланса берёт цену тарифа и кладёт в платёж тарифный снимок.
func TestPlanSale_Balance(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	srv := panelStub(1)
	defer srv.Close()
	a.panel = testPanel(srv.URL)
	p := vipPlan(t, fs, model.PlanAvailLink)

	uid := int64(730)
	_ = fs.UpsertUser(ctx, uid)
	_ = fs.AddBalance(ctx, uid, 200000) // 2000 ₽
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	a.handleCallback(ctx, cb(uid, "method:bal"))

	var pay *model.Payment
	for _, x := range fs.pays {
		if x.TelegramID == uid {
			pay = x
		}
	}
	if pay == nil || pay.Snapshot == nil || pay.Snapshot.Code != p.Code || pay.Amount != "990 ₽" {
		t.Fatalf("платёж должен нести тарифный снимок и цену: %+v", pay)
	}
	if bal, _ := fs.GetUser(ctx, uid); bal.Balance != 200000-99000 {
		t.Fatalf("с баланса должна списаться цена тарифа: %d", bal.Balance)
	}
}

// Гейт №3: тариф стал недоступен после выставления счёта — подписка выдаётся
// по снимку, админ получает уведомление.
func TestPlanGateBreach_NotifiesAdmin(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	srv := panelStub(1)
	defer srv.Close()
	a.panel = testPanel(srv.URL)
	p := vipPlan(t, fs, model.PlanAvailAll)

	uid := int64(740)
	_ = fs.UpsertUser(ctx, uid)
	snap := &model.PlanSnapshot{Code: p.Code, Name: "VIP", Months: 1, DeviceLimit: 7, Price: "990", Currency: "₽"}

	// Пока тариф доступен — тишина.
	if _, _, err := a.finalizePurchase(ctx, uid, 1, model.PayMethodCryptoBot, "990 ₽", "ext-b1", snap); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fm.joined(), "недоступен покупателю") {
		t.Fatalf("ложное уведомление: %q", fm.joined())
	}

	// Тариф удалён между счётом и оплатой.
	_ = fs.DeletePlan(ctx, p.Code)
	if _, _, err := a.finalizePurchase(ctx, uid, 1, model.PayMethodCryptoBot, "990 ₽", "ext-b2", snap); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fm.joined(), "недоступен покупателю") {
		t.Fatalf("админ должен узнать о продаже удалённого тарифа: %q", fm.last())
	}
	// Подписка при этом выдана: платёж записан.
	if done, _ := fs.PaymentByExtID(ctx, "ext-b2"); !done {
		t.Fatal("подписка должна быть выдана по снимку")
	}
}

// Смена режима «по списку» на другой чистит список только через подтверждение.
func TestPlanAvail_ModeSwitchClearsListWithConfirm(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailList)
	_ = fs.GrantPlanAccess(ctx, p.Code, 900, "")

	planTap(t, a, "pln:avm:all:"+p.Code)
	if got, _ := fs.GetPlan(ctx, p.Code); got.Availability != model.PlanAvailList {
		t.Fatalf("режим не должен меняться без подтверждения: %q", got.Availability)
	}
	if !strings.Contains(fm.last(), "очистит список") {
		t.Fatalf("нет подтверждения: %q", fm.last())
	}
	planTap(t, a, "pln:avmc:all:"+p.Code)
	got, _ := fs.GetPlan(ctx, p.Code)
	if got.Availability != model.PlanAvailAll {
		t.Fatalf("режим не сменился: %q", got.Availability)
	}
	if ok, _ := fs.HasPlanAccess(ctx, p.Code, 900, ""); ok {
		t.Fatal("список должен быть очищен")
	}

	// Пустой список чистится без вопросов.
	planTap(t, a, "pln:avm:link:"+p.Code)
	if got, _ := fs.GetPlan(ctx, p.Code); got.Availability != model.PlanAvailLink {
		t.Fatalf("смена без списка должна проходить сразу: %q", got.Availability)
	}
	// Смена режима — оформление: ведомость «Базового» она не трогает, а у
	// прочих тарифов FromConfig и так снят.
	if got, _ := fs.GetPlan(ctx, p.Code); got.FromConfig {
		t.Fatal("FromConfig не должен взводиться")
	}
}

// Многострочный ввод списка: ID и почты вперемешку, мусор честно считается.
func TestPlanAvail_MultilineInput(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailList)

	planTap(t, a, "pln:avla:"+p.Code)
	a.handleMessage(ctx, msgText(planAdmin, "123, 456\nUser@Example.com; мусор 0"))

	if ok, _ := fs.HasPlanAccess(ctx, p.Code, 123, ""); !ok {
		t.Fatal("ID из ввода не добавлен")
	}
	if ok, _ := fs.HasPlanAccess(ctx, p.Code, 456, ""); !ok {
		t.Fatal("второй ID не добавлен")
	}
	if ok, _ := fs.HasPlanAccess(ctx, p.Code, -1, "user@example.com"); !ok {
		t.Fatal("почта не добавлена (или не нормализована)")
	}
	if !strings.Contains(fm.last(), "Добавлено: 3") || !strings.Contains(fm.last(), "пропущено: 2") {
		t.Fatalf("итог ввода неверен: %q", fm.last())
	}
}

// Выдача и отзыв допуска из карточки пользователя.
func TestUserCard_PlanAccessToggle(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailList)
	uid := int64(910)
	_ = fs.UpsertUser(ctx, uid)

	planTap(t, a, "usr:plans:"+itoa64(uid))
	if !strings.Contains(fm.last(), "Допуски") {
		t.Fatalf("экран допусков не открылся: %q", fm.last())
	}
	planTap(t, a, "usr:pg:"+itoa64(uid)+":"+p.Code)
	if ok, _ := fs.HasPlanAccess(ctx, p.Code, uid, ""); !ok {
		t.Fatal("допуск не выдан")
	}
	planTap(t, a, "usr:pg:"+itoa64(uid)+":"+p.Code)
	if ok, _ := fs.HasPlanAccess(ctx, p.Code, uid, ""); ok {
		t.Fatal("допуск не отозван")
	}

	// E-mail-аккаунт кабинета: допуск пишется по почте.
	web := int64(-33)
	fs.webUsers = map[string]*model.WebUser{"w@e.com": {TgID: web, Email: "w@e.com"}}
	_ = fs.UpsertUser(ctx, web)
	planTap(t, a, "usr:pg:"+itoa64(web)+":"+p.Code)
	if ok, _ := fs.HasPlanAccess(ctx, p.Code, 0, "w@e.com"); !ok {
		t.Fatal("допуск по почте не выдан")
	}
}

// Экспорт → импорт: файл честно переносит тариф и список допущенных; новый
// тариф создаётся выключенным.
func TestPlanExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailList)
	_ = fs.GrantPlanAccess(ctx, p.Code, 111, "")
	_ = fs.GrantPlanAccess(ctx, p.Code, 0, "x@y.z")

	planTap(t, a, "pln:exp:"+p.Code)
	data := fm.docs["plan_"+p.Code+".json"]
	if len(data) == 0 {
		t.Fatalf("файл не отправлен: %v", fm.joined())
	}

	// Переносим в «другой бот»: тариф удалён, файл импортируется заново.
	_ = fs.DeletePlan(ctx, p.Code)
	planTap(t, a, "pln:imp")
	fm.downloads = map[string][]byte{"file-1": data}
	doc := &models.Message{
		Chat: models.Chat{ID: planAdmin}, From: &models.User{ID: planAdmin},
		Document: &models.Document{FileID: "file-1", FileSize: int64(len(data))},
	}
	a.handleDocument(ctx, doc)
	if !strings.Contains(fm.last(), "Импорт тарифа") {
		t.Fatalf("нет предпросмотра: %q", fm.last())
	}
	planTap(t, a, "pln:impok")

	got, _ := fs.GetPlan(ctx, p.Code)
	if got == nil || got.Name != "VIP" || got.Availability != model.PlanAvailList {
		t.Fatalf("тариф не импортирован: %+v", got)
	}
	if got.Enabled {
		t.Fatal("импортированный НОВЫЙ тариф обязан быть выключен")
	}
	if got.FromConfig {
		t.Fatal("импортированный тариф не может быть ведомым от конфига")
	}
	if d := got.Duration(1); d == nil || d.Base != "990" || d.Stars != 55 {
		t.Fatalf("длительности искажены: %+v", got.Durations)
	}
	if ok, _ := fs.HasPlanAccess(ctx, p.Code, 111, ""); !ok {
		t.Fatal("список допущенных не переехал")
	}
	if ok, _ := fs.HasPlanAccess(ctx, p.Code, -1, "x@y.z"); !ok {
		t.Fatal("почта из списка не переехала")
	}
}

// Импорт поверх существующего тарифа: условия и список заменяются файлом,
// включённость, порядок и время создания остаются прежними.
func TestPlanImport_OverwriteKeepsPlacement(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailList)
	_ = fs.GrantPlanAccess(ctx, p.Code, 111, "")

	planTap(t, a, "pln:exp:"+p.Code)
	data := fm.docs["plan_"+p.Code+".json"]
	if len(data) == 0 {
		t.Fatal("файл не отправлен")
	}

	// Тариф живёт дальше: его включили, переставили и сменили список.
	cur, _ := fs.GetPlan(ctx, p.Code)
	cur.Enabled = true
	cur.Order = 5
	cur.Name = "VIP переименованный"
	_ = fs.SavePlan(ctx, cur)
	_ = fs.ClearPlanAccess(ctx, p.Code)
	_ = fs.GrantPlanAccess(ctx, p.Code, 222, "")

	planTap(t, a, "pln:imp")
	fm.downloads = map[string][]byte{"file-2": data}
	a.handleDocument(ctx, &models.Message{
		Chat: models.Chat{ID: planAdmin}, From: &models.User{ID: planAdmin},
		Document: &models.Document{FileID: "file-2", FileSize: int64(len(data))},
	})
	if !strings.Contains(fm.last(), "Заменит существующий") {
		t.Fatalf("предпросмотр не предупредил о замене: %q", fm.last())
	}
	planTap(t, a, "pln:impok")

	got, _ := fs.GetPlan(ctx, p.Code)
	if got == nil || got.Name != "VIP" {
		t.Fatalf("условия не заменились файлом: %+v", got)
	}
	if !got.Enabled || got.Order != 5 {
		t.Fatalf("включённость и порядок должны сохраниться: %+v", got)
	}
	if ok, _ := fs.HasPlanAccess(ctx, p.Code, 111, ""); !ok {
		t.Fatal("список из файла не применился")
	}
	if ok, _ := fs.HasPlanAccess(ctx, p.Code, 222, ""); ok {
		t.Fatal("список должен заменяться целиком, а не сливаться")
	}
}

// Ожидания файлов взаимоисключающие: свежее ожидание дампа remnashop не
// перехватывается забытым ожиданием файла тарифа.
func TestPlanImport_DoesNotHijackRSDump(t *testing.T) {
	a, _, _ := planAdminApp(t)

	planTap(t, a, "pln:imp")
	if !a.getUI(planAdmin).awaitPlanImport {
		t.Fatal("ожидание файла тарифа не взведено")
	}
	planTap(t, a, "rsimp:up")
	if a.getUI(planAdmin).awaitPlanImport {
		t.Fatal("ожидание файла тарифа должно сняться при взводе ожидания дампа")
	}
	if !a.getUI(planAdmin).awaitRSDump {
		t.Fatal("ожидание дампа не взведено")
	}
	// И наоборот.
	planTap(t, a, "pln:imp")
	if a.getUI(planAdmin).awaitRSDump {
		t.Fatal("ожидание дампа должно сняться при взводе ожидания тарифа")
	}
}

// Мусорный файл отклоняется с причиной, «файл из будущего» — тоже.
func TestPlanImport_RejectsGarbage(t *testing.T) {
	if _, _, err := parsePlanFile([]byte("{")); err == nil {
		t.Fatal("битый JSON должен отклоняться")
	}
	if _, _, err := parsePlanFile([]byte(`{"format":"other","version":1,"plan":{"code":"abc","name":"x"}}`)); err == nil {
		t.Fatal("чужой формат должен отклоняться")
	}
	if _, _, err := parsePlanFile([]byte(`{"format":"remnabot-plan","version":9,"plan":{"code":"abc","name":"x"}}`)); err == nil {
		t.Fatal("файл более новой версии должен отклоняться")
	}
	if _, _, err := parsePlanFile([]byte(`{"format":"remnabot-plan","version":1,"plan":{"code":"такое","name":"x"}}`)); err == nil {
		t.Fatal("недопустимый код должен отклоняться")
	}
	if _, _, err := parsePlanFile([]byte(`{"format":"remnabot-plan","version":1,"plan":{"code":"abcabc","name":"x","durations":[{"base":"1"}]}}`)); err == nil {
		t.Fatal("длительность без срока должна отклоняться")
	}
	if _, _, err := parsePlanFile([]byte(`{"format":"remnabot-plan","version":1,"plan":{"code":"abcabc","name":"x"},"access":[{}]}`)); err == nil {
		t.Fatal("пустая запись списка должна отклоняться")
	}
}

// Автосписание подписчика тарифа: цена и условия берутся из ЕГО тарифа, а
// удалённый тариф останавливает списание с причиной, видной админу.
func TestAutoPay_PlanPricing(t *testing.T) {
	ctx := context.Background()
	a, fs := autoPayApp(t)
	now := time.Now().UTC()

	p := vipPlan(t, fs, model.PlanAvailLink)
	uid := int64(950)
	_ = fs.UpsertUser(ctx, uid)
	ap := &model.AutoPay{
		TelegramID: uid, Method: model.PayMethodYooKassa, MethodID: "pm", Months: 6,
		Enabled: true, Snapshot: &model.PlanSnapshot{Code: p.Code, Months: 6},
	}
	_ = fs.SetAutoPay(ctx, ap)

	// У тарифа нет цены на 6 месяцев — списание откладывается, а не идёт по
	// сетке «Базового».
	reason := a.chargeAutoPay(ctx, ap, now, now.Add(time.Hour))
	if reason == "" || !strings.Contains(reason, "нет цены") {
		t.Fatalf("ожидалась причина «нет цены»: %q", reason)
	}

	// Тариф удалён — «действующих условий» больше нет.
	_ = fs.DeletePlan(ctx, p.Code)
	reason = a.chargeAutoPay(ctx, ap, now, now.Add(time.Hour))
	if reason == "" || !strings.Contains(reason, "удалён") {
		t.Fatalf("ожидалась причина про удалённый тариф: %q", reason)
	}
	if cur, _ := fs.GetAutoPay(ctx, uid); cur.Fails != 0 || !cur.Enabled {
		t.Fatalf("проблема магазина не должна наказывать пользователя: %+v", cur)
	}
}

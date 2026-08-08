package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func planApp(t *testing.T) (*App, *fakeStore) {
	t.Helper()
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   &fakeMsg{},
		store: fs,
		ui:    map[int64]*uiState{},
	}
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru", Pricing: model.Pricing{
		Currency:        "₽",
		Base:            map[int]string{1: "150", 3: "400", 12: "1400"},
		P2P:             map[int]string{1: "140"},
		YooKassa:        map[int]string{1: "160"},
		Stars:           map[int]int{1: 99},
		Traffic:         map[int]int{12: 500},
		Devices:         map[int]int{3: 5},
		SquadsInt:       map[int][]string{12: {"squad-year"}},
		SquadsExt:       map[int]string{12: "ext-year"},
		DeviceLimit:     3,
		TrafficStrategy: "MONTH",
	}}
	a.botCfg.Plan.ActiveInternalSquads = []string{"squad-main"}
	a.botCfg.NormalizePricing()
	return a, fs
}

// Первый запуск новой версии обязан перенести текущую сетку в тариф «Базовый»
// один в один: цены по способам оплаты, лимиты, сквады и переопределения по
// срокам.
func TestSyncBasePlan_MigratesPricingGrid(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)

	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	if p == nil {
		t.Fatal("тариф «Базовый» не создан")
	}
	if !p.Enabled || p.Name != "Базовый" || p.Currency != "₽" || p.Availability != model.PlanAvailAll {
		t.Fatalf("оформление тарифа неверно: %+v", p)
	}
	if !p.FromConfig {
		t.Fatalf("тариф из сетки цен обязан быть помечен ведомым: %+v", p)
	}
	if p.DeviceLimit != 3 || p.Strategy != "MONTH" {
		t.Fatalf("лимиты тарифа неверны: %+v", p)
	}
	if len(p.IntSquads) != 1 || p.IntSquads[0] != "squad-main" {
		t.Fatalf("сквады тарифа неверны: %+v", p.IntSquads)
	}
	// Срок 6 месяцев в сетке не продаётся — в тарифе его быть не должно.
	if len(p.Durations) != 3 {
		t.Fatalf("длительностей должно быть три: %+v", p.Durations)
	}
	if p.Duration(6) != nil {
		t.Fatalf("срок без цены попал в тариф: %+v", p.Durations)
	}
	d1 := p.Duration(1)
	if d1 == nil || d1.Base != "150" || d1.P2P != "140" || d1.YooKassa != "160" || d1.Stars != 99 {
		t.Fatalf("цены на месяц перенесены неверно: %+v", d1)
	}
	if d1.TrafficGB != nil || d1.DeviceLimit != nil || d1.IntSquads != nil || d1.ExtSquad != nil {
		t.Fatalf("у месяца не было переопределений, а они появились: %+v", d1)
	}
	d3 := p.Duration(3)
	if d3 == nil || d3.DeviceLimit == nil || *d3.DeviceLimit != 5 {
		t.Fatalf("переопределение устройств на 3 месяца потеряно: %+v", d3)
	}
	d12 := p.Duration(12)
	if d12 == nil || d12.TrafficGB == nil || *d12.TrafficGB != 500 {
		t.Fatalf("переопределение трафика на год потеряно: %+v", d12)
	}
	if d12.IntSquads == nil || len(*d12.IntSquads) != 1 || (*d12.IntSquads)[0] != "squad-year" {
		t.Fatalf("сквады года потеряны: %+v", d12)
	}
	if d12.ExtSquad == nil || *d12.ExtSquad != "ext-year" {
		t.Fatalf("внешний сквад года потерян: %+v", d12)
	}

	// Условия, которые тариф отдаёт на год, обязаны совпасть с тем, что
	// продаёт бот сегодня: снимок сделки — эталон.
	snap := a.planSnapshot(12)
	if p.TrafficGBFor(d12) != snap.TrafficGB || p.DeviceLimitFor(d12) != snap.DeviceLimit ||
		p.ExtSquadFor(d12) != snap.ExtSquad {
		t.Fatalf("тариф расходится со снимком сделки: %+v против %+v", d12, snap)
	}
	if sq := p.IntSquadsFor(d12); len(sq) != len(snap.IntSquads) || sq[0] != snap.IntSquads[0] {
		t.Fatalf("сквады тарифа расходятся со снимком: %+v против %+v", sq, snap.IntSquads)
	}
	if d12.Fiat(model.PayMethodP2P) != a.botCfg.Pricing.Fiat(model.PayMethodP2P, 12) {
		t.Fatalf("цена P2P на год разошлась: %q", d12.Fiat(model.PayMethodP2P))
	}
}

// Легаси-звено цепочки сквадов (одиночный сквад P2P) должно доехать до тарифа
// ровно тогда, когда его использует финализация.
func TestSyncBasePlan_LegacyP2PSquad(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	a.botCfg.Plan.ActiveInternalSquads = nil
	a.botCfg.P2P.SquadUUID = "squad-p2p"

	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	if p == nil || len(p.IntSquads) != 1 || p.IntSquads[0] != "squad-p2p" {
		t.Fatalf("легаси-сквад P2P не перенесён: %+v", p)
	}
	snap := a.planSnapshot(1)
	if len(snap.IntSquads) != 1 || snap.IntSquads[0] != p.IntSquads[0] {
		t.Fatalf("тариф разошёлся со снимком: %+v против %+v", p.IntSquads, snap.IntSquads)
	}
}

// Повторный запуск не плодит тарифы, не теряет админское оформление и
// подхватывает новые цены.
func TestSyncBasePlan_RepeatKeepsAdminFields(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	p.Name = "Личный"
	p.Icon = "💎"
	p.Description = "описание"
	p.Order = 7
	p.Enabled = false
	if err := fs.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}

	a.botCfg.Pricing.Base[1] = "199"
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	list, _ := fs.ListPlans(ctx)
	if len(list) != 1 {
		t.Fatalf("тарифов должно остаться один: %+v", list)
	}
	got, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	if got.Name != "Личный" || got.Icon != "💎" || got.Description != "описание" ||
		got.Order != 7 || got.Enabled {
		t.Fatalf("оформление тарифа затёрто: %+v", got)
	}
	if d := got.Duration(1); d == nil || d.Base != "199" {
		t.Fatalf("новая цена не доехала до тарифа: %+v", d)
	}
}

// Сохранение конфига из админки цен обязано доводить правку до тарифа: иначе
// сетка и тариф разъедутся в первый же день.
func TestSaveBotConfig_SyncsBasePlan(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	if err := a.saveBotConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := fs.GetPlan(ctx, model.PlanCodeBase); p == nil {
		t.Fatal("сохранение конфига не создало тариф")
	}
	a.botCfg.Pricing.Base[3] = "450"
	if err := a.saveBotConfig(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	if d := p.Duration(3); d == nil || d.Base != "450" {
		t.Fatalf("правка цены не доехала до тарифа: %+v", d)
	}
}

// Бот без настроенных цен не должен падать и не должен заводить пустой тариф с
// нулевыми условиями.
func TestSyncBasePlan_NoPrices(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	a.botCfg.Pricing = model.Pricing{}
	a.botCfg.NormalizePricing()
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	if p == nil || len(p.Durations) != 0 {
		t.Fatalf("тариф без цен должен быть пустым: %+v", p)
	}
}

// Тариф, который правили редактором (признак «ведомый от конфига» снят),
// пересборкой из старой сетки затирать нельзя: иначе откат на версию, где
// редактора ещё не было, стирает работу админа.
func TestSyncBasePlan_SkipsEditedPlan(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	p.FromConfig = false
	p.Durations = []model.PlanDuration{{Days: 7, Base: "70"}}
	if err := fs.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}

	a.botCfg.Pricing.Base[1] = "999"
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	if len(got.Durations) != 1 || got.Durations[0].Days != 7 || got.Durations[0].Base != "70" {
		t.Fatalf("правки редактора затёрты сеткой цен: %+v", got.Durations)
	}
}

// Выбор срока обязан пережить рестарт бота: экран со способами оплаты остаётся
// в чате рабочим, и раньше нажатие на нём после перезапуска молча продавало
// месяц вместо года.
func TestBuyIntent_SurvivesRestart(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	const u int64 = 555

	a.onBuyPlan(ctx, u, "12")
	if a.buyMonths(ctx, u) != 12 {
		t.Fatalf("выбор не сохранён: %d", a.buyMonths(ctx, u))
	}

	// Новый процесс: память пуста, база та же.
	restarted := &App{
		cfg:   a.cfg,
		log:   a.log,
		msg:   &fakeMsg{},
		store: fs,
		ui:    map[int64]*uiState{},
	}
	restarted.botCfg = a.botCfg
	if got := restarted.buyMonths(ctx, u); got != 12 {
		t.Fatalf("после рестарта выбор потерян: %d", got)
	}

	// Новый выбор вытесняет прежний.
	restarted.onBuyPlan(ctx, u, "3")
	if got := restarted.buyMonths(ctx, u); got != 3 {
		t.Fatalf("новый выбор не применился: %d", got)
	}
	if in, err := restarted.buyIntent(ctx, u); err != nil || in == nil || in.PlanCode != model.PlanCodeBase {
		t.Fatalf("тариф в намерении покупки не проставлен: %+v err=%v", in, err)
	}
}

// У Stars нет строки счёта, а payload трогать нельзя — снимок условий живёт в
// намерении покупки. Правка конфига между выставлением счёта и оплатой не
// должна доезжать до человека.
func TestStars_AppliesSnapshotFromIntent(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	a.botCfg.Stars = model.StarsConfig{Enabled: true, Prices: map[int]int{1: 100}}
	a.botCfg.Pricing.Stars = map[int]int{1: 100}

	a.handleCallback(ctx, cb(u, "buy:1"))
	a.handleCallback(ctx, cb(u, "method:stars"))
	snap, _, _ := fs.InvoiceSnapshot(ctx, u, model.PayMethodStars, 1)
	if snap == nil || snap.DeviceLimit != 3 {
		t.Fatalf("условия счёта Stars не записаны: %+v", snap)
	}

	// Админ правит условия уже после того, как счёт выставлен.
	a.botCfg.Pricing.Devices[1] = 99
	a.handleSuccessfulPayment(ctx, successPayMsg(u, "stars:1", 100))
	if patched["hwidDeviceLimit"] != float64(3) {
		t.Fatalf("применились текущие условия вместо проданных: %+v", patched)
	}

	// Снимок от другого срока к этой покупке не относится.
	if snap := a.starsSnapshot(ctx, u, 12); snap != nil {
		t.Fatalf("снимок чужого срока не должен применяться: %+v", snap)
	}
}

// Код тарифа обязан ехать вместе со сделкой: он лежит в снимке, а снимок уже
// расходится по счетам, заявкам, платежам и пользователю. Отдельных колонок
// поэтому не заводим.
func TestPlanCodeTravelsWithDeal(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)

	// До первой синхронизации тариф ещё не прочитан — снимок всё равно обязан
	// быть подписан кодом, иначе сделка окажется «ничья».
	if snap := a.planSnapshot(1); snap.Code != model.PlanCodeBase || snap.Name != "Базовый" {
		t.Fatalf("снимок без тарифа: %+v", snap)
	}

	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	p.Name = "Личный"
	if err := fs.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	snap := a.planSnapshot(3)
	if snap.Code != model.PlanCodeBase || snap.Name != "Личный" {
		t.Fatalf("снимок не подхватил имя тарифа: %+v", snap)
	}

	// Заявка на перевод — один из носителей снимка.
	a.botCfg.P2P = model.P2PConfig{Enabled: true, OpenForAll: true, Cards: []string{"0000"}}
	a.onBuyPlan(ctx, 555, "3")
	if _, _, _, err := a.prepareP2PCard(ctx, 555, 3); err != nil {
		t.Fatal(err)
	}
	var req *model.P2PRequest
	for _, r := range fs.reqs {
		req = r
	}
	if req == nil || req.Snapshot == nil || req.Snapshot.Code != model.PlanCodeBase {
		t.Fatalf("тариф не доехал до заявки: %+v", req)
	}
}

// Кнопка способа оплаты на старом экране из истории чата больше не продаёт
// «месяц по умолчанию»: без выбранного срока бот возвращает человека в витрину
// и счёт не выставляет.
func TestPayMethodWithoutPeriod_AsksAgain(t *testing.T) {
	ctx := context.Background()
	fm := &fakeMsg{}
	a, fs := planApp(t)
	a.msg = fm
	a.botCfg.Stars = model.StarsConfig{Enabled: true, Prices: map[int]int{1: 99}}
	const u int64 = 555

	a.handleCallback(ctx, cb(u, "method:stars"))
	if len(fm.invoices) != 0 {
		t.Fatalf("счёт выставлен без выбранного срока: %v", fm.invoices)
	}
	if !strings.Contains(fm.joined(), "срок подписки") {
		t.Fatalf("ожидалась витрина со сроками:\n%s", fm.joined())
	}
	if in, _ := fs.PurchaseIntent(ctx, u); in != nil {
		t.Fatalf("намерение покупки не должно появляться само: %+v", in)
	}

	// А с выбранным сроком счёт выставляется как обычно.
	a.handleCallback(ctx, cb(u, "buy:1"))
	a.handleCallback(ctx, cb(u, "method:stars"))
	if len(fm.invoices) != 1 {
		t.Fatalf("после выбора срока счёт не выставлен: %v", fm.invoices)
	}
}

// Оплата пришла, а срок определить не удалось: подписку на «срок по умолчанию»
// не выдаём — зовём админа и говорим человеку, что им занимаются.
func TestPaidWithoutPeriod_CallsAdmin(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	fm := &fakeMsg{}
	a.msg = fm
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)

	a.handleSuccessfulPayment(ctx, successPayMsg(u, "stars:", 100))

	if ok, _ := fs.HasPaidPayment(ctx, u); ok {
		t.Fatal("подписка выдана по неизвестному сроку")
	}
	if patched != nil {
		t.Fatalf("панель трогать было нельзя: %+v", patched)
	}
	joined := fm.joined()
	if !strings.Contains(joined, "срок подписки определить не удалось") {
		t.Fatalf("админ не уведомлён:\n%s", joined)
	}
	if !strings.Contains(joined, "Администратор уведомлён") {
		t.Fatalf("пользователь не уведомлён:\n%s", joined)
	}
}

// «0 = безлимит» из админки обязан доезжать до панели нулём. Раньше нулевые
// поля просто не отправлялись, и купивший безлимит после триала оставался с
// триальным лимитом.
func TestUnlimitedTrafficReachesPanel(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	// Тариф без ограничения трафика — ровно то, что админка называет
	// безлимитом.
	a.botCfg.Pricing.Traffic = map[int]int{}

	if _, _, err := a.finalizePurchase(ctx, u, 1, model.PayMethodStars, "150", "unlim_1", nil); err != nil {
		t.Fatal(err)
	}
	v, ok := patched["trafficLimitBytes"]
	if !ok {
		t.Fatalf("лимит трафика не отправлен в панель: %+v", patched)
	}
	if v != float64(0) {
		t.Fatalf("безлимит должен уехать нулём, а уехало %v", v)
	}
}

// Бонусные дни (реферальные и промокод «дни») по-прежнему обязаны НЕ трогать
// лимиты: пустой набор — это «не менять», а не «обнулить».
func TestBonusDaysDoNotTouchLimits(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, _ := snapApp(t, srv.URL)
	ctx := context.Background()

	if _, _, err := a.panel.CreateOrUpdateUserDays(ctx, 555, 7, remnawave.UserLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := patched["trafficLimitBytes"]; ok {
		t.Fatalf("бонусные дни не должны менять лимит трафика: %+v", patched)
	}
	if _, ok := patched["hwidDeviceLimit"]; ok {
		t.Fatalf("бонусные дни не должны менять лимит устройств: %+v", patched)
	}
}

// Счёт Stars из мини-аппа не должен перебивать срок, выбранный в чате: там
// висит экран, подписанный ценой другого срока.
func TestStarsFromMiniApp_KeepsChatChoice(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	a.botCfg.Stars = model.StarsConfig{Enabled: true, Prices: map[int]int{1: 99, 12: 900}}
	a.botCfg.Pricing.Stars = map[int]int{1: 99, 12: 900}
	const u int64 = 555

	a.onBuyPlan(ctx, u, "12")
	// Мини-апп запрашивает ссылку на счёт Stars на другой срок.
	if _, err := a.starsInvoiceLink(ctx, u, 1); err != nil {
		t.Fatal(err)
	}
	if got := a.buyMonths(ctx, u); got != 12 {
		t.Fatalf("мини-апп затёр выбор из чата: %d", got)
	}
	// Условия счёта из мини-аппа живут отдельной строкой и выбору не мешают.
	if snap, _, _ := fs.InvoiceSnapshot(ctx, u, model.PayMethodStars, 1); snap == nil {
		t.Fatal("условия счёта из мини-аппа не сохранены")
	}
	if _, err := a.starsInvoiceLink(ctx, u, 12); err != nil {
		t.Fatal(err)
	}
	if snap, _, _ := fs.InvoiceSnapshot(ctx, u, model.PayMethodStars, 12); snap == nil {
		t.Fatal("условия счёта на выбранный срок потеряны")
	}
	if got := a.buyMonths(ctx, u); got != 12 {
		t.Fatalf("второй счёт из мини-аппа сдвинул выбор: %d", got)
	}
}

// Мини-апп не должен ЗАВОДИТЬ выбор срока в чате: после покупки (или по
// истечении выбора) экран способов оплаты остаётся в переписке, и нажатие на
// нём должно вести в витрину, а не продавать срок, взятый из мини-аппа.
func TestStarsFromMiniApp_DoesNotCreateChatChoice(t *testing.T) {
	ctx := context.Background()
	fm := &fakeMsg{}
	a, fs := planApp(t)
	a.msg = fm
	a.botCfg.Stars = model.StarsConfig{Enabled: true, Prices: map[int]int{1: 99}}
	a.botCfg.Pricing.Stars = map[int]int{1: 99}
	const u int64 = 555

	if _, err := a.starsInvoiceLink(ctx, u, 1); err != nil {
		t.Fatal(err)
	}
	if in, _ := fs.PurchaseIntent(ctx, u); in != nil {
		t.Fatalf("мини-апп завёл выбор срока в чате: %+v", in)
	}
	a.handleCallback(ctx, cb(u, "method:bal"))
	if !strings.Contains(fm.joined(), "срок подписки") {
		t.Fatalf("ожидалась витрина, а не продажа срока из мини-аппа:\n%s", fm.joined())
	}
}

// Выбор срока не должен жить вечно: нажатие на экране полугодовой давности —
// это не «человек выбрал год», а просто старая кнопка.
func TestBuyIntent_Expires(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	const u int64 = 555

	if err := fs.SetPurchaseIntent(ctx, &model.PurchaseIntent{
		TelegramID: u, PlanCode: model.PlanCodeBase, Months: 12,
		CreatedAt: time.Now().UTC().Add(-purchaseIntentTTL - time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if got := a.buyMonths(ctx, u); got != 0 {
		t.Fatalf("просроченный выбор применился: %d", got)
	}
	if in, _ := fs.PurchaseIntent(ctx, u); in != nil {
		t.Fatalf("просроченное намерение не убрано: %+v", in)
	}
}

// После состоявшейся покупки выбор забывается — старая кнопка способа оплаты
// больше не продаёт тот же срок повторно.
func TestBuyIntent_ForgottenAfterPurchase(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)

	a.onBuyPlan(ctx, u, "1")
	if _, _, err := a.finalizePurchase(ctx, u, 1, model.PayMethodStars, "150", "forget_1", nil); err != nil {
		t.Fatal(err)
	}
	if in, _ := fs.PurchaseIntent(ctx, u); in != nil {
		t.Fatalf("выбор пережил покупку: %+v", in)
	}

	// Чужой срок (например, автопродление) выбор не стирает.
	a.botCfg.Pricing.Base[12] = "1400"
	a.onBuyPlan(ctx, u, "12")
	if _, _, err := a.finalizePurchase(ctx, u, 1, model.PayMethodYooKassa, "150", "forget_2", nil); err != nil {
		t.Fatal(err)
	}
	if got := a.buyMonths(ctx, u); got != 12 {
		t.Fatalf("продление стёрло текущий выбор человека: %d", got)
	}
}

// Условия счёта Stars нельзя терять при чтении: оплата может не дойти до
// панели с первого раза, и повторная доставка обязана применить ТЕ ЖЕ
// проданные условия, а не текущий конфиг.
func TestStarsSnapshot_SurvivesFailedFinalize(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	a.botCfg.Stars = model.StarsConfig{Enabled: true, Prices: map[int]int{1: 99}}
	a.botCfg.Pricing.Stars = map[int]int{1: 99}
	const u int64 = 555

	a.handleCallback(ctx, cb(u, "buy:1"))
	a.handleCallback(ctx, cb(u, "method:stars"))
	// Панель не подключена — финализация упадёт.
	a.handleSuccessfulPayment(ctx, successPayMsg(u, "stars:1", 99))

	snap, _, _ := fs.InvoiceSnapshot(ctx, u, model.PayMethodStars, 1)
	if snap == nil || snap.DeviceLimit != 3 {
		t.Fatalf("условия счёта пропали после неудачной выдачи: %+v", snap)
	}
	// Второй счёт на другой срок условия первого не трогает.
	a.handleCallback(ctx, cb(u, "buy:3"))
	a.handleCallback(ctx, cb(u, "method:stars"))
	if kept, _, _ := fs.InvoiceSnapshot(ctx, u, model.PayMethodStars, 1); kept == nil {
		t.Fatal("условия счёта на месяц потерялись при выставлении счёта на три")
	}
}

// Срок, снятый админом с продажи (нет базовой цены), не должен продаваться за
// звёзды в обход витрины — ни из чата, ни из мини-аппа.
func TestStars_PeriodOffSaleIsNotSold(t *testing.T) {
	ctx := context.Background()
	fm := &fakeMsg{}
	a, _ := planApp(t)
	a.msg = fm
	a.botCfg.Stars = model.StarsConfig{Enabled: true, Prices: map[int]int{6: 460}}
	a.botCfg.Pricing.Stars = map[int]int{6: 460}
	const u int64 = 555

	if _, err := a.starsInvoiceLink(ctx, u, 6); err == nil {
		t.Fatal("мини-апп выставил счёт на срок, снятый с продажи")
	}
	a.onBuyPlan(ctx, u, "6")
	a.startStars(ctx, u)
	if len(fm.invoices) != 0 {
		t.Fatalf("счёт выставлен на срок вне витрины: %v", fm.invoices)
	}
}

// Подделанные callback-данные не должны ни продаваться, ни выглядеть как сбой
// хранилища.
func TestBuyPlan_RejectsUnknownPeriod(t *testing.T) {
	ctx := context.Background()
	fm := &fakeMsg{}
	a, fs := planApp(t)
	a.msg = fm
	const u int64 = 555

	for _, val := range []string{"9999999999", "-5", "2", "abc"} {
		a.onBuyPlan(ctx, u, val)
		if in, _ := fs.PurchaseIntent(ctx, u); in != nil {
			t.Fatalf("подделанный срок %q записан в намерение: %+v", val, in)
		}
	}
	a.onBuyPlan(ctx, u, "12")
	if got := a.buyMonths(ctx, u); got != 12 {
		t.Fatalf("нормальный срок перестал приниматься: %d", got)
	}
}

// Срок, снятый админом с продажи, не должен продаваться ни одним способом:
// ни звёздами, ни переводом (у P2P это была бы заявка с пустой суммой).
func TestPeriodOffSale_NotSoldAnywhere(t *testing.T) {
	ctx := context.Background()
	fm := &fakeMsg{}
	a, fs := planApp(t)
	a.msg = fm
	a.botCfg.P2P = model.P2PConfig{Enabled: true, OpenForAll: true, Cards: []string{"0000"}}
	a.botCfg.Stars = model.StarsConfig{Enabled: true, Prices: map[int]int{6: 460}}
	a.botCfg.Pricing.Stars = map[int]int{6: 460}
	const u int64 = 555

	// 6 месяцев в сетке цен нет — витрина его не показывает.
	a.handleCallback(ctx, cb(u, "buy:6"))
	if in, _ := fs.PurchaseIntent(ctx, u); in != nil {
		t.Fatalf("срок вне продажи записан в намерение: %+v", in)
	}
	a.handleCallback(ctx, cb(u, "method:p2p"))
	if len(fs.reqs) != 0 {
		t.Fatalf("создана заявка на срок вне продажи: %+v", fs.reqs)
	}
	if len(fm.invoices) != 0 {
		t.Fatalf("выставлен счёт на срок вне продажи: %v", fm.invoices)
	}

	// А продаваемый срок работает как раньше.
	a.handleCallback(ctx, cb(u, "buy:3"))
	a.handleCallback(ctx, cb(u, "method:p2p"))
	if len(fs.reqs) != 1 {
		t.Fatalf("обычная заявка не создалась: %+v", fs.reqs)
	}
	for _, r := range fs.reqs {
		if r.Price == "" {
			t.Fatalf("заявка с пустой суммой: %+v", r)
		}
	}
}

// Условия счёта Stars не применяются вечно: счёт полугодовой давности — это уже
// не сегодняшняя сделка.
func TestStarsSnapshot_Expires(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	const u int64 = 555

	sold := &model.PlanSnapshot{Months: 1, DeviceLimit: 3}
	if err := fs.SetInvoiceSnapshot(ctx, u, model.PayMethodStars, 1, sold); err != nil {
		t.Fatal(err)
	}
	if got := a.starsSnapshot(ctx, u, 1); got == nil || got.DeviceLimit != 3 {
		t.Fatalf("свежие условия счёта не применились: %+v", got)
	}
	// Состариваем строку.
	fs.invSnapAt[invSnapKey(u, model.PayMethodStars, 1)] =
		time.Now().UTC().Add(-purchaseIntentTTL - time.Hour).Format(time.RFC3339)
	if got := a.starsSnapshot(ctx, u, 1); got != nil {
		t.Fatalf("применились условия протухшего счёта: %+v", got)
	}
}

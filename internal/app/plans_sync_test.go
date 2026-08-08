package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// effectiveGrid — то, что сетка цен фактически означает для продаж: цены по
// способам, лимиты и сквады каждого продаваемого срока плюс общие параметры.
// Сравнивать сырые карты нельзя: у сетки несколько эквивалентных представлений
// (ноль против отсутствия записи, легаси-сквад P2P против глобального набора).
func effectiveGrid(cfg *model.BotConfig) map[string]string {
	out := map[string]string{}
	pr := cfg.Pricing
	out["currency"] = pr.Currency
	out["strategy"] = pr.ResetStrategy()
	for _, mo := range model.PlanMonths {
		if pr.Base[mo] == "" {
			continue // срок не продаётся — его настроек покупатель не увидит
		}
		key := func(k string) string { return k + ":" + string(rune('0'+mo)) }
		out[key("base")] = pr.Base[mo]
		out[key("p2p")] = pr.Fiat(model.PayMethodP2P, mo)
		out[key("yk")] = pr.Fiat(model.PayMethodYooKassa, mo)
		out[key("stars")] = string(rune('0' + pr.StarPrice(mo)%10))
		out[key("traffic")] = string(rune('0' + int(pr.TrafficBytes(mo)/(1024*1024*1024))%10))
		out[key("dev")] = string(rune('0' + pr.DeviceLimitFor(mo)%10))
		// Цепочка сквадов финализации: глобальный набор → легаси P2P → набор
		// срока.
		ints := cfg.Plan.ActiveInternalSquads
		if len(ints) == 0 && cfg.P2P.SquadUUID != "" {
			ints = []string{cfg.P2P.SquadUUID}
		}
		if sq := pr.SquadsInt[mo]; len(sq) > 0 {
			ints = sq
		}
		out[key("int")] = strings.Join(ints, ",")
		ext := cfg.Plan.ExternalSquadUUID
		if e := pr.SquadsExt[mo]; e != "" {
			ext = e
		}
		out[key("ext")] = ext
	}
	return out
}

// Зеркало обязано быть обратной стороной пересборки: сетка → тариф → сетка не
// меняет ничего, что видит покупатель. Иначе первый же переход тарифа в
// ведущие молча менял бы продаваемые условия.
func TestMirrorRoundTripKeepsEffectiveGrid(t *testing.T) {
	grids := []model.Pricing{
		// обычная сетка со всеми переопределениями
		{
			Currency: "₽",
			Base:     map[int]string{1: "150", 3: "400", 6: "700", 12: "1400"},
			P2P:      map[int]string{1: "140", 12: "1300"},
			YooKassa: map[int]string{3: "420"},
			Stars:    map[int]int{1: 99, 6: 500},
			Traffic:  map[int]int{12: 500, 1: 0},
			Devices:  map[int]int{3: 5, 6: 0},
			SquadsInt: map[int][]string{
				12: {"squad-year", "squad-extra"},
			},
			SquadsExt:       map[int]string{12: "ext-year"},
			DeviceLimit:     3,
			TrafficStrategy: "MONTH_ROLLING",
		},
		// сетка с дырой: срок снят с продажи, переопределения остались
		{
			Currency:        "$",
			Base:            map[int]string{1: "5"},
			P2P:             map[int]string{3: "12"},
			Stars:           map[int]int{3: 250},
			Traffic:         map[int]int{3: 100},
			TrafficStrategy: "NO_RESET",
		},
		// пустая сетка (ничего не продаётся)
		{Currency: "₽", TrafficStrategy: "MONTH"},
	}
	plans := []model.SubscriptionPlan{
		{ActiveInternalSquads: []string{"main"}, ExternalSquadUUID: "ext-main"},
		{}, // глобальных сквадов нет — работает легаси-фолбэк P2P
	}
	for gi, pr := range grids {
		for pi, plan := range plans {
			cfg := &model.BotConfig{Installed: true, Language: "ru", Pricing: pr, Plan: plan}
			cfg.P2P.SquadUUID = "legacy-p2p"
			cfg.NormalizePricing()
			want := effectiveGrid(cfg)

			p := basePlanFrom(cfg, nil)
			cfg2 := &model.BotConfig{Installed: true, Language: "ru"}
			cfg2.P2P.SquadUUID = "legacy-p2p"
			cfg2.NormalizePricing()
			applyPlanToConfig(cfg2, p)
			got := effectiveGrid(cfg2)

			for k, v := range want {
				if got[k] != v {
					t.Fatalf("сетка %d/тариф %d: после круга %q стало %q, было %q", gi, pi, k, got[k], v)
				}
			}
			for k := range got {
				if _, ok := want[k]; !ok {
					t.Fatalf("сетка %d/тариф %d: после круга появилось лишнее %q=%q", gi, pi, k, got[k])
				}
			}
		}
	}
}

// Первая коммерческая правка делает тариф ведущим: from_config снимается, цена
// доезжает зеркалом до сетки, а последующие сохранения конфига НЕ пересобирают
// тариф из сетки обратно.
func TestFirstPriceEditFlipsAndMirrors(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetPlan(ctx, model.PlanCodeBase)
	if !p.FromConfig {
		t.Fatal("до правки тариф должен быть ведомым")
	}

	if err := a.setPlanPrice(ctx, "", 1, "base", "999"); err != nil {
		t.Fatal(err)
	}
	p, _ = st.GetPlan(ctx, model.PlanCodeBase)
	if p.FromConfig {
		t.Fatal("после правки тариф остался ведомым")
	}
	if got := a.pricing().Base[1]; got != "999" {
		t.Fatalf("цена не доехала зеркалом до сетки: %q", got)
	}

	// Ведущий тариф не пересобирается из сетки: правка сетки в обход сеттеров
	// (так делает только старый образ после отката) при сохранении конфига
	// зеркалится ОБРАТНО из тарифа.
	a.mu.Lock()
	a.botCfg.Pricing.Base[1] = "111"
	a.mu.Unlock()
	if err := a.saveBotConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.pricing().Base[1]; got != "999" {
		t.Fatalf("сетка не восстановлена из ведущего тарифа: %q", got)
	}
	p, _ = st.GetPlan(ctx, model.PlanCodeBase)
	if d := p.Duration(1); d == nil || d.Base != "999" {
		t.Fatalf("тариф затёрт сеткой: %+v", p.Durations)
	}
}

// Правка оформления тариф ведущим НЕ делает: имя — не коммерческое поле, и
// прежняя схема «цены правит конфиг» обязана пережить переименование.
func TestLookEditDoesNotFlip(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	fm := &fakeMsg{}
	a.msg = fm
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	planTap(t, a, "pln:name:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, "Стандарт"))
	planTap(t, a, "pln:toggle:"+model.PlanCodeBase)

	p, _ := st.GetPlan(ctx, model.PlanCodeBase)
	if !p.FromConfig {
		t.Fatal("оформление сняло from_config — цены из старой админки перестанут доезжать")
	}
}

// Лечение после отката: старый образ правил сетку напрямую, тариф — истина.
// Стартовая синхронизация восстанавливает сетку из тарифа и сообщает об этом.
func TestStartupHealRestoresGridFromPlan(t *testing.T) {
	ctx := context.Background()
	a, _ := planSyncApp(t, 0)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.setPlanPrice(ctx, "", 1, "base", "999"); err != nil {
		t.Fatal(err)
	}

	// «Откат»: сетку правят в обход тарифа.
	a.mu.Lock()
	a.botCfg.Pricing.Base[1] = "111"
	a.botCfg.Pricing.Traffic[12] = 42
	a.mu.Unlock()

	changed, err := a.syncPlansConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("расхождение сетки с тарифом не замечено")
	}
	pr := a.pricing()
	if pr.Base[1] != "999" || pr.Traffic[12] != 500 {
		t.Fatalf("сетка не восстановлена из тарифа: base=%q traffic=%d", pr.Base[1], pr.Traffic[12])
	}

	// Без расхождения лечение молчит — иначе уведомление приходило бы на каждом
	// старте.
	changed, err = a.syncPlansConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("зеркало сообщает о расхождении, которого нет")
	}
}

// Старые экраны продолжают работать: ввод цены на экране «Цены и лимиты»
// доезжает и до тарифа, и до сетки, по которой продаёт витрина.
func TestLegacyPricingScreenWritesThroughPlan(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	fm := &fakeMsg{}
	a.msg = fm
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	// Базовая цена через старый экран.
	planTap(t, a, "prc:price:3")
	a.handleMessage(ctx, msgText(planAdmin, "555"))
	// Трафик через старый экран.
	planTap(t, a, "prc:trafmo:3")
	a.handleMessage(ctx, msgText(planAdmin, "77"))
	// Стратегия кнопкой.
	planTap(t, a, "prc:setstrat:WEEK")
	// Сквад срока тумблером старого экрана.
	planTap(t, a, "prc:sqi:3:squad-new")

	p, _ := st.GetPlan(ctx, model.PlanCodeBase)
	if p.FromConfig {
		t.Fatal("правка старым экраном не сняла from_config")
	}
	d := p.Duration(3)
	if d == nil || d.Base != "555" || d.TrafficGB == nil || *d.TrafficGB != 77 {
		t.Fatalf("правка старого экрана не доехала до тарифа: %+v", d)
	}
	if p.Strategy != "WEEK" {
		t.Fatalf("стратегия не доехала до тарифа: %q", p.Strategy)
	}
	if d.IntSquads == nil || len(*d.IntSquads) == 0 || (*d.IntSquads)[len(*d.IntSquads)-1] != "squad-new" {
		t.Fatalf("сквад срока не доехал до тарифа: %+v", d.IntSquads)
	}

	pr := a.pricing()
	if pr.Base[3] != "555" || pr.Traffic[3] != 77 || pr.ResetStrategy() != "WEEK" {
		t.Fatalf("правка не доехала зеркалом до сетки: %+v", pr)
	}
	// Витрина продаёт новый срок по новой цене.
	if !a.periodOnSale(3) {
		t.Fatal("срок с ценой не продаётся")
	}

	// Прочерк снимает срок с продажи, переопределения остаются.
	planTap(t, a, "prc:price:3")
	a.handleMessage(ctx, msgText(planAdmin, "-"))
	if a.periodOnSale(3) {
		t.Fatal("срок не снят с продажи прочерком")
	}
	p, _ = st.GetPlan(ctx, model.PlanCodeBase)
	if d := p.Duration(3); d == nil || d.TrafficGB == nil || *d.TrafficGB != 77 {
		t.Fatalf("снятие с продажи стёрло переопределения: %+v", p.Durations)
	}
}

// Глобальные сквады «Продаж» — это сквады тарифа: тумблер старого экрана пишет
// в тариф, зеркало — в конфиг, и цепочка финализации видит новый набор.
func TestLegacySquadsScreenWritesThroughPlan(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	fm := &fakeMsg{}
	a.msg = fm
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	a.toggleInternalSquad(ctx, planAdmin, "squad-added")
	a.toggleExternalSquad(ctx, planAdmin, "ext-added")

	p, _ := st.GetPlan(ctx, model.PlanCodeBase)
	found := false
	for _, u := range p.IntSquads {
		if u == "squad-added" {
			found = true
		}
	}
	if !found || p.ExtSquad != "ext-added" {
		t.Fatalf("сквады не доехали до тарифа: %v / %q", p.IntSquads, p.ExtSquad)
	}
	a.mu.Lock()
	gInt := append([]string(nil), a.botCfg.Plan.ActiveInternalSquads...)
	gExt := a.botCfg.Plan.ExternalSquadUUID
	a.mu.Unlock()
	found = false
	for _, u := range gInt {
		if u == "squad-added" {
			found = true
		}
	}
	if !found || gExt != "ext-added" {
		t.Fatalf("сквады не доехали зеркалом до конфига: %v / %q", gInt, gExt)
	}

	// Снимок сделки собирается из конфига — новый набор виден и ему.
	snap := a.planSnapshot(1)
	found = false
	for _, u := range snap.IntSquads {
		if u == "squad-added" {
			found = true
		}
	}
	if !found || snap.ExtSquad != "ext-added" {
		t.Fatalf("снимок сделки не видит новые сквады: %+v", snap)
	}
}

// Редактор в карточке: цены не «Базового» живут только в тарифе — сетку и
// витрину они не трогают.
func TestCardEditorForOtherPlanDoesNotTouchGrid(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	fm := &fakeMsg{}
	a.msg = fm
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	planTap(t, a, "pln:dup:"+model.PlanCodeBase)
	list, _ := st.ListPlans(ctx)
	code := ""
	for i := range list {
		if list[i].Code != model.PlanCodeBase {
			code = list[i].Code
		}
	}

	gridBefore := a.pricing()

	// Через экраны карточки: цены и сроки → месяц 6 → базовая цена.
	planTap(t, a, "pln:pr:"+code)
	planTap(t, a, "pln:prm:6:"+code)
	planTap(t, a, "pln:in:b:6:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "800"))
	planTap(t, a, "pln:in:s:6:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "700"))

	p, _ := st.GetPlan(ctx, code)
	d := p.Duration(6)
	if d == nil || d.Base != "800" || d.Stars != 700 {
		t.Fatalf("правка карточки не доехала до тарифа: %+v", p.Durations)
	}
	if p.FromConfig {
		t.Fatal("не-базовый тариф стал ведомым")
	}

	gridAfter := a.pricing()
	if gridAfter.Base[6] != gridBefore.Base[6] || gridAfter.StarPrice(6) != gridBefore.StarPrice(6) {
		t.Fatalf("правка чужого тарифа тронула сетку: %q -> %q", gridBefore.Base[6], gridAfter.Base[6])
	}
	base, _ := st.GetPlan(ctx, model.PlanCodeBase)
	if !base.FromConfig {
		t.Fatal("правка чужого тарифа сняла from_config у «Базового»")
	}
}

// Тумблер сквада в карточке защищён отпечатком: нажатие на старом сообщении,
// когда админ уже редактирует другой тариф, отбивается, а не переключает сквад
// не тому тарифу.
func TestPlanSquadToggleStaleGuard(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	fm := &fakeMsg{}
	a.msg = fm
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	planTap(t, a, "pln:dup:"+model.PlanCodeBase)
	list, _ := st.ListPlans(ctx)
	code := ""
	for i := range list {
		if list[i].Code != model.PlanCodeBase {
			code = list[i].Code
		}
	}

	// Админ «редактирует» копию (контекст выставлен её экраном цен)…
	a.getUI(planAdmin).planEdit = code
	// …а нажимает тумблер со СТАРОГО экрана «Базового» (отпечаток базового).
	planTap(t, a, "plq:i:0:"+planEditHash(model.PlanCodeBase)+":squad-x")

	p, _ := st.GetPlan(ctx, code)
	for _, u := range p.IntSquads {
		if u == "squad-x" {
			t.Fatal("нажатие со старого экрана переключило сквад другому тарифу")
		}
	}
	if !strings.Contains(fm.last(), "устарел") {
		t.Fatalf("отказ не объяснён: %q", fm.last())
	}

	// Совпадающий отпечаток — работает.
	planTap(t, a, "plq:i:0:"+planEditHash(code)+":squad-x")
	p, _ = st.GetPlan(ctx, code)
	found := false
	for _, u := range p.IntSquads {
		if u == "squad-x" {
			found = true
		}
	}
	if !found {
		t.Fatal("тумблер с верным отпечатком не сработал")
	}
}

// «Базовый» без строки в базе — сбой стартовой синхронизации — не блокирует
// экран цен: первая же правка собирает тариф из текущей сетки и применяется.
func TestPriceEditBootstrapsMissingBasePlan(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	// Строки тарифа нет: syncBasePlan не звался.
	if p, _ := st.GetPlan(ctx, model.PlanCodeBase); p != nil {
		t.Fatal("тариф уже есть — тест не о том")
	}

	if err := a.setPlanPrice(ctx, "", 1, "base", "222"); err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetPlan(ctx, model.PlanCodeBase)
	if p == nil {
		t.Fatal("тариф не собран из сетки")
	}
	if d := p.Duration(1); d == nil || d.Base != "222" {
		t.Fatalf("цена не применилась: %+v", p.Durations)
	}
	// Остальная сетка переехала как была.
	if d := p.Duration(12); d == nil || d.Base != "1400" {
		t.Fatalf("прежние сроки потеряны при сборке: %+v", p.Durations)
	}
}

// Быстрая настройка (мастер «срок → цена → трафик → устройства») идёт тем же
// путём: тариф, зеркало, витрина.
func TestQuickSetupWritesThroughPlan(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	fm := &fakeMsg{}
	a.msg = fm
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	planTap(t, a, "prc:qmo:6")
	a.handleMessage(ctx, msgText(planAdmin, "650"))
	a.handleMessage(ctx, msgText(planAdmin, "120"))
	a.handleMessage(ctx, msgText(planAdmin, "4"))

	p, _ := st.GetPlan(ctx, model.PlanCodeBase)
	d := p.Duration(6)
	if d == nil || d.Base != "650" {
		t.Fatalf("быстрая настройка не доехала до тарифа: %+v", p.Durations)
	}
	if d.TrafficGB == nil || *d.TrafficGB != 120 || d.DeviceLimit == nil || *d.DeviceLimit != 4 {
		t.Fatalf("лимиты быстрой настройки потеряны: %+v", d)
	}
	pr := a.pricing()
	if pr.Base[6] != "650" || pr.Traffic[6] != 120 || pr.Devices[6] != 4 {
		t.Fatalf("быстрая настройка не доехала до сетки: %+v", pr)
	}
}

// Перенос сетки в тариф обязан быть полным: месяцы вне стандартной четвёрки и
// настройки сроков, снятых с продажи, переживают и перенос, и первый разворот
// синхронизации. Терялись они молча, а по цене из сетки продлевает автосписание.
func TestFlipPreservesLegacyMonthsAndOffSaleOverrides(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	a.mu.Lock()
	// Легаси-месяц вне четвёрки — по нему живёт автосписание.
	a.botCfg.Pricing.Base[2] = "260"
	a.botCfg.Pricing.YooKassa[2] = "280"
	// Срок снят с продажи, переопределения остались и должны вернуться с ценой.
	a.botCfg.Pricing.P2P[6] = "610"
	a.botCfg.Pricing.Stars[6] = 460
	a.botCfg.Pricing.Traffic[6] = 100
	a.mu.Unlock()
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	// Первая правка — флип и пересборка сетки зеркалом.
	if err := a.setPlanPrice(ctx, "", 1, "base", "999"); err != nil {
		t.Fatal(err)
	}
	pr := a.pricing()
	if pr.Base[2] != "260" || pr.Fiat(model.PayMethodYooKassa, 2) != "280" {
		t.Fatalf("легаси-месяц стёрт разворотом: base=%q yk=%q — автосписание на 2 мес упадёт", pr.Base[2], pr.Fiat(model.PayMethodYooKassa, 2))
	}
	if pr.P2P[6] != "610" || pr.Stars[6] != 460 || pr.Traffic[6] != 100 {
		t.Fatalf("настройки снятого с продажи срока стёрты: %q/%d/%d", pr.P2P[6], pr.Stars[6], pr.Traffic[6])
	}

	// Легаси-месяц редактируется из карточки: экран открывается, цена правится.
	fm := &fakeMsg{}
	a.msg = fm
	planTap(t, a, "pln:prm:2:"+model.PlanCodeBase)
	if !strings.Contains(fm.last(), "2 мес") {
		t.Fatalf("экран легаси-месяца не открылся: %q", fm.last())
	}
	planTap(t, a, "pln:in:b:2:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, "270"))
	if got := a.pricing().Base[2]; got != "270" {
		t.Fatalf("легаси-месяц не правится: %q", got)
	}

	// Прочерк на отсутствующем месяце длительность не создаёт.
	p, _ := st.GetPlan(ctx, model.PlanCodeBase)
	before := len(p.Durations)
	if err := a.setPlanPrice(ctx, "", 3, "p2p", "-"); err != nil {
		t.Fatal(err)
	}
	p, _ = st.GetPlan(ctx, model.PlanCodeBase)
	if len(p.Durations) != before {
		t.Fatalf("прочерк создал длительность: было %d, стало %d", before, len(p.Durations))
	}
}

// Пустая валюта тарифа не воюет с нормализацией: NormalizePricing заполняет
// пустую валюту сетки из легаси-полей при каждом чтении, и зеркало, пишущее
// «пусто» поверх, устраивало бы качели с ложным «цены восстановлены» на каждом
// старте.
func TestEmptyPlanCurrencyDoesNotLoop(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	a.mu.Lock()
	a.botCfg.Pricing.Currency = ""
	a.botCfg.P2P.Currency = "RUB" // легаси-поле, из которого нормализация берёт валюту
	a.mu.Unlock()
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.setPlanPrice(ctx, "", 1, "base", "999"); err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetPlan(ctx, model.PlanCodeBase)
	if p.FromConfig {
		t.Fatal("тариф не стал ведущим")
	}

	// Чтение цен нормализует живой конфиг — валюта воскресает из легаси-поля.
	if got := a.pricing().Currency; got != "RUB" {
		t.Fatalf("нормализация валюты сломана: %q", got)
	}
	// «Рестарт» дважды: содержательных отличий нет, лечение молчит оба раза.
	for i := 0; i < 2; i++ {
		changed, err := a.syncPlansConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			t.Fatalf("рестарт %d: ложное «цены восстановлены» на ровном месте", i+1)
		}
		if got := a.pricing().Currency; got != "RUB" {
			t.Fatalf("рестарт %d: зеркало затёрло валюту: %q", i+1, got)
		}
	}
}

// Сбой записи КОНФИГА после записи тарифа — не отказ правки: тариф записан,
// сетка в памяти зеркальная, витрина продаёт по новой цене. Сказать админу
// «не сохранилось» значило бы соврать в обратную сторону.
func TestMirrorSaveFailureIsNotEditFailure(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.failSaveConfig = 1
	st.mu.Unlock()

	if err := a.setPlanPrice(ctx, "", 1, "base", "888"); err != nil {
		t.Fatalf("правка объявлена несостоявшейся, хотя тариф записан: %v", err)
	}
	p, _ := st.GetPlan(ctx, model.PlanCodeBase)
	if d := p.Duration(1); d == nil || d.Base != "888" {
		t.Fatalf("тариф не записан: %+v", p.Durations)
	}
	if got := a.pricing().Base[1]; got != "888" {
		t.Fatalf("сетка в памяти не зеркальная: %q", got)
	}
	// Следующее сохранение конфига дописывает сетку в базу.
	if err := a.saveBotConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if got := st.cfg.Pricing.Base[1]; got != "888" {
		t.Fatalf("сетка в базе не догнала: %q", got)
	}
}

// Контекст тарифа не утекает на старые экраны: взведённый в карточке ввод,
// брошенный ради экрана Stars или ЮKassa, не отправляет их цены в чужой тариф.
func TestPlanCodeDoesNotLeakIntoLegacyScreens(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	fm := &fakeMsg{}
	a.msg = fm
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	planTap(t, a, "pln:dup:"+model.PlanCodeBase)
	list, _ := st.ListPlans(ctx)
	code := ""
	for i := range list {
		if list[i].Code != model.PlanCodeBase {
			code = list[i].Code
		}
	}

	// Взвели ввод цены в карточке копии и бросили его ради экрана Stars.
	planTap(t, a, "pln:in:b:1:"+code)
	planTap(t, a, "star:price:1")
	a.handleMessage(ctx, msgText(planAdmin, "77"))
	p, _ := st.GetPlan(ctx, code)
	if d := p.Duration(1); d != nil && d.Stars == 77 {
		t.Fatal("цена Stars ушла в чужой тариф")
	}
	if got := a.pricing().StarPrice(1); got != 77 {
		t.Fatalf("цена Stars не дошла до «Базового»: %d", got)
	}

	// То же для ЮKassa.
	planTap(t, a, "pln:in:b:1:"+code)
	planTap(t, a, "yk:price:3")
	a.handleMessage(ctx, msgText(planAdmin, "444"))
	p, _ = st.GetPlan(ctx, code)
	if d := p.Duration(3); d != nil && d.YooKassa == "444" {
		t.Fatal("цена ЮKassa ушла в чужой тариф")
	}
	if got := a.pricing().Fiat(model.PayMethodYooKassa, 3); got != "444" {
		t.Fatalf("цена ЮKassa не дошла до «Базового»: %q", got)
	}

	// Ошибка разбора цены Stars тоже снимает контекст.
	planTap(t, a, "pln:in:s:1:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "abc"))
	if got := a.getUI(planAdmin).planCode; got != "" {
		t.Fatalf("контекст пережил ошибку разбора: %q", got)
	}
}

// Навигация по тарифам снимает взведённый коммерческий вопрос: брошенный ввод
// цены не должен дожидаться случайного текста.
func TestPlanNavigationForgetsCommercialInput(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	fm := &fakeMsg{}
	a.msg = fm
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetPlan(ctx, model.PlanCodeBase)

	planTap(t, a, "pln:in:b:1:"+model.PlanCodeBase)
	planTap(t, a, "pln:open:"+model.PlanCodeBase) // ушёл с экрана ввода
	a.handleMessage(ctx, msgText(planAdmin, "123456"))

	after, _ := st.GetPlan(ctx, model.PlanCodeBase)
	if d := after.Duration(1); d.Base != before.Duration(1).Base {
		t.Fatalf("брошенный вопрос записал цену: %q", d.Base)
	}

	// И тумблер сквада тоже снимает вопрос.
	planTap(t, a, "pln:in:b:1:"+model.PlanCodeBase)
	planTap(t, a, "plq:i:0:"+planEditHash(model.PlanCodeBase)+":squad-y")
	a.handleMessage(ctx, msgText(planAdmin, "654321"))
	after, _ = st.GetPlan(ctx, model.PlanCodeBase)
	if d := after.Duration(1); d.Base != before.Duration(1).Base {
		t.Fatalf("тумблер не снял вопрос, текст записал цену: %q", d.Base)
	}
}

// Сквады выключаются так же, как включаются, — и на уровне тарифа, и у срока;
// пустое переопределение срока снимается целиком (наследование возвращается),
// сброс переопределения чистит и внешний сквад.
func TestPlanSquadToggleOffAndClear(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	fm := &fakeMsg{}
	a.msg = fm
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	// Уровень тарифа: включили-выключили.
	for _, on := range []bool{true, false} {
		if err := a.togglePlanSquad(ctx, "", 0, "sq-t", false); err != nil {
			t.Fatal(err)
		}
		if err := a.togglePlanSquad(ctx, "", 0, "ext-t", true); err != nil {
			t.Fatal(err)
		}
		p, _ := st.GetPlan(ctx, model.PlanCodeBase)
		hasInt := false
		for _, u := range p.IntSquads {
			if u == "sq-t" {
				hasInt = true
			}
		}
		if hasInt != on || (p.ExtSquad == "ext-t") != on {
			t.Fatalf("тумблер уровня тарифа: ожидалось %v, int=%v ext=%q", on, hasInt, p.ExtSquad)
		}
	}

	// Срок: последний выключенный сквад снимает переопределение целиком.
	if err := a.togglePlanSquad(ctx, "", 1, "sq-d", false); err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetPlan(ctx, model.PlanCodeBase)
	if d := p.Duration(1); d == nil || d.IntSquads == nil {
		t.Fatal("переопределение срока не создано")
	}
	if err := a.togglePlanSquad(ctx, "", 1, "sq-d", false); err != nil {
		t.Fatal(err)
	}
	p, _ = st.GetPlan(ctx, model.PlanCodeBase)
	if d := p.Duration(1); d.IntSquads != nil {
		t.Fatalf("пустое переопределение осталось: %+v — срок продавался бы без сквадов вместо наследования", d.IntSquads)
	}

	// Сброс переопределения чистит и внутренние, и внешний.
	if err := a.togglePlanSquad(ctx, "", 12, "sq-d", false); err != nil {
		t.Fatal(err)
	}
	if err := a.togglePlanSquad(ctx, "", 12, "ext-d", true); err != nil {
		t.Fatal(err)
	}
	if err := a.clearPlanSquadOverride(ctx, "", 12); err != nil {
		t.Fatal(err)
	}
	p, _ = st.GetPlan(ctx, model.PlanCodeBase)
	if d := p.Duration(12); d.IntSquads != nil || d.ExtSquad != nil {
		t.Fatalf("сброс не снял переопределение: %+v/%+v", d.IntSquads, d.ExtSquad)
	}

	// Подделанный месяц в тумблере длительность-призрак не создаёт.
	a.getUI(planAdmin).planEdit = model.PlanCodeBase
	planTap(t, a, "plq:i:99:"+planEditHash(model.PlanCodeBase)+":rogue")
	p, _ = st.GetPlan(ctx, model.PlanCodeBase)
	if p.Duration(99) != nil {
		t.Fatal("подделанный месяц завёл длительность-призрак")
	}
}

// Лечение на старте не смеет ронять процесс: при загрузке конфига мессенджера
// ещё нет, уведомление ждёт запуска бота и уходит первым сообщением.
func TestHealNoticeDeferredUntilRun(t *testing.T) {
	ctx := context.Background()
	a, _ := planSyncApp(t, 0)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.setPlanPrice(ctx, "", 1, "base", "999"); err != nil {
		t.Fatal(err)
	}
	// «Откат»: сетку правят в обход тарифа; на старте мессенджера нет.
	a.mu.Lock()
	a.botCfg.Pricing.Base[1] = "111"
	a.mu.Unlock()
	a.msg = nil

	changed, err := a.syncPlansConfig(ctx)
	if err != nil || !changed {
		t.Fatalf("лечение не сработало: %v/%v", changed, err)
	}
	// Ровно так ведёт себя стартовый путь: флаг вместо прямой отправки.
	a.mu.Lock()
	a.healNotice = true
	a.mu.Unlock()

	// Запуск: мессенджер появился, уведомление ушло один раз.
	fm := &fakeMsg{}
	a.msg = fm
	a.sendHealNotice(ctx)
	if !strings.Contains(fm.last(), "восстановлены") {
		t.Fatalf("уведомление о лечении не ушло: %q", fm.last())
	}
	n := len(fm.texts)
	a.sendHealNotice(ctx)
	if len(fm.texts) != n {
		t.Fatal("уведомление ушло повторно")
	}
}

// Цены снятых с продажи сроков не немые: по переопределению без базовой цены
// продлевает автосписание, и его изменение на откате должно поднимать
// уведомление при лечении.
func TestHealNoticesOffSalePriceChange(t *testing.T) {
	ctx := context.Background()
	a, _ := planSyncApp(t, 0)
	a.mu.Lock()
	a.botCfg.Pricing.YooKassa[2] = "260" // переопределение без базовой цены
	a.mu.Unlock()
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.setPlanPrice(ctx, "", 1, "base", "999"); err != nil {
		t.Fatal(err)
	}
	// Первое лечение после флипа — канонизация, молчит.
	if changed, err := a.syncPlansConfig(ctx); err != nil || changed {
		t.Fatalf("канонизация подняла уведомление: %v/%v", changed, err)
	}

	// «Откат»: старый образ поменял цену продления снятого с продажи срока.
	a.mu.Lock()
	a.botCfg.Pricing.YooKassa[2] = "999"
	a.mu.Unlock()
	changed, err := a.syncPlansConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("смена цены автопродления прошла без уведомления")
	}
	if got := a.pricing().Fiat(model.PayMethodYooKassa, 2); got != "260" {
		t.Fatalf("цена автопродления не восстановлена из тарифа: %q", got)
	}
}

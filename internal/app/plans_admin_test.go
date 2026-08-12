package app

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// planAdminApp — приложение с сообщениями: экраны админки тарифов проверяются
// через настоящий разбор callback'ов, а не вызовом внутренних функций. Именно
// разбор ломается чаще всего: кнопка есть, а маршрута к ней нет.
func planAdminApp(t *testing.T) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	a, fs := planApp(t)
	fm := &fakeMsg{}
	a.msg = fm
	return a, fm, fs
}

const planAdmin = int64(100)

func planTap(t *testing.T, a *App, data string) {
	t.Helper()
	a.handleCallback(context.Background(), cb(planAdmin, data))
}

// Список тарифов открывается кнопкой из «Продаж» и ведёт в карточку.
func TestPlansAdmin_ListAndCard(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	planTap(t, a, "menu:plans")
	if !strings.Contains(fm.last(), "Тарифы") {
		t.Fatalf("экран тарифов не открылся: %q", fm.last())
	}
	var openCB string
	for _, d := range fm.allCallbackData() {
		if strings.HasPrefix(d, "pln:open:") {
			openCB = d
		}
	}
	if openCB != "pln:open:"+model.PlanCodeBase {
		t.Fatalf("в списке нет тарифа «Базовый»: %v", fm.allCallbackData())
	}

	planTap(t, a, openCB)
	card := fm.last()
	// Карточка обязана показывать код (он уходит в ссылки и метаданные),
	// состояние и то, что тариф продаёт.
	for _, want := range []string{model.PlanCodeBase, "Базовый", "150"} {
		if !strings.Contains(card, want) {
			t.Fatalf("карточка не показывает %q: %q", want, card)
		}
	}
}

// Новый тариф создаётся выключенным, со своим кодом, в конце списка и НЕ ведомым
// от сетки цен: пересобирается из конфига ровно один тариф — «Базовый».
func TestPlansAdmin_NewPlanIsDisabledAndOwnCode(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	planTap(t, a, "pln:new")
	list, _ := fs.ListPlans(ctx)
	if len(list) != 2 {
		t.Fatalf("ожидался второй тариф, есть %d", len(list))
	}
	var fresh *model.Plan
	for i := range list {
		if list[i].Code != model.PlanCodeBase {
			fresh = &list[i]
		}
	}
	if fresh == nil {
		t.Fatal("новый тариф не найден")
	}
	if fresh.Enabled {
		t.Fatal("новый тариф включён — он ничего не продаёт, включать его нельзя")
	}
	if fresh.FromConfig {
		t.Fatal("новый тариф помечен ведомым от конфига")
	}
	if len(fresh.Code) != planCodeLen {
		t.Fatalf("код тарифа неожиданной длины: %q", fresh.Code)
	}
	if !model.ValidPlanCode(fresh.Code) {
		t.Fatalf("код тарифа не проходит проверку: %q", fresh.Code)
	}
	if fresh.Order <= 0 {
		t.Fatalf("новый тариф встал не в конец списка: порядок %d", fresh.Order)
	}

	// Пересборка «Базового» из конфига чужой тариф не трогает.
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := fs.GetPlan(ctx, fresh.Code); again == nil {
		t.Fatal("новый тариф исчез после синхронизации «Базового»")
	}
}

// Дублирование переносит условия, но не переносит ни код, ни включённость.
func TestPlansAdmin_DuplicateCopiesTerms(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	base, _ := fs.GetPlan(ctx, model.PlanCodeBase)

	planTap(t, a, "pln:dup:"+model.PlanCodeBase)
	list, _ := fs.ListPlans(ctx)
	var copy_ *model.Plan
	for i := range list {
		if list[i].Code != model.PlanCodeBase {
			copy_ = &list[i]
		}
	}
	if copy_ == nil {
		t.Fatal("копия не создана")
	}
	if copy_.Code == base.Code {
		t.Fatal("копия получила код источника")
	}
	if copy_.Enabled {
		t.Fatal("копия включена")
	}
	if len(copy_.Durations) != len(base.Durations) {
		t.Fatalf("копия потеряла длительности: %d против %d", len(copy_.Durations), len(base.Durations))
	}
	if copy_.Name == base.Name {
		t.Fatal("копия и источник неотличимы по имени")
	}
}

// Переопределения лимитов у длительностей — указатели, и копия обязана владеть
// своими значениями: иначе правка копии молча меняла бы условия источника.
func TestClonePlanDurations_OwnsOverrides(t *testing.T) {
	gb, dev, ext := 100, 4, "ext"
	src := []model.PlanDuration{{
		Months: 1, Base: "150",
		TrafficGB: &gb, DeviceLimit: &dev,
		IntSquads: &[]string{"one"}, ExtSquad: &ext,
	}}
	out := clonePlanDurations(src)
	*out[0].TrafficGB = 1
	*out[0].DeviceLimit = 1
	(*out[0].IntSquads)[0] = "other"
	*out[0].ExtSquad = "other"

	if gb != 100 || dev != 4 || ext != "ext" {
		t.Fatal("копия делит переопределения с источником")
	}
	if (*src[0].IntSquads)[0] != "one" {
		t.Fatal("копия делит набор сквадов с источником")
	}
}

// Оформление тарифа правится сейчас, потому что синхронизация из конфига его не
// перезаписывает. Проверяем именно это: имя, описание, значок, порядок и
// включённость обязаны переживать пересборку «Базового».
func TestPlansAdmin_LookSurvivesConfigSync(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	planTap(t, a, "pln:name:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, "Стандарт"))
	planTap(t, a, "pln:desc:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, "для всех"))
	planTap(t, a, "pln:icon:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, "🚀"))
	planTap(t, a, "pln:toggle:"+model.PlanCodeBase)

	check := func(stage string) {
		p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
		if p == nil {
			t.Fatalf("%s: тариф исчез", stage)
		}
		if p.Name != "Стандарт" || p.Description != "для всех" || p.Icon != "🚀" {
			t.Fatalf("%s: оформление потеряно: %+v", stage, p)
		}
		if p.Enabled {
			t.Fatalf("%s: выключенный тариф снова включён", stage)
		}
	}
	check("сразу после правки")

	// Сохранение конфига пересобирает «Базового» из сетки цен — оформление
	// обязано остаться.
	if err := a.saveBotConfig(ctx); err != nil {
		t.Fatal(err)
	}
	check("после сохранения конфига")

	// И цены при этом продолжают доезжать из конфига в тариф: направление
	// синхронизации на этом шаге не менялось.
	if err := a.setPlanPrice(ctx, "", 1, "base", "199"); err != nil {
		t.Fatal(err)
	}
	p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	d := p.Duration(1)
	if d == nil || d.Base != "199" {
		t.Fatalf("цена не записалась в тариф: %+v", p.Durations)
	}
	// И доехала зеркалом до сетки, по которой продаёт витрина.
	if got := a.pricing().Base[1]; got != "199" {
		t.Fatalf("цена не доехала зеркалом до сетки: %q", got)
	}
	check("после правки цены")
}

// Имя в снимке сделки берётся из тарифа, а не из старого значения в памяти:
// переименовали тариф — со следующей продажи в снимке новое имя.
func TestPlansAdmin_RenameReachesSnapshot(t *testing.T) {
	ctx := context.Background()
	a, _, _ := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	planTap(t, a, "pln:name:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, "Стандарт"))

	snap := a.planSnapshot(1)
	if snap == nil || snap.Name != "Стандарт" {
		t.Fatalf("снимок сделки остался с прежним именем: %+v", snap)
	}
}

// Порядок: «выше» и «ниже» меняют соседей местами, в том числе когда номера
// совпадают (их оставляет и миграция, и импорт).
func TestPlansAdmin_MoveSwapsNeighbours(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	first := &model.Plan{Code: "aaa", Name: "A", Order: 0, Availability: model.PlanAvailAll}
	second := &model.Plan{Code: "bbb", Name: "B", Order: 0, Availability: model.PlanAvailAll}
	if err := fs.SavePlan(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := fs.SavePlan(ctx, second); err != nil {
		t.Fatal(err)
	}

	planTap(t, a, "pln:down:aaa")
	list, _ := fs.ListPlans(ctx)
	if list[0].Code != "bbb" || list[1].Code != "aaa" {
		t.Fatalf("порядок не поменялся: %s, %s", list[0].Code, list[1].Code)
	}

	planTap(t, a, "pln:up:aaa")
	list, _ = fs.ListPlans(ctx)
	if list[0].Code != "aaa" || list[1].Code != "bbb" {
		t.Fatalf("порядок не вернулся: %s, %s", list[0].Code, list[1].Code)
	}

	// Крайний тариф выше не поднимается и порядок не портит.
	planTap(t, a, "pln:up:aaa")
	list, _ = fs.ListPlans(ctx)
	if list[0].Code != "aaa" || list[1].Code != "bbb" {
		t.Fatalf("порядок сломан на краю списка: %s, %s", list[0].Code, list[1].Code)
	}
}

// «Базовый» не удаляется: он мост к сетке цен в конфиге, по которой продаёт
// предыдущий образ бота после откатa. Кнопки удаления у него нет, но нажатие на
// старое сообщение обязано быть отбито тоже.
func TestPlansAdmin_BasePlanNotDeletable(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	planTap(t, a, "pln:open:"+model.PlanCodeBase)
	for _, d := range fm.allCallbackData() {
		if strings.HasPrefix(d, "pln:del") {
			t.Fatalf("в карточке «Базового» есть кнопка удаления: %s", d)
		}
	}

	planTap(t, a, "pln:delyes:"+model.PlanCodeBase)
	if p, _ := fs.GetPlan(ctx, model.PlanCodeBase); p == nil {
		t.Fatal("«Базовый» удалён прямым callback'ом")
	}
}

// Копию удалить можно, и подтверждение спрашивается до удаления.
func TestPlansAdmin_DeleteCopyAsksFirst(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	planTap(t, a, "pln:dup:"+model.PlanCodeBase)
	list, _ := fs.ListPlans(ctx)
	code := ""
	for i := range list {
		if list[i].Code != model.PlanCodeBase {
			code = list[i].Code
		}
	}

	planTap(t, a, "pln:del:"+code)
	if p, _ := fs.GetPlan(ctx, code); p == nil {
		t.Fatal("тариф удалён без подтверждения")
	}
	if !strings.Contains(fm.last(), "Удалить тариф") {
		t.Fatalf("подтверждение не показано: %q", fm.last())
	}

	planTap(t, a, "pln:delyes:"+code)
	if p, _ := fs.GetPlan(ctx, code); p != nil {
		t.Fatal("тариф не удалён после подтверждения")
	}
}

// Имя тарифа вводит человек, а экраны уходят с разметкой HTML: неэкранированное
// «<VIP>» Telegram считает неизвестным тегом и отвечает ошибкой — карточка не
// отправляется вообще, а кнопка «Имя» живёт только в ней, то есть выбраться
// нельзя. В тексте — экранированное значение, в подписи кнопки — исходное:
// подписи Telegram принимает обычным текстом.
func TestPlansAdmin_NameIsEscapedInText(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	planTap(t, a, "pln:name:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, "Тариф <VIP> & Co"))

	card := fm.last()
	if strings.Contains(card, "<VIP>") {
		t.Fatalf("имя не экранировано, карточка не отправится: %q", card)
	}
	if !strings.Contains(card, "&lt;VIP&gt;") || !strings.Contains(card, "&amp;") {
		t.Fatalf("имя потеряно при экранировании: %q", card)
	}

	planTap(t, a, "pln:list")
	raw := false
	for _, l := range fm.buttonLabels() {
		if strings.Contains(l, "<VIP>") {
			raw = true
		}
	}
	if !raw {
		t.Fatalf("в подписи кнопки имя должно быть как есть: %v", fm.buttonLabels())
	}
}

// Границы длины: имя уезжает в снимок условий сделки (а он лежит в каждой
// строке платежей и счетов), описание — в подпись под баннером, у которой предел
// у Telegram. Слишком длинное значение отклоняется, прежнее остаётся.
func TestPlansAdmin_FieldLimits(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := fs.GetPlan(ctx, model.PlanCodeBase)

	planTap(t, a, "pln:name:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, strings.Repeat("я", planNameMaxLen+1)))
	if !strings.Contains(fm.last(), "Слишком длинно") {
		t.Fatalf("длинное имя принято молча: %q", fm.last())
	}
	after, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	if after.Name != before.Name {
		t.Fatalf("имя изменилось на отклонённое значение: %q", after.Name)
	}

	planTap(t, a, "pln:desc:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, strings.Repeat("о", planDescMaxLen+1)))
	after, _ = fs.GetPlan(ctx, model.PlanCodeBase)
	if after.Description != "" {
		t.Fatalf("длинное описание сохранено: %d символов", len([]rune(after.Description)))
	}

	// Ровно по границе — принимается.
	planTap(t, a, "pln:name:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, strings.Repeat("я", planNameMaxLen)))
	after, _ = fs.GetPlan(ctx, model.PlanCodeBase)
	if len([]rune(after.Name)) != planNameMaxLen {
		t.Fatalf("значение по границе отклонено: %q", after.Name)
	}

	// Имя в одну строку: перевод строки в подписи кнопки превращается в мусор.
	planTap(t, a, "pln:name:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, "Первая\nвторая"))
	after, _ = fs.GetPlan(ctx, model.PlanCodeBase)
	if after.Name != "Первая" {
		t.Fatalf("имя осталось многострочным: %q", after.Name)
	}
}

// Тариф без сроков и цен включить нельзя: в витрине он либо пустой, либо
// продаётся по пустой цене. Ровно поэтому новый тариф и создаётся выключенным.
func TestPlansAdmin_CannotEnableEmptyPlan(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	planTap(t, a, "pln:new")
	list, _ := fs.ListPlans(ctx)
	code := list[0].Code

	planTap(t, a, "pln:toggle:"+code)
	p, _ := fs.GetPlan(ctx, code)
	if p.Enabled {
		t.Fatal("тариф без сроков включён")
	}
	if !strings.Contains(fm.last(), "включать нечего") {
		t.Fatalf("причина отказа не показана: %q", fm.last())
	}
}

// Порядок при равных номерах: их оставляют и миграция, и импорт. Одно нажатие
// обязано двигать тариф ровно на одну позицию, а номера — становиться
// последовательными.
func TestPlansAdmin_MoveWithEqualOrders(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	for _, code := range []string{"aaa", "bbb", "ccc"} {
		if err := fs.SavePlan(ctx, &model.Plan{Code: code, Name: code, Availability: model.PlanAvailAll}); err != nil {
			t.Fatal(err)
		}
	}

	planTap(t, a, "pln:up:ccc")
	list, _ := fs.ListPlans(ctx)
	got := []string{list[0].Code, list[1].Code, list[2].Code}
	want := []string{"aaa", "ccc", "bbb"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("тариф перепрыгнул позицию: %v, ожидалось %v", got, want)
		}
	}
	for i := range list {
		if list[i].Order != i {
			t.Fatalf("номера не стали последовательными: %d у %s", list[i].Order, list[i].Code)
		}
	}
}

// Код тарифа генерируется без перекоса. Проверка не «отклонение не больше
// стольких процентов» (такой порог перекос по модулю не видит: 256 на 27 не
// делится, преимущество первых символов всего около пяти процентов), а критерий
// хи-квадрат — он на этих объёмах отличает перекос от случайности с огромным
// запасом: у ровного распределения ожидаемое значение около 26, у перекошенного
// счёт идёт на сотни.
func TestNewPlanCode_NoModuloBias(t *testing.T) {
	// Длина и алфавит проверяются литералами намеренно: сверка с теми же
	// константами, из которых код и собран, не проверяет ничего.
	if planCodeLen != 12 {
		t.Fatalf("длина кода изменилась: %d — пересчитайте запас на перебор", planCodeLen)
	}
	if len([]rune(planCodeAlphabet)) != 27 {
		t.Fatalf("алфавит кода изменился: %d символов — пересчитайте запас на перебор", len([]rune(planCodeAlphabet)))
	}

	seen := map[rune]int{}
	const codes = 8000
	for i := 0; i < codes; i++ {
		code, err := newPlanCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 12 || !model.ValidPlanCode(code) {
			t.Fatalf("негодный код: %q", code)
		}
		for _, r := range code {
			if !strings.ContainsRune(planCodeAlphabet, r) {
				t.Fatalf("символ вне алфавита: %q в %q", r, code)
			}
			seen[r]++
		}
	}
	if len(seen) != len([]rune(planCodeAlphabet)) {
		t.Fatalf("часть алфавита не встречается: %d из %d", len(seen), len([]rune(planCodeAlphabet)))
	}

	buckets := float64(len([]rune(planCodeAlphabet)))
	expect := float64(codes*12) / buckets
	chi := 0.0
	for _, n := range seen {
		d := float64(n) - expect
		chi += d * d / expect
	}
	// 26 степеней свободы: ровное распределение даёт около 26, порог 100
	// достигается случайно с вероятностью порядка одной миллионной, а перекос по
	// модулю даёт сотни.
	if chi > 100 {
		t.Fatalf("распределение символов кода перекошено: хи-квадрат %.0f при ожидаемых ~26", chi)
	}
}

// Тарифы — админский раздел: у обычного пользователя ни экран, ни действия не
// должны открываться.
func TestPlansAdmin_NotForRegularUser(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := fs.ListPlans(ctx)

	a.handleCallback(ctx, cb(777, "menu:plans"))
	a.handleCallback(ctx, cb(777, "pln:new"))
	a.handleCallback(ctx, cb(777, "pln:toggle:"+model.PlanCodeBase))
	a.handleCallback(ctx, cb(777, "pln:delyes:"+model.PlanCodeBase))

	after, _ := fs.ListPlans(ctx)
	if len(after) != len(before) {
		t.Fatalf("обычный пользователь изменил список тарифов: было %d, стало %d", len(before), len(after))
	}
	if p, _ := fs.GetPlan(ctx, model.PlanCodeBase); p == nil || !p.Enabled {
		t.Fatal("обычный пользователь выключил или удалил тариф")
	}
	if strings.Contains(fm.last(), "Тарифы") {
		t.Fatalf("обычному пользователю показали админский экран: %q", fm.last())
	}
}

// Экранируется всё, что человек когда-либо вводил руками, а не только имя:
// описание, значок и валюта тоже попадают в текст карточки, и незакрытый тег в
// любом из них — это отказ Telegram отправить сообщение, то есть карточка, из
// которой уже не выбраться.
func TestPlansAdmin_AllHandTypedFieldsEscaped(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	planTap(t, a, "pln:desc:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, "до 5 <устройств> & быстро"))
	planTap(t, a, "pln:icon:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, "<b>"))
	// Валюта — свободный ввод админа на экране цен, и она едет в строку сроков.
	if err := a.setPlanCurrency(ctx, model.PlanCodeBase, "<b>USD"); err != nil {
		t.Fatal(err)
	}
	planTap(t, a, "pln:open:"+model.PlanCodeBase)

	card := fm.last()
	for _, raw := range []string{"<устройств>", "<b>USD"} {
		if strings.Contains(card, raw) {
			t.Fatalf("значение %q не экранировано, карточка не отправится: %q", raw, card)
		}
	}
	if !strings.Contains(card, "&lt;устройств&gt;") || !strings.Contains(card, "&amp;") {
		t.Fatalf("описание потеряно при экранировании: %q", card)
	}
	// Разметка самой карточки при этом остаётся разметкой.
	if !strings.Contains(card, "<b>") || !strings.Contains(card, "<code>") {
		t.Fatalf("экранирование съело разметку карточки: %q", card)
	}
}

// Прочерк стирает описание и значок, пустой ввод не трогает ничего, а имя не
// стирается ни тем, ни другим: безымянный тариф в витрине читается как сбой.
func TestPlansAdmin_ClearAndEmptyInput(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	set := func(action, text string) {
		planTap(t, a, "pln:"+action+":"+model.PlanCodeBase)
		a.handleMessage(ctx, msgText(planAdmin, text))
	}
	plan := func() *model.Plan {
		p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
		return p
	}

	set("desc", "описание")
	set("icon", "🚀")
	if p := plan(); p.Description != "описание" || p.Icon != "🚀" {
		t.Fatalf("значения не сохранены: %+v", p)
	}

	// Пустой ввод (в том числе из одних пробелов) значения не трогает.
	set("desc", "   ")
	set("icon", "  ")
	if p := plan(); p.Description != "описание" || p.Icon != "🚀" {
		t.Fatalf("пробельный ввод стёр значение: %+v", p)
	}

	set("desc", "-")
	set("icon", "-")
	if p := plan(); p.Description != "" || p.Icon != "" {
		t.Fatalf("прочерк не стёр значения: %+v", p)
	}

	// Имя: ни пусто, ни прочерк — и об отказе говорится вслух.
	before := plan().Name
	for _, text := range []string{"", "   ", "-"} {
		set("name", text)
		if p := plan(); p.Name != before {
			t.Fatalf("имя стёрто вводом %q: %q", text, p.Name)
		}
		if !strings.Contains(fm.last(), "должно быть имя") {
			t.Fatalf("отказ по имени (%q) не показан: %q", text, fm.last())
		}
	}
}

// Отклонённое по длине значение не должно стирать прежнее, а законное значение
// по границе — приниматься. Значок ограничен своей границей, не именной.
func TestPlansAdmin_LimitsKeepPreviousValue(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	set := func(action, text string) {
		planTap(t, a, "pln:"+action+":"+model.PlanCodeBase)
		a.handleMessage(ctx, msgText(planAdmin, text))
	}
	plan := func() *model.Plan {
		p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
		return p
	}

	set("desc", "живое описание")
	set("desc", strings.Repeat("о", planDescMaxLen+1))
	if p := plan(); p.Description != "живое описание" {
		t.Fatalf("отклонённое описание затёрло прежнее: %q", p.Description)
	}
	// Ровно по границе — принимается, и многострочность сохраняется.
	long := strings.Repeat("о", planDescMaxLen-2) + "\nх"
	set("desc", long)
	if p := plan(); p.Description != long {
		t.Fatalf("описание по границе отклонено или потеряло строки: %d символов", len([]rune(p.Description)))
	}

	set("icon", "🚀")
	set("icon", strings.Repeat("э", planIconMaxLen+1))
	if p := plan(); p.Icon != "🚀" {
		t.Fatalf("значок принял значение длиннее своей границы: %q", p.Icon)
	}
}

// Дублирование: копия обязана быть самостоятельной — свой код, не ведомая от
// конфига, со своими датами и со своими лимитами, а не общими с источником.
func TestPlansAdmin_DuplicateIsIndependent(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	base, _ := fs.GetPlan(ctx, model.PlanCodeBase)

	planTap(t, a, "pln:dup:"+model.PlanCodeBase)
	list, _ := fs.ListPlans(ctx)
	var cp *model.Plan
	for i := range list {
		if list[i].Code != model.PlanCodeBase {
			cp = &list[i]
		}
	}
	if cp == nil {
		t.Fatal("копия не создана")
	}
	if cp.FromConfig {
		t.Fatal("копия помечена ведомой от сетки цен — синхронизация «Базового» её затрёт")
	}
	if cp.CreatedAt == base.CreatedAt && base.CreatedAt != "" {
		t.Fatal("копия унаследовала дату создания источника")
	}
	// Переопределения лимитов у длительностей — указатели; общие указатели
	// означали бы, что правка копии молча меняет условия источника.
	bd, cd := base.Duration(12), cp.Duration(12)
	if bd == nil || cd == nil {
		t.Fatalf("длительность 12 мес потерялась: источник %+v, копия %+v", base.Durations, cp.Durations)
	}
	if bd.IntSquads != nil && cd.IntSquads != nil && &(*bd.IntSquads)[0] == &(*cd.IntSquads)[0] {
		t.Fatal("копия делит набор сквадов длительности с источником")
	}
	if len(base.IntSquads) > 0 && len(cp.IntSquads) > 0 && &base.IntSquads[0] == &cp.IntSquads[0] {
		t.Fatal("копия делит сквады тарифа с источником")
	}

	// И имя копии отличимо от имени источника даже у имени по границе длины.
	longName := strings.Repeat("я", planNameMaxLen)
	planTap(t, a, "pln:name:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, longName))
	planTap(t, a, "pln:dup:"+model.PlanCodeBase)
	list, _ = fs.ListPlans(ctx)
	for i := range list {
		if list[i].Code == model.PlanCodeBase {
			continue
		}
		if list[i].Name == longName {
			t.Fatalf("копия неотличима от источника по имени: %q", list[i].Name)
		}
		if len([]rune(list[i].Name)) > planNameMaxLen {
			t.Fatalf("имя копии длиннее границы: %d символов", len([]rune(list[i].Name)))
		}
	}
}

// Список листается, а действие возвращает на ту же страницу: иначе протащить
// тариф вверх с третьей страницы значило бы листать заново после каждого
// нажатия.
func TestPlansAdmin_PaginationKeepsPage(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	for i := 0; i < plansPageSize*2+3; i++ {
		code := "plan" + strconv.Itoa(100+i)
		if err := fs.SavePlan(ctx, &model.Plan{Code: code, Name: code, Order: i, Availability: model.PlanAvailAll}); err != nil {
			t.Fatal(err)
		}
	}

	planTap(t, a, "menu:plans")
	if !strings.Contains(fm.last(), "страница 1 из 3") {
		t.Fatalf("первая страница не показана: %q", fm.last())
	}
	planTap(t, a, "pln:page:1")
	if !strings.Contains(fm.last(), "страница 2 из 3") {
		t.Fatalf("вторая страница не показана: %q", fm.last())
	}

	// Перестановка на второй странице оставляет админа на второй странице.
	planTap(t, a, "pln:down:plan108")
	if !strings.Contains(fm.last(), "страница 2 из 3") {
		t.Fatalf("после перестановки страница сбросилась: %q", fm.last())
	}
	list, _ := fs.ListPlans(ctx)
	if list[8].Code != "plan109" || list[9].Code != "plan108" {
		t.Fatalf("тариф не переставлен: %s, %s", list[8].Code, list[9].Code)
	}

	// Кнопки из старого сообщения не должны выносить за границы списка.
	for _, data := range []string{"pln:page:-5", "pln:page:99", "pln:page:", "pln:page:abc"} {
		planTap(t, a, data)
		if !strings.Contains(fm.last(), "из 3") {
			t.Fatalf("%s: экран сломался: %q", data, fm.last())
		}
	}
}

// Старый сводный экран цен убран: menu:pricing (кнопки из старых переписок,
// «настроить вручную» в «Продажах») ведёт в редактор цен «Базового».
func TestMenuPricing_OpensBasePlanEditor(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	planTap(t, a, "menu:pricing")
	if !strings.Contains(fm.last(), "цены и лимиты") {
		t.Fatalf("ожидался редактор цен «Базового»: %q", fm.last())
	}
}

// Экран удаления показывает, сколько людей живёт на тарифе: их условия
// сохранятся, но продление и автосписания встанут.
func TestPlanDelete_ShowsSubscriberCount(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)

	// Один живёт на тарифе, у второго подписка истекла.
	_ = fs.UpsertUser(ctx, 810)
	_ = fs.SetUserSnapshot(ctx, 810, &model.PlanSnapshot{Code: p.Code, Months: 1})
	_ = fs.SetSubExpiry(ctx, 810, "2099-01-01T00:00:00Z", "sub")
	_ = fs.UpsertUser(ctx, 811)
	_ = fs.SetUserSnapshot(ctx, 811, &model.PlanSnapshot{Code: p.Code, Months: 1})
	_ = fs.SetSubExpiry(ctx, 811, "2001-01-01T00:00:00Z", "sub")

	planTap(t, a, "pln:del:"+p.Code)
	if !strings.Contains(fm.last(), "1 чел") {
		t.Fatalf("нет числа подписчиков тарифа: %q", fm.last())
	}

	// Без подписчиков ноль не печатается — он читался бы как разрешение.
	_ = fs.SetSubExpiry(ctx, 810, "2001-01-01T00:00:00Z", "sub")
	planTap(t, a, "pln:del:"+p.Code)
	if strings.Contains(fm.last(), "0 чел") {
		t.Fatalf("ноль подписчиков не должен печататься: %q", fm.last())
	}
}

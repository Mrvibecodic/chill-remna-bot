package app

import (
	"context"
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
	a.setBasePrice(1, "199")
	if err := a.saveBotConfig(ctx); err != nil {
		t.Fatal(err)
	}
	p, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	d := p.Duration(1)
	if d == nil || d.Base != "199" {
		t.Fatalf("цена из конфига не доехала до тарифа: %+v", p.Durations)
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

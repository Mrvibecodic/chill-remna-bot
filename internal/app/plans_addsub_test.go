package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// Матрица «продаётся ли опция с тарифом»: глобальный переключатель — мастер,
// режим тарифа — поверх него.
func TestPlanAddSubOn_Matrix(t *testing.T) {
	a, fs := planApp(t)
	_ = fs
	p := &model.Plan{Code: "vipvipvipvip"}

	a.botCfg.AddSub.Enabled = true
	p.AddSub = model.PlanAddSubInherit
	if !a.planAddSubOn(p) {
		t.Fatal("наследование при включённой инфраструктуре должно давать опцию")
	}
	p.AddSub = model.PlanAddSubOff
	if a.planAddSubOn(p) {
		t.Fatal("явное «выкл» должно снимать опцию")
	}
	p.AddSub = model.PlanAddSubOn
	if !a.planAddSubOn(p) {
		t.Fatal("явное «вкл» должно давать опцию")
	}

	a.botCfg.AddSub.Enabled = false
	for _, m := range []string{model.PlanAddSubInherit, model.PlanAddSubOn, model.PlanAddSubOff} {
		p.AddSub = m
		if a.planAddSubOn(p) {
			t.Fatalf("при выключенной инфраструктуре опции нет ни у кого (режим %q)", m)
		}
	}
}

// Снимок сделки несёт признак опции; продажа тарифа без опции даёт явный false.
func TestPlanSnapshot_CarriesAddSub(t *testing.T) {
	a, fs := planApp(t)
	a.botCfg.AddSub.Enabled = true

	p := vipPlan(t, fs, model.PlanAvailAll)
	p.AddSub = model.PlanAddSubOff
	_ = fs.SavePlan(context.Background(), p)
	snap := a.planSnapshotOf(p, p.Duration(1), 1)
	if snap.AddSub == nil || *snap.AddSub {
		t.Fatalf("тариф без опции обязан продаваться со снимком addsub=false: %+v", snap.AddSub)
	}
	if snap.AddSubSold() {
		t.Fatal("AddSubSold по такому снимку должен быть false")
	}

	// «Базовый» наследует глобальный переключатель.
	if err := a.syncBasePlan(context.Background()); err != nil {
		t.Fatal(err)
	}
	base := a.planSnapshot(1)
	if base.AddSub == nil || !*base.AddSub {
		t.Fatalf("«Базовый» при включённой инфраструктуре продаётся с опцией: %+v", base.AddSub)
	}

	// Старые снимки без поля — «как раньше».
	old := &model.PlanSnapshot{Months: 1}
	if !old.AddSubSold() {
		t.Fatal("снимок без поля означает «опция есть»")
	}
}

// Отпечаток условий: «опция есть» канонизируется в отсутствие поля — иначе
// обновление бота рассылало бы «условия изменились» всем подряд.
func TestPlanSnapshot_AddSubFingerprint(t *testing.T) {
	yes := true
	no := false
	oldSnap := &model.PlanSnapshot{Months: 1, Price: "150"}
	withOpt := &model.PlanSnapshot{Months: 1, Price: "150", AddSub: &yes}
	without := &model.PlanSnapshot{Months: 1, Price: "150", AddSub: &no}
	if oldSnap.Fingerprint() != withOpt.Fingerprint() {
		t.Fatal("снимок с опцией и старый снимок без поля обязаны совпадать по отпечатку")
	}
	if oldSnap.Fingerprint() == without.Fingerprint() {
		t.Fatal("продажа без опции — другие условия")
	}
}

// «Продана ли опция этому пользователю» — по снимку его сделки.
func TestAddSubSoldTo(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	a.botCfg.AddSub.Enabled = true

	// Нет пользователя/снимка — как раньше (опция есть).
	if !a.addSubSoldTo(ctx, 500) {
		t.Fatal("без снимка опция наследуется")
	}
	no := false
	_ = fs.UpsertUser(ctx, 501)
	_ = fs.SetUserSnapshot(ctx, 501, &model.PlanSnapshot{Months: 1, AddSub: &no})
	if a.addSubSoldTo(ctx, 501) {
		t.Fatal("снимок с addsub=false должен снимать опцию")
	}
	// Статус для экранов тоже гаснет (до похода в панель).
	if _, ok := a.addSubStatus(ctx, 501); ok {
		t.Fatal("экраны не должны показывать непроданную опцию")
	}
}

// Тексты опции: тариф → общие → стандартное название.
func TestAddSubTexts_Fallbacks(t *testing.T) {
	a, _ := planApp(t)
	name, desc := a.addSubTexts("ru", nil)
	if name != "Доп-сервер" || desc != "" {
		t.Fatalf("стандартные тексты: %q %q", name, desc)
	}
	a.botCfg.AddSub.Name = "Второй сервер"
	a.botCfg.AddSub.Description = "Общее описание"
	name, desc = a.addSubTexts("ru", nil)
	if name != "Второй сервер" || desc != "Общее описание" {
		t.Fatalf("общие тексты: %q %q", name, desc)
	}
	p := &model.Plan{AddSubName: "Турбо", AddSubDesc: "Свое"}
	name, desc = a.addSubTexts("ru", p)
	if name != "Турбо" || desc != "Свое" {
		t.Fatalf("тексты тарифа главнее: %q %q", name, desc)
	}
}

// Экран опции в карточке тарифа: маршрут, смена режима, свои тексты.
func TestPlanAddSubAdmin_Screen(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	a.botCfg.AddSub.Enabled = true
	p := vipPlan(t, fs, model.PlanAvailAll)

	planTap(t, a, "pln:as:"+p.Code)
	if !strings.Contains(fm.last(), "доп-подписка") {
		t.Fatalf("экран опции не открылся: %q", fm.last())
	}
	planTap(t, a, "pln:asm:off:"+p.Code)
	if got, _ := fs.GetPlan(ctx, p.Code); model.NormalizeAddSubMode(got.AddSub) != model.PlanAddSubOff {
		t.Fatalf("режим не сохранился: %q", got.AddSub)
	}
	// Продажа этого тарифа теперь без опции.
	got, _ := fs.GetPlan(ctx, p.Code)
	if snap := a.planSnapshotOf(got, got.Duration(1), 1); snap.AddSubSold() {
		t.Fatal("после «выкл» тариф продаётся без опции")
	}

	// Свои тексты через ввод; прочерк возвращает общие.
	planTap(t, a, "pln:asn:"+p.Code)
	a.handleMessage(ctx, msgText(planAdmin, "Турбо-сервер"))
	if got, _ := fs.GetPlan(ctx, p.Code); got.AddSubName != "Турбо-сервер" {
		t.Fatalf("название опции не сохранилось: %q", got.AddSubName)
	}
	planTap(t, a, "pln:asn:"+p.Code)
	a.handleMessage(ctx, msgText(planAdmin, "-"))
	if got, _ := fs.GetPlan(ctx, p.Code); got.AddSubName != "" {
		t.Fatalf("прочерк должен вернуть общее название: %q", got.AddSubName)
	}
}

// Витрина: один видимый тариф — сразу его карточка; несколько — список; тариф
// «по ссылке» в списке не показывается и по plo не открывается.
func TestShowcase_ListAndSingle(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	uid := int64(810)
	// Один тариф («Базовый») — сразу сроки его карточки.
	a.handleMessage(ctx, msgText(uid, "/buy"))
	if !hasCB(fm.allCallbackData(), "plb:base:1") {
		t.Fatalf("единственный тариф должен открываться сразу: %v", fm.allCallbackData())
	}

	// Второй видимый тариф — появляется список.
	p := vipPlan(t, fs, model.PlanAvailAll)
	a.handleMessage(ctx, msgText(uid, "/buy"))
	if !hasCB(fm.allCallbackData(), "plo:"+p.Code) || !hasCB(fm.allCallbackData(), "plo:base") {
		t.Fatalf("список тарифов не показан: %v", fm.allCallbackData())
	}
	// Карточка из списка открывается и продаёт.
	a.handleCallback(ctx, cb(uid, "plo:"+p.Code))
	if !hasCB(fm.allCallbackData(), "plb:"+p.Code+":1") {
		t.Fatalf("карточка тарифа из списка не открылась: %q", fm.last())
	}

	// Тариф «по ссылке» из списка исчезает и по plo не открывается.
	p.Availability = model.PlanAvailLink
	_ = fs.SavePlan(ctx, p)
	a.handleMessage(ctx, msgText(uid, "/buy"))
	if hasCB(fm.allCallbackData()[len(fm.allCallbackData())-3:], "plo:"+p.Code) {
		t.Fatalf("скрытый тариф не должен быть в списке")
	}
	a.handleCallback(ctx, cb(uid, "plo:"+p.Code))
	if !strings.Contains(fm.last(), "недоступен") {
		t.Fatalf("plo на скрытый тариф должен отвечать отказом: %q", fm.last())
	}
}

// Карточка тарифа показывает опцию доп-подписки только там, где она продаётся.
func TestShowcase_AddSubLineOnOffer(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	a.botCfg.AddSub.Enabled = true
	a.botCfg.AddSub.Name = "Второй сервер"
	a.botCfg.AddSub.Description = "Ещё одна подписка в той же ссылке"
	p := vipPlan(t, fs, model.PlanAvailAll)

	uid := int64(820)
	a.handleCallback(ctx, cb(uid, "plo:"+p.Code))
	if !strings.Contains(fm.last(), "Второй сервер") || !strings.Contains(fm.last(), "той же ссылке") {
		t.Fatalf("опция должна быть в карточке: %q", fm.last())
	}

	p.AddSub = model.PlanAddSubOff
	_ = fs.SavePlan(ctx, p)
	a.handleCallback(ctx, cb(uid, "plo:"+p.Code))
	if strings.Contains(fm.last(), "Второй сервер") {
		t.Fatalf("тариф без опции не должен её показывать: %q", fm.last())
	}
}

// Продление: без изменений — карточка как есть; условия изменились — плашка;
// тариф исчез — честное сообщение с выбором нового.
func TestRenew_ThreeScenarios(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	a.botCfg.AddSub.Enabled = true
	p := vipPlan(t, fs, model.PlanAvailLink)
	uid := int64(830)
	_ = fs.UpsertUser(ctx, uid)

	// Снимок ровно с сегодняшних условий тарифа (опция продана).
	snap := a.planSnapshotOf(p, p.Duration(1), 1)
	_ = fs.SetUserSnapshot(ctx, uid, snap)

	// 1: условия те же — плашки нет.
	a.handleCallback(ctx, cb(uid, "menu:renew"))
	if strings.Contains(fm.last(), "изменились") {
		t.Fatalf("плашка без изменений: %q", fm.last())
	}
	if !hasCB(fm.allCallbackData(), "plb:"+p.Code+":1") {
		t.Fatalf("карточка тарифа не открылась: %q", fm.last())
	}
	if !strings.Contains(strings.Join(fm.buttonLabels(), "|"), "другой тариф") {
		t.Fatalf("нет кнопки смены тарифа: %v", fm.buttonLabels())
	}

	// 2: цена срока изменилась — плашка.
	p.Durations[0].Base = "1990"
	_ = fs.SavePlan(ctx, p)
	a.handleCallback(ctx, cb(uid, "menu:renew"))
	if !strings.Contains(fm.last(), "изменились") {
		t.Fatalf("плашка об изменении не показана: %q", fm.last())
	}

	// 2а: изменение только опции доп-подписки — тоже изменение условий.
	p.Durations[0].Base = snap.Price
	p.AddSub = model.PlanAddSubOff
	_ = fs.SavePlan(ctx, p)
	a.handleCallback(ctx, cb(uid, "menu:renew"))
	if !strings.Contains(fm.last(), "изменились") {
		t.Fatalf("снятая опция — изменение условий: %q", fm.last())
	}

	// 3: тариф удалён — сообщение и выбор нового.
	_ = fs.DeletePlan(ctx, p.Code)
	a.handleCallback(ctx, cb(uid, "menu:renew"))
	if !strings.Contains(fm.last(), "больше недоступен") || !hasCB(fm.allCallbackData(), "menu:buy") {
		t.Fatalf("сценарий «тариф исчез» не отработал: %q", fm.last())
	}
}

// Общие тексты в настройках доп-подписки.
func TestAddSubAdmin_GlobalTexts(t *testing.T) {
	ctx := context.Background()
	a, _, _ := planAdminApp(t)
	planTap(t, a, "addsub:name")
	a.handleMessage(ctx, msgText(planAdmin, "Второй сервер"))
	if a.botCfg.AddSub.Name != "Второй сервер" {
		t.Fatalf("общее название не сохранилось: %q", a.botCfg.AddSub.Name)
	}
	planTap(t, a, "addsub:desc")
	a.handleMessage(ctx, msgText(planAdmin, "Описание опции"))
	if a.botCfg.AddSub.Description != "Описание опции" {
		t.Fatalf("общее описание не сохранилось: %q", a.botCfg.AddSub.Description)
	}
}

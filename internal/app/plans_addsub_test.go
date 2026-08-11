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

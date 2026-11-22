package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
)

// assertPhoneFit — каждая подпись влезает в свою долю ряда на телефоне шириной
// 360dp: многоточия Telegram не будет. Ряды уже прошли fitKeyboard, поэтому
// упасть может только кнопка, которой и одной в ряду тесно.
func assertPhoneFit(t *testing.T, rows [][]models.InlineKeyboardButton) {
	t.Helper()
	// Проверяется то, что увидит телефон: при отправке ряды проходят fitKeyboard.
	for _, row := range fitKeyboard(rows) {
		capW := rowCap(len(row))
		for _, b := range row {
			if w := btnWidth(b); w > capW {
				t.Errorf("кнопка %q не влезает на телефон: %.0f из %.0f dp", b.Text, w, capW)
			}
		}
	}
}

// trafficAdminPlan проходит создание трафик-тарифа кнопками и возвращает код.
func trafficAdminPlan(t *testing.T, a *App, fs *fakeStore) string {
	t.Helper()
	ctx := context.Background()
	before, _ := fs.ListPlans(ctx)
	planTap(t, a, "pln:new")
	planTap(t, a, "pln:newk:r")
	after, _ := fs.ListPlans(ctx)
	if len(after) != len(before)+1 {
		t.Fatalf("тариф не создан: было %d, стало %d", len(before), len(after))
	}
	for _, p := range after {
		if p.IsTraffic() {
			return p.Code
		}
	}
	t.Fatal("создан тариф не того вида")
	return ""
}

func TestTrafficAdmin_CreateAndPrice(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	planTap(t, a, "pln:new")
	if !strings.Contains(fm.last(), "Какой тариф создаём") {
		t.Fatalf("нет выбора вида: %q", fm.last())
	}
	assertPhoneFit(t, fm.lastKB)

	code := trafficAdminPlan(t, a, fs)
	p, _ := fs.GetPlan(ctx, code)
	if p.Enabled || p.Strategy != "NO_RESET" || len(p.Durations) != 0 {
		t.Fatalf("новый трафик-тариф: %+v", p)
	}
	if !strings.Contains(fm.last(), "бессрочно, без сброса") {
		t.Fatalf("карточка трафик-тарифа: %q", fm.last())
	}
	assertPhoneFit(t, fm.lastKB)

	// Пустой тариф не включается.
	planTap(t, a, "pln:toggle:"+code)
	if p, _ := fs.GetPlan(ctx, code); p.Enabled || !strings.Contains(fm.last(), "включать нечего") {
		t.Fatalf("пустой трафик-тариф включился: %q", fm.last())
	}

	planTap(t, a, "pln:pr:"+code)
	if !strings.Contains(fm.last(), "цена и лимиты") {
		t.Fatalf("экран цены: %q", fm.last())
	}
	assertPhoneFit(t, fm.lastKB)

	planTap(t, a, "pln:tin:g:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "0"))
	if p, _ := fs.GetPlan(ctx, code); p.TrafficGB != 0 || !strings.Contains(fm.last(), "Безлимита у трафик-тарифа нет") {
		t.Fatalf("безлимит принят: %d %q", p.TrafficGB, fm.last())
	}
	planTap(t, a, "pln:tin:g:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "150"))
	planTap(t, a, "pln:tin:p:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "1 450"))
	planTap(t, a, "pln:tin:s:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "300"))
	p, _ = fs.GetPlan(ctx, code)
	if p.TrafficGB != 150 || p.Traffic == nil || p.Traffic.Price != "1450" || p.Traffic.Stars != 300 {
		t.Fatalf("цена и трафик: %+v %+v", p, p.Traffic)
	}
	planTap(t, a, "pln:tin:p:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "abc"))
	if p, _ := fs.GetPlan(ctx, code); p.Traffic.Price != "1450" {
		t.Fatal("кривая цена перезаписала цену")
	}

	planTap(t, a, "pln:toggle:"+code)
	if p, _ := fs.GetPlan(ctx, code); !p.Enabled {
		t.Fatalf("заполненный тариф не включился: %q", fm.last())
	}

	// Экраны срочного тарифа у трафик-тарифа не открываются и сроки не
	// заводят — ни старой кнопкой, ни подделанной.
	planTap(t, a, "pln:prm:1:"+code)
	planTap(t, a, "pln:in:b:1:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "100"))
	planTap(t, a, "pln:sts:MONTH:"+code)
	if p, _ := fs.GetPlan(ctx, code); len(p.Durations) != 0 || p.Strategy != "NO_RESET" {
		t.Fatalf("трафик-тарифу завели срок или сброс: %+v", p)
	}
}

func TestTrafficAdmin_Packs(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	code := trafficAdminPlan(t, a, fs)

	planTap(t, a, "pln:pk:"+code)
	if !strings.Contains(fm.last(), "Пакетов пока нет") {
		t.Fatalf("экран пакетов: %q", fm.last())
	}
	for _, line := range []string{"50 / 1 990 / 110", "100 290", "500 / - / 850", "25 / 990"} {
		planTap(t, a, "pln:pka:"+code)
		a.handleMessage(ctx, msgText(planAdmin, line))
	}
	for _, bad := range []string{"10 -", "50 1 990", "10 / -", "10 / 5 / 6 / 7", "0 / 100"} {
		planTap(t, a, "pln:pka:"+code)
		a.handleMessage(ctx, msgText(planAdmin, bad))
		if !strings.Contains(fm.last(), "Не разобрал") {
			t.Fatalf("строка %q принята: %q", bad, fm.last())
		}
	}
	p, _ := fs.GetPlan(ctx, code)
	packs := p.Traffic.Packs
	if len(packs) != 4 || packs[0].GB != 50 || packs[0].Price != "1990" || packs[0].Stars != 110 ||
		packs[1].Price != "290" || packs[2].Price != "" || packs[2].Stars != 850 || packs[3].Price != "990" {
		t.Fatalf("пакеты: %+v", packs)
	}
	planTap(t, a, "pln:pk:"+code)
	if len(fm.lastKB) != 6 {
		t.Fatalf("на экране пакетов %d рядов", len(fm.lastKB))
	}
	assertPhoneFit(t, fm.lastKB)

	id := packs[1].ID
	planTap(t, a, "pln:pke:"+id+":"+code)
	assertPhoneFit(t, fm.lastKB)
	planTap(t, a, "pln:pki:g:"+id+":"+code)
	a.handleMessage(ctx, msgText(planAdmin, "120"))
	planTap(t, a, "pln:pki:p:"+id+":"+code)
	a.handleMessage(ctx, msgText(planAdmin, "-"))
	if !strings.Contains(fm.last(), "не останется ни одной цены") {
		t.Fatalf("пакет остался бы без цены: %q", fm.last())
	}
	if p, _ := fs.GetPlan(ctx, code); p.Pack(id).GB != 120 || p.Pack(id).Price != "290" {
		t.Fatalf("правка пакета: %+v", p.Pack(id))
	}

	planTap(t, a, "pln:pkx:"+id+":"+code)
	if !strings.Contains(fm.last(), "Удалить пакет") {
		t.Fatalf("нет подтверждения удаления: %q", fm.last())
	}
	planTap(t, a, "pln:pkxy:"+id+":"+code)
	if p, _ := fs.GetPlan(ctx, code); p.Pack(id) != nil || len(p.Traffic.Packs) != 3 {
		t.Fatal("пакет не удалён")
	}
	// Повторное нажатие на старой кнопке — не ошибка.
	planTap(t, a, "pln:pkxy:"+id+":"+code)
	planTap(t, a, "pln:pke:"+id+":"+code)
	if !strings.Contains(fm.last(), "пакета уже нет") {
		t.Fatalf("удалённый пакет: %q", fm.last())
	}

	// Callback-данные укладываются в лимит Telegram при самом длинном коде.
	long := strings.Repeat("x", model.PlanCodeMaxLen)
	for _, cbd := range []string{"pln:pki:s:" + id + ":" + long, "pln:pkxy:" + id + ":" + long, "pln:asrs:MONTH_ROLLING:" + long} {
		if len(cbd) > 64 {
			t.Errorf("callback длиннее 64 байт: %q", cbd)
		}
	}
}

func TestTrafficAdmin_AddSubResetAndCopy(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	code := trafficAdminPlan(t, a, fs)
	planTap(t, a, "pln:as:"+code)
	if !strings.Contains(fm.last(), "каждый месяц в день подключения") {
		t.Fatalf("сброс доп-подписки по умолчанию не показан: %q", fm.last())
	}
	planTap(t, a, "pln:asr:"+code)
	assertPhoneFit(t, fm.lastKB)
	planTap(t, a, "pln:asrs:DAY:"+code)
	if p, _ := fs.GetPlan(ctx, code); p.AddSubStrategyOrDefault() != "DAY" {
		t.Fatalf("период сброса не сохранился: %+v", p.Traffic)
	}
	planTap(t, a, "pln:asrs:YEAR:"+code)
	if p, _ := fs.GetPlan(ctx, code); p.AddSubStrategyOrDefault() != "DAY" {
		t.Fatal("принят неизвестный период")
	}

	planTap(t, a, "pln:pka:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "50 160"))
	planTap(t, a, "pln:dup:"+code)
	plans, _ := fs.ListPlans(ctx)
	var src, cp *model.Plan
	for i := range plans {
		switch {
		case plans[i].Code == code:
			src = &plans[i]
		case plans[i].IsTraffic():
			cp = &plans[i]
		}
	}
	if cp == nil || len(cp.Traffic.Packs) != 1 || cp.Traffic.Packs[0] != src.Traffic.Packs[0] ||
		cp.AddSubStrategyOrDefault() != "DAY" {
		t.Fatalf("копия трафик-тарифа: %+v", cp)
	}
	// У срочного тарифа кнопки сброса доп-подписки нет.
	_ = fs.SavePlan(ctx, &model.Plan{Code: "term01", Name: "Срочный", Durations: []model.PlanDuration{{Months: 1, Base: "100"}}})
	planTap(t, a, "pln:as:term01")
	for _, row := range fm.lastKB {
		for _, b := range row {
			if strings.HasPrefix(b.CallbackData, "pln:asr:") {
				t.Fatal("у срочного тарифа кнопка сброса доп-подписки")
			}
		}
	}
}

func TestTrafficAdmin_ExportImport(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	code := trafficAdminPlan(t, a, fs)
	planTap(t, a, "pln:tin:g:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "150"))
	planTap(t, a, "pln:tin:p:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "450"))
	planTap(t, a, "pln:pka:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "50 / 160 / 110"))
	planTap(t, a, "pln:tin:r:"+code)
	a.handleMessage(ctx, msgText(planAdmin, "7"))
	planTap(t, a, "pln:rft:i:"+code)

	planTap(t, a, "pln:exp:"+code)
	data := fm.docs["plan_"+code+".json"]
	var head struct {
		Version int `json:"version"`
	}
	_ = json.Unmarshal(data, &head)
	if head.Version != planFileVersion {
		t.Fatalf("файл трафик-тарифа записан версией %d", head.Version)
	}

	_ = fs.DeletePlan(ctx, code)
	planTap(t, a, "pln:imp")
	fm.downloads = map[string][]byte{"file-1": data}
	a.handleDocument(ctx, &models.Message{
		Chat: models.Chat{ID: planAdmin}, From: &models.User{ID: planAdmin},
		Document: &models.Document{FileID: "file-1", FileSize: int64(len(data))},
	})
	planTap(t, a, "pln:impok")
	p, _ := fs.GetPlan(ctx, code)
	if p == nil || !p.IsTraffic() || p.TrafficGB != 150 || p.Traffic.Price != "450" || len(p.Traffic.Packs) != 1 || p.Traffic.RefGB != 7 || p.Traffic.RefTo != model.RefToInvitee {
		t.Fatalf("импорт трафик-тарифа: %+v", p)
	}

	// Поверх срочного тарифа с тем же кодом — отказ.
	_ = fs.DeletePlan(ctx, code)
	_ = fs.SavePlan(ctx, &model.Plan{Code: code, Name: "Срочный", Durations: []model.PlanDuration{{Months: 1, Base: "100"}}})
	planTap(t, a, "pln:imp")
	a.handleDocument(ctx, &models.Message{
		Chat: models.Chat{ID: planAdmin}, From: &models.User{ID: planAdmin},
		Document: &models.Document{FileID: "file-1", FileSize: int64(len(data))},
	})
	if !strings.Contains(fm.last(), "другого вида") {
		t.Fatalf("импорт поверх тарифа другого вида не остановлен: %q", fm.last())
	}
	if p, _ := fs.GetPlan(ctx, code); p.IsTraffic() {
		t.Fatal("вид тарифа сменился импортом")
	}

	for _, bad := range []string{
		`{"format":"remnabot-plan","version":3,"plan":{"code":"base","name":"x","kind":"traffic"}}`,
		`{"format":"remnabot-plan","version":3,"plan":{"code":"abcd","name":"x","kind":"future"}}`,
		`{"format":"remnabot-plan","version":3,"plan":{"code":"abcd","name":"x","kind":"traffic","durations":[{"months":1,"base":"1"}]}}`,
		`{"format":"remnabot-plan","version":3,"plan":{"code":"abcd","name":"x","kind":"traffic","traffic":{"packs":[{"id":"abc123","gb":5}]}}}`,
		`{"format":"remnabot-plan","version":3,"plan":{"code":"abcd","name":"x","traffic":{"price":"1"}}}`,
		`{"format":"remnabot-plan","version":3,"plan":{"code":"abcd","name":"x","kind":"traffic","traffic":{"ref_gb":-1}}}`,
		`{"format":"remnabot-plan","version":3,"plan":{"code":"abcd","name":"x","kind":"traffic","traffic":{"ref_gb":1000001}}}`,
		`{"format":"remnabot-plan","version":3,"plan":{"code":"abcd","name":"x","kind":"traffic","traffic":{"ref_gb":5,"ref_to":"all"}}}`,
	} {
		if _, _, err := parsePlanFile([]byte(bad)); err == nil {
			t.Errorf("принят неверный файл: %s", bad)
		}
	}
}

func TestTrafficAdmin_PacksLimit(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	code := trafficAdminPlan(t, a, fs)
	p, _ := fs.GetPlan(ctx, code)
	for i := 0; i < model.PlanPacksMax; i++ {
		id, _ := newCode(model.PlanPackIDLen)
		p.Traffic.Packs = append(p.Traffic.Packs, model.PlanPack{ID: id, GB: i + 1, Price: "10"})
	}
	_ = fs.SavePlan(ctx, p)
	planTap(t, a, "pln:pka:"+code)
	if !strings.Contains(fm.last(), "максимум") {
		t.Fatalf("добавление сверх предела: %q", fm.last())
	}
	planTap(t, a, "pln:pk:"+code)
	if len(fm.lastKB) != model.PlanPacksMax+2 {
		t.Fatalf("на экране пакетов %d рядов", len(fm.lastKB))
	}
}

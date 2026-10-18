package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// goneSquadsPanel — панель, где живы только s1 и e1 (и squad-main у тарифов).
// listFail — списки сквадов недоступны. Записывает тела PATCH пользователя.
type goneSquadsPanel struct {
	mu       sync.Mutex
	listFail bool
	patches  []map[string]any
}

func (p *goneSquadsPanel) start(t *testing.T) *remnawave.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		p.mu.Lock()
		fail := p.listFail
		p.mu.Unlock()
		switch {
		case strings.HasSuffix(path, "/internal-squads"):
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{"response":{"total":2,"internalSquads":[{"uuid":"s1","name":"Squad-1"},{"uuid":"squad-main","name":"Main"}]}}`))
		case strings.HasSuffix(path, "/external-squads"):
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{"response":{"total":1,"externalSquads":[{"uuid":"e1","name":"Ext-1"}]}}`))
		case strings.Contains(path, "/by-telegram-id/"):
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u1","tag":"CHILLBOT","username":"tg_555","telegramId":555,"subscriptionUrl":"https://sub/x","expireAt":"2030-01-01T00:00:00Z"}]}`))
		case path == "/api/users" && r.Method == http.MethodPatch:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			p.mu.Lock()
			p.patches = append(p.patches, body)
			p.mu.Unlock()
			_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/x","expireAt":"2030-01-04T00:00:00Z"}}`))
		default:
			_, _ = w.Write([]byte(`{"response":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

func goneSquadsApp(t *testing.T, p *goneSquadsPanel) (*App, *fakeMsg) {
	t.Helper()
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.ui = map[int64]*uiState{}
	a.botCfg = fullyConfigured()
	a.panel = p.start(t)
	return a, fm
}

func goneLabels(fm *fakeMsg) int {
	n := 0
	for _, l := range fm.buttonLabels() {
		if strings.Contains(l, "удалён в панели") {
			n++
		}
	}
	return n
}

func (a *App) trialSquadsForTest() ([]string, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.botCfg.Trial.InternalSquads...), a.botCfg.Trial.ExternalSquadUUID
}

// Удалённые в панели сквады триала видны на экране отдельной строкой, и
// нажатие на неё убирает их из настроек.
func TestTrialSquadsGoneShownAndRemoved(t *testing.T) {
	p := &goneSquadsPanel{}
	a, fm := goneSquadsApp(t, p)
	a.botCfg.Trial.InternalSquads = []string{"s1", "s-gone"}
	a.botCfg.Trial.ExternalSquadUUID = "e-gone"
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "trial:squads"))
	if n := goneLabels(fm); n != 2 {
		t.Fatalf("строк «удалён в панели»: %d, ожидалось 2; кнопки: %v", n, fm.buttonLabels())
	}
	if !strings.Contains(fm.last(), "удалены в панели") {
		t.Fatalf("нет пояснения на экране: %q", fm.last())
	}
	cbs := fm.allCallbackData()
	if !hasCB(cbs, "trial:intsq:s-gone") || !hasCB(cbs, "trial:extsq:e-gone") {
		t.Fatalf("у удалённых сквадов нет кнопок: %v", cbs)
	}

	a.handleCallback(ctx, cb(100, "trial:intsq:s-gone"))
	ints, ext := a.trialSquadsForTest()
	if strings.Join(ints, ",") != "s1" || ext != "" {
		t.Fatalf("после нажатия: internal=%v external=%q, ожидалось [s1] и пусто", ints, ext)
	}
}

// Любое сохранение набора триала выкидывает удалённые в панели сквады.
func TestTrialToggleDropsGoneSquads(t *testing.T) {
	p := &goneSquadsPanel{}
	a, _ := goneSquadsApp(t, p)
	a.botCfg.Trial.InternalSquads = []string{"s-gone"}
	a.botCfg.Trial.ExternalSquadUUID = "e-gone"

	a.handleCallback(context.Background(), cb(100, "trial:extsq:e1"))
	ints, ext := a.trialSquadsForTest()
	if len(ints) != 0 || ext != "e1" {
		t.Fatalf("после сохранения: internal=%v external=%q", ints, ext)
	}
}

// Список сквадов недоступен — ничего не считается удалённым.
func TestTrialSquadsUnknownListKeepsConfig(t *testing.T) {
	p := &goneSquadsPanel{listFail: true}
	a, fm := goneSquadsApp(t, p)
	a.botCfg.Trial.InternalSquads = []string{"s-gone"}
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "trial:squads"))
	if n := goneLabels(fm); n != 0 {
		t.Fatalf("без списка панели сквады объявлены удалёнными: %v", fm.buttonLabels())
	}
	a.handleCallback(ctx, cb(100, "trial:intsq:s1"))
	ints, _ := a.trialSquadsForTest()
	if strings.Join(ints, ",") != "s-gone,s1" {
		t.Fatalf("без списка панели набор изменён: %v", ints)
	}
}

// Выдача триала не отправляет в панель удалённые сквады.
func TestTrialIssueSkipsGoneSquads(t *testing.T) {
	p := &goneSquadsPanel{}
	a, _ := goneSquadsApp(t, p)
	a.botCfg.Trial.InternalSquads = []string{"s-gone", "s1"}
	a.botCfg.Trial.ExternalSquadUUID = "e-gone"

	if _, _, err := a.trialProvision(context.Background(), 555); err != nil {
		t.Fatalf("триал не выдан: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.patches) != 1 {
		t.Fatalf("ожидали одну запись в панель, получили %d", len(p.patches))
	}
	raw, _ := p.patches[0]["activeInternalSquads"].([]any)
	if len(raw) != 1 || raw[0] != "s1" {
		t.Fatalf("сквады в панель: %v", p.patches[0]["activeInternalSquads"])
	}
	if _, ok := p.patches[0]["externalSquadUuid"]; ok {
		t.Fatal("удалённый внешний сквад ушёл в панель")
	}
	// Настройки при выдаче не трогаются — их правит админ на экране.
	ints, ext := a.trialSquadsForTest()
	if len(ints) != 2 || ext != "e-gone" {
		t.Fatalf("выдача изменила настройки: %v %q", ints, ext)
	}
}

// Доп-подписка: удалённый сквад виден на экране и уходит при сохранении.
func TestAddSubSquadsGone(t *testing.T) {
	p := &goneSquadsPanel{}
	a, fm := goneSquadsApp(t, p)
	a.botCfg.AddSub.InternalSquads = []string{"s-gone"}
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "addsub:squads"))
	if n := goneLabels(fm); n != 1 || !hasCB(fm.allCallbackData(), "addsub:int:s-gone") {
		t.Fatalf("удалённый сквад доп-подписки не показан: %v", fm.buttonLabels())
	}
	a.handleCallback(ctx, cb(100, "addsub:int:s1"))
	a.mu.Lock()
	got := strings.Join(a.botCfg.AddSub.InternalSquads, ",")
	a.mu.Unlock()
	if got != "s1" {
		t.Fatalf("после сохранения: %q, ожидалось s1", got)
	}
}

// Тариф: удалённые сквады видны в редакторе срока, нажатие снимает их, а
// опустевшее переопределение снимается целиком — срок наследует тариф.
func TestPlanSquadsGone(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	p := &goneSquadsPanel{}
	a.panel = p.start(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	planTap(t, a, "pln:sqm:12:"+model.PlanCodeBase)
	if n := goneLabels(fm); n != 2 {
		t.Fatalf("строк «удалён в панели»: %d, ожидалось 2; кнопки: %v", n, fm.buttonLabels())
	}
	h := planEditHash(model.PlanCodeBase)
	gone := "plq:i:12:" + h + ":squad-year"
	if !hasCB(fm.allCallbackData(), gone) {
		t.Fatalf("нет кнопки удалённого сквада: %v", fm.allCallbackData())
	}

	planTap(t, a, gone)
	plan, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	d := plan.Duration(12)
	if d == nil {
		t.Fatal("срок 12 мес пропал")
	}
	if d.IntSquads != nil || d.ExtSquad != nil {
		t.Fatalf("переопределение не снято: int=%v ext=%v", d.IntSquads, d.ExtSquad)
	}
	if strings.Join(plan.IntSquads, ",") != "squad-main" {
		t.Fatalf("живые сквады тарифа задеты: %v", plan.IntSquads)
	}
}

// Уровень тарифа: сохранение набора выкидывает удалённые сквады, живые остаются.
func TestPlanLevelToggleDropsGone(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	a.botCfg.Plan.ActiveInternalSquads = []string{"squad-main", "squad-old"}
	p := &goneSquadsPanel{}
	a.panel = p.start(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	planTap(t, a, "pln:sqm:0:"+model.PlanCodeBase)
	planTap(t, a, "plq:i:0:"+planEditHash(model.PlanCodeBase)+":s1")
	plan, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	if strings.Join(plan.IntSquads, ",") != "squad-main,s1" {
		t.Fatalf("сквады тарифа: %v, ожидалось squad-main,s1", plan.IntSquads)
	}
}

// Глобальный экран сквадов тоже показывает удалённые.
func TestGlobalSquadsScreenShowsGone(t *testing.T) {
	p := &goneSquadsPanel{}
	a, fm := goneSquadsApp(t, p)
	a.botCfg.Plan.ActiveInternalSquads = []string{"s1", "s-gone"}

	a.handleCallback(context.Background(), cb(100, "menu:squads"))
	if n := goneLabels(fm); n != 1 || !hasCB(fm.allCallbackData(), "sqd:int:s-gone") {
		t.Fatalf("удалённый сквад не показан: %v", fm.buttonLabels())
	}
}

// Сохранение другого сквада срока тоже чистит его набор: переопределение из
// одних удалённых сквадов снимается, срок наследует внутренние сквады тарифа.
func TestPlanMonthOverrideAllGoneInherits(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	p := &goneSquadsPanel{}
	a.panel = p.start(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	planTap(t, a, "pln:sqm:12:"+model.PlanCodeBase)
	planTap(t, a, "plq:e:12:"+planEditHash(model.PlanCodeBase)+":e1")
	plan, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	d := plan.Duration(12)
	if d == nil || d.IntSquads != nil {
		t.Fatalf("переопределение из удалённых сквадов осталось: %+v", d)
	}
	if d.ExtSquad == nil || *d.ExtSquad != "e1" {
		t.Fatalf("внешний сквад срока не сохранён: %v", d.ExtSquad)
	}
	if got := strings.Join(plan.IntSquadsFor(d), ","); got != "squad-main" {
		t.Fatalf("срок получает сквады %q, ожидались сквады тарифа", got)
	}
}

// Старый экран сквадов по срокам (кнопки из прежних переписок) тоже их показывает.
func TestLegacyPriceSquadsScreenShowsGone(t *testing.T) {
	p := &goneSquadsPanel{}
	a, fm := goneSquadsApp(t, p)
	a.botCfg.Pricing.SquadsInt = map[int][]string{1: {"s-gone"}}

	a.handleCallback(context.Background(), cb(100, "prc:sqm:1"))
	if n := goneLabels(fm); n != 1 || !hasCB(fm.allCallbackData(), "prc:sqi:1:s-gone") {
		t.Fatalf("удалённый сквад не показан: %v", fm.buttonLabels())
	}
}

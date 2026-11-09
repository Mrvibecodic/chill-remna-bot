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

// addSubTermsPanel — панель с основной учёткой tg_555 и доп-подпиской,
// которая появляется после первого создания. Запоминает записи доп-подписки.
type addSubTermsPanel struct {
	mu      sync.Mutex
	created bool
	writes  []map[string]any
}

const addSubTermsMain = `{"uuid":"u1","tag":"CHILLBOT","username":"tg_555","telegramId":555,"status":"ACTIVE",` +
	`"subscriptionUrl":"https://sub/x","expireAt":"2030-01-01T00:00:00Z","hwidDeviceLimit":3,"trafficLimitStrategy":"MONTH"}`

func (p *addSubTermsPanel) start(t *testing.T) *remnawave.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case strings.HasSuffix(path, "/internal-squads"):
			_, _ = w.Write([]byte(`{"response":{"total":3,"internalSquads":[{"uuid":"g1","name":"Общий"},` +
				`{"uuid":"s1","name":"Свой-1"},{"uuid":"s2","name":"Свой-2"}]}}`))
		case strings.HasSuffix(path, "/external-squads"):
			_, _ = w.Write([]byte(`{"response":{"total":0,"externalSquads":[]}}`))
		case strings.Contains(path, "/by-telegram-id/"):
			_, _ = w.Write([]byte(`{"response":[` + addSubTermsMain + `]}`))
		case strings.Contains(path, "/by-username/"):
			if !p.created || !strings.HasSuffix(path, "_addsub") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"response":{"uuid":"b-uuid","username":"tg_555_addsub","tag":"CHILLBOT_ADD",` +
				`"status":"ACTIVE","expireAt":"2030-01-01T00:00:00Z"}}`))
		case path == "/api/users" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"response":{"total":1,"users":[` + addSubTermsMain + `]}}`))
		case path == "/api/users" && (r.Method == http.MethodPost || r.Method == http.MethodPatch):
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			name, _ := body["username"].(string)
			uuid, _ := body["uuid"].(string)
			// Включение и выключение доп-подписки пишут только статус — нужны
			// записи условий.
			_, terms := body["trafficLimitBytes"]
			if terms && (strings.HasSuffix(name, "_addsub") || uuid == "b-uuid") {
				if r.Method == http.MethodPost {
					p.created = true
				}
				p.writes = append(p.writes, body)
			}
			_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/x","expireAt":"2030-02-01T00:00:00Z"}}`))
		default:
			_, _ = w.Write([]byte(`{"response":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

// last — трафик и сквады последней записи доп-подписки.
func (p *addSubTermsPanel) last(t *testing.T) (int64, string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.writes) == 0 {
		t.Fatal("доп-подписка в панель не записана")
	}
	w := p.writes[len(p.writes)-1]
	traffic, _ := w["trafficLimitBytes"].(float64)
	var squads []string
	if raw, ok := w["activeInternalSquads"].([]any); ok {
		for _, v := range raw {
			s, _ := v.(string)
			squads = append(squads, s)
		}
	}
	return int64(traffic), strings.Join(squads, ",")
}

func addSubTermsApp(t *testing.T) (*App, *fakeMsg, *fakeStore, *addSubTermsPanel) {
	t.Helper()
	a, fm, fs := planAdminApp(t)
	p := &addSubTermsPanel{}
	a.panel = p.start(t)
	a.botCfg.AddSub = model.AddSubConfig{Enabled: true, Init: true, UsernameSuffix: "_addsub",
		TrafficGB: 10, InternalSquads: []string{"g1"}}
	_ = fs.UpsertUser(context.Background(), 555)
	return a, fm, fs, p
}

func intp(v int) *int { return &v }

// Покупка применяет к доп-подписке условия, проданные тарифом: свои — из
// сделки (правка тарифа после счёта не действует), «как в общих» — сегодняшние
// общие настройки.
func TestAddSubTerms_PurchaseAppliesSoldTerms(t *testing.T) {
	cases := []struct {
		name       string
		traffic    *int
		squads     []string
		wantBytes  int64
		wantSquads string
	}{
		{"свои у тарифа", intp(25), []string{"s2"}, 25 * gb, "s2"},
		{"безлимит при общих сквадах", intp(0), nil, 0, "s1"},
		{"как в общих", nil, nil, 11 * gb, "s1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			a, _, fs, panel := addSubTermsApp(t)
			p := vipPlan(t, fs, model.PlanAvailAll)
			p.AddSubTrafficGB, p.AddSubSquads = tc.traffic, tc.squads
			_ = fs.SavePlan(ctx, p)
			snap := a.planSnapshotOf(p, p.Duration(1), 1)

			// После счёта админ правит и тариф, и общие настройки.
			p.AddSubTrafficGB, p.AddSubSquads = intp(99), []string{"s1", "s2"}
			_ = fs.SavePlan(ctx, p)
			a.botCfg.AddSub.TrafficGB = 11
			a.botCfg.AddSub.InternalSquads = []string{"s1"}

			if _, _, err := a.finalizePurchase(ctx, 555, 1, model.PayMethodYooKassa, "990 ₽", "yk_terms_1", snap); err != nil {
				t.Fatal(err)
			}
			bytes, squads := panel.last(t)
			if bytes != tc.wantBytes || squads != tc.wantSquads {
				t.Fatalf("доп-подписка: трафик %d, сквады %q; ожидалось %d, %q", bytes, squads, tc.wantBytes, tc.wantSquads)
			}
		})
	}
}

// «Синхронизировать всех» пишет каждому условия его сделки, а не общие.
func TestAddSubTerms_BackfillUsesUserSnapshot(t *testing.T) {
	ctx := context.Background()
	a, _, fs, panel := addSubTermsApp(t)
	yes := true
	_ = fs.SetUserSnapshot(ctx, 555, &model.PlanSnapshot{Code: "vipvipvipvip", Months: 1, AddSub: &yes,
		AddSubTrafficGB: intp(25), AddSubSquads: []string{"s2"}})

	cli, enabled, opt := a.addSubOptions(false)
	if !enabled {
		t.Fatal("доп-подписка выключена")
	}
	if st := a.addSubBackfill(ctx, cli, opt); st.err != nil || st.ok != 1 {
		t.Fatalf("синхронизация: %+v", st)
	}
	if bytes, squads := panel.last(t); bytes != 25*gb || squads != "s2" {
		t.Fatalf("синхронизация записала общие условия: %d %q", bytes, squads)
	}
}

// Триал — общие условия, даже если у человека осталась сделка со своими.
func TestAddSubTerms_TrialUsesCommon(t *testing.T) {
	ctx := context.Background()
	a, _, fs, panel := addSubTermsApp(t)
	a.botCfg.Trial = model.TrialConfig{Enabled: true, Days: 3}
	yes := true
	_ = fs.SetUserSnapshot(ctx, 555, &model.PlanSnapshot{Months: 1, AddSub: &yes, AddSubTrafficGB: intp(25), AddSubSquads: []string{"s2"}})

	if _, _, err := a.trialProvision(ctx, 555); err != nil {
		t.Fatal(err)
	}
	if bytes, squads := panel.last(t); bytes != 10*gb || squads != "g1" {
		t.Fatalf("триал получил условия тарифа: %d %q", bytes, squads)
	}
}

// Экран тарифа: трафик вводом (число, 0 — безлимит, прочерк — как в общих),
// сквады тумблерами и кнопкой «как в общих».
func TestAddSubTerms_AdminScreen(t *testing.T) {
	ctx := context.Background()
	a, fm, fs, _ := addSubTermsApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)
	traffic := func() *int { got, _ := fs.GetPlan(ctx, p.Code); return got.AddSubTrafficGB }
	squads := func() string { got, _ := fs.GetPlan(ctx, p.Code); return strings.Join(got.AddSubSquads, ",") }

	planTap(t, a, "pln:as:"+p.Code)
	if !strings.Contains(fm.last(), "как в общих (10 GB)") {
		t.Fatalf("экран не показывает общий трафик: %q", fm.last())
	}
	for _, step := range []struct {
		in   string
		want *int
	}{{"25", intp(25)}, {"0", intp(0)}, {"мало", intp(0)}, {"-1", intp(0)}, {"-", nil}} {
		planTap(t, a, "pln:ast:"+p.Code)
		a.handleMessage(ctx, msgText(planAdmin, step.in))
		got := traffic()
		if (got == nil) != (step.want == nil) || (got != nil && *got != *step.want) {
			t.Fatalf("ввод %q: трафик %v", step.in, got)
		}
	}
	if !strings.Contains(fm.last(), "как в общих") {
		t.Fatalf("после прочерка экран не вернулся к общим: %q", fm.last())
	}

	planTap(t, a, "pln:asq:"+p.Code)
	h := planEditHash(p.Code)
	if !hasCB(fm.allCallbackData(), "plq:a:0:"+h+":s1") {
		t.Fatalf("нет тумблеров сквадов: %v", fm.allCallbackData())
	}
	planTap(t, a, "plq:a:0:"+h+":s1")
	planTap(t, a, "plq:a:0:"+h+":s2")
	if got := squads(); got != "s1,s2" {
		t.Fatalf("сквады не сохранились: %q", got)
	}
	planTap(t, a, "plq:a:0:"+h+":s1")
	if got := squads(); got != "s2" {
		t.Fatalf("повторное нажатие не сняло сквад: %q", got)
	}
	// Тумблер не трогает сетку цен: «Базовый» не переходит на редактор.
	if got, _ := fs.GetPlan(ctx, p.Code); strings.Join(got.IntSquads, ",") != "sq-vip" {
		t.Fatalf("тумблер доп-подписки изменил сквады тарифа: %v", got.IntSquads)
	}
	planTap(t, a, "pln:asqc:"+p.Code)
	if got := squads(); got != "" {
		t.Fatalf("«как в общих» не сбросило сквады: %q", got)
	}
}

// «Базовый»: свои условия переживают пересборку из сетки цен и попадают в
// снимок продажи по сетке.
func TestAddSubTerms_BasePlanKeepsTerms(t *testing.T) {
	ctx := context.Background()
	a, _, fs, _ := addSubTermsApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.editPlan(ctx, model.PlanCodeBase, func(p *model.Plan) error {
		p.AddSubTrafficGB, p.AddSubSquads = intp(7), []string{"s1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	if got.AddSubTrafficGB == nil || *got.AddSubTrafficGB != 7 || strings.Join(got.AddSubSquads, ",") != "s1" || !got.FromConfig {
		t.Fatalf("пересборка «Базового» потеряла условия или сняла ведение от конфига: %+v", got)
	}
	snap := a.planSnapshot(1)
	if snap.AddSubTrafficGB == nil || *snap.AddSubTrafficGB != 7 || strings.Join(snap.AddSubSquads, ",") != "s1" {
		t.Fatalf("снимок «Базового» без условий доп-подписки: %+v", snap)
	}
	// Тариф без опции не несёт её условий в сделку.
	_, _ = a.editPlan(ctx, model.PlanCodeBase, func(p *model.Plan) error { p.AddSub = model.PlanAddSubOff; return nil })
	if snap := a.planSnapshot(1); snap.AddSubTrafficGB != nil || snap.AddSubSquads != nil {
		t.Fatalf("условия опции в сделке без опции: %+v", snap)
	}
}

// Отпечаток: свои условия доп-подписки — часть условий сделки, а снимки без
// них считаются как раньше.
func TestAddSubTerms_Fingerprint(t *testing.T) {
	old := model.DecodePlanSnapshot(`{"months":1,"traffic_gb":0,"device_limit":3,"price":"150"}`)
	same := &model.PlanSnapshot{Months: 1, DeviceLimit: 3, Price: "150"}
	if old.Fingerprint() != same.Fingerprint() {
		t.Fatal("снимок без условий доп-подписки поменял отпечаток")
	}
	withTraffic := *same
	withTraffic.AddSubTrafficGB = intp(0)
	withSquads := *same
	withSquads.AddSubSquads = []string{"s1"}
	if withTraffic.Fingerprint() == same.Fingerprint() || withSquads.Fingerprint() == same.Fingerprint() {
		t.Fatal("свои условия доп-подписки не меняют отпечаток")
	}
}

// Экспорт и импорт тарифа переносят условия; файл без них — «как в общих».
func TestAddSubTerms_ExportImport(t *testing.T) {
	ctx := context.Background()
	a, fm, fs, _ := addSubTermsApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)
	p.AddSubTrafficGB, p.AddSubSquads = intp(0), []string{"s2"}
	_ = fs.SavePlan(ctx, p)
	planTap(t, a, "pln:exp:"+p.Code)
	data := fm.docs["plan_"+p.Code+".json"]
	if len(data) == 0 {
		t.Fatal("файл тарифа не отправлен")
	}
	got, _, err := parsePlanFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.AddSubTrafficGB == nil || *got.AddSubTrafficGB != 0 || strings.Join(got.AddSubSquads, ",") != "s2" {
		t.Fatalf("импорт потерял условия доп-подписки: %+v", got)
	}
	plain := strings.Replace(string(data), `"addsub_traffic_gb": 0,`, "", 1)
	plain = strings.Replace(plain, `"addsub_squads": [
      "s2"
    ],`, "", 1)
	if strings.Contains(plain, "addsub_traffic_gb") || strings.Contains(plain, "addsub_squads") {
		t.Fatalf("не удалось убрать поля из файла: %s", plain)
	}
	got, _, err = parsePlanFile([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if got.AddSubTrafficGB != nil || got.AddSubSquads != nil {
		t.Fatalf("файл без условий дал свои условия: %+v", got)
	}
}

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
	"time"
	"unicode/utf16"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// addSubTermsPanel — панель с основной учёткой tg_555 и доп-подпиской,
// которая появляется после первого создания. Запоминает записи доп-подписки.
type addSubTermsPanel struct {
	mu      sync.Mutex
	created bool
	expired bool
	// renamed — основная учётка переименована, доп-подписка живёт под старым
	// именем, и удалить её панель не даёт.
	renamed bool
	writes  []map[string]any
}

// main — основная учётка; с expired её подписка истекла.
func (p *addSubTermsPanel) main() string {
	out := addSubTermsMain
	if p.expired {
		out = strings.Replace(out, "2030-01-01", "2020-01-01", 1)
	}
	if p.renamed {
		out = strings.Replace(out, `"username":"tg_555"`, `"username":"vasya"`, 1)
	}
	return out
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
			_, _ = w.Write([]byte(`{"response":[` + p.main() + `]}`))
		case strings.Contains(path, "/by-username/"):
			if !p.created || !strings.HasSuffix(path, "_addsub") || (p.renamed && !strings.HasSuffix(path, "/tg_555_addsub")) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"response":{"uuid":"b-uuid","username":"tg_555_addsub","tag":"CHILLBOT_ADD",` +
				`"status":"ACTIVE","expireAt":"2030-01-01T00:00:00Z"}}`))
		case p.renamed && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusInternalServerError)
		case path == "/api/users" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"response":{"total":1,"users":[` + p.main() + `]}}`))
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

// Синхронизация пишет каждому условия его сделки, а не общие.
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
	if !strings.Contains(fm.last(), "как в настройках (10 GB)") {
		t.Fatalf("экран не показывает общий трафик: %q", fm.last())
	}
	for _, step := range []struct {
		in   string
		want *int
	}{{"25", intp(25)}, {"0", intp(0)}, {"мало", intp(0)}, {"-1", intp(0)}, {"1000001", intp(0)}, {"1000000", intp(1000000)}, {"-", nil}} {
		planTap(t, a, "pln:ast:"+p.Code)
		a.handleMessage(ctx, msgText(planAdmin, step.in))
		got := traffic()
		if (got == nil) != (step.want == nil) || (got != nil && *got != *step.want) {
			t.Fatalf("ввод %q: трафик %v", step.in, got)
		}
	}
	if !strings.Contains(fm.last(), "как в настройках (") {
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

// Все свои сквады тарифа удалены в панели: доп-подписка получает общие, а
// экран тарифа предупреждает.
func TestAddSubTerms_GoneSquadsFallBackToCommon(t *testing.T) {
	ctx := context.Background()
	a, fm, fs, panel := addSubTermsApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)
	p.AddSubSquads = []string{"gone-1"}
	_ = fs.SavePlan(ctx, p)
	snap := a.planSnapshotOf(p, p.Duration(1), 1)
	if _, _, err := a.finalizePurchase(ctx, 555, 1, model.PayMethodYooKassa, "990 ₽", "yk_terms_gone", snap); err != nil {
		t.Fatal(err)
	}
	if _, squads := panel.last(t); squads != "g1" {
		t.Fatalf("доп-подписка без живых сквадов тарифа не взяла общие: %q", squads)
	}
	planTap(t, a, "pln:as:"+p.Code)
	if !strings.Contains(fm.last(), "удалены в панели") {
		t.Fatalf("нет предупреждения об удалённых сквадах: %q", fm.last())
	}
	// Хотя бы один живой — свои сквады остаются.
	p.AddSubSquads = []string{"gone-1", "s2"}
	_ = fs.SavePlan(ctx, p)
	snap = a.planSnapshotOf(p, p.Duration(1), 1)
	if _, _, err := a.finalizePurchase(ctx, 555, 1, model.PayMethodYooKassa, "990 ₽", "yk_terms_gone2", snap); err != nil {
		t.Fatal(err)
	}
	if _, squads := panel.last(t); squads != "s2" {
		t.Fatalf("живой сквад тарифа потерян: %q", squads)
	}
}

// Ни своих, ни общих сквадов — экран тарифа предупреждает; общий трафик
// тоже не принимает опечатку с лишними нулями.
func TestAddSubTerms_NoSquadsWarningAndGlobalCap(t *testing.T) {
	ctx := context.Background()
	a, fm, fs, _ := addSubTermsApp(t)
	a.botCfg.AddSub.InternalSquads = nil
	p := vipPlan(t, fs, model.PlanAvailAll)
	planTap(t, a, "pln:as:"+p.Code)
	if !strings.Contains(fm.last(), "без серверов") {
		t.Fatalf("нет предупреждения о пустых сквадах: %q", fm.last())
	}
	a.botCfg.AddSub.InternalSquads = []string{"g1"}
	planTap(t, a, "pln:as:"+p.Code)
	if strings.Contains(fm.last(), "без серверов") || strings.Contains(fm.last(), "удалены в панели") {
		t.Fatalf("лишнее предупреждение: %q", fm.last())
	}

	planTap(t, a, "addsub:gb")
	a.handleMessage(ctx, msgText(planAdmin, "1000000000000"))
	if a.botCfg.AddSub.TrafficGB != 10 {
		t.Fatalf("общий трафик принял переполнение: %d", a.botCfg.AddSub.TrafficGB)
	}
	planTap(t, a, "addsub:gb")
	a.handleMessage(ctx, msgText(planAdmin, "40"))
	if a.botCfg.AddSub.TrafficGB != 40 {
		t.Fatalf("общий трафик не сохранился: %d", a.botCfg.AddSub.TrafficGB)
	}
}

// Версия файла тарифа: без своих условий — 1 (читают прежние версии бота),
// со своими — 2; файл новее понятного отклоняется внятно, даже с незнакомыми
// полями; трафик вне границ не импортируется.
func TestAddSubTerms_PlanFileVersion(t *testing.T) {
	ctx := context.Background()
	a, fm, fs, _ := addSubTermsApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)
	version := func() int {
		planTap(t, a, "pln:exp:"+p.Code)
		var head struct {
			Version int `json:"version"`
		}
		_ = json.Unmarshal(fm.docs["plan_"+p.Code+".json"], &head)
		return head.Version
	}
	if v := version(); v != 1 {
		t.Fatalf("файл без своих условий записан версией %d", v)
	}
	p.AddSubTrafficGB = intp(5)
	_ = fs.SavePlan(ctx, p)
	if v := version(); v != 2 {
		t.Fatalf("файл со своими условиями записан версией %d", v)
	}

	_, _, err := parsePlanFile([]byte(`{"format":"remnabot-plan","version":3,"plan":{"code":"abc","name":"x","future":1}}`))
	if err == nil || !strings.Contains(err.Error(), "более новой версии") {
		t.Fatalf("файл новой версии отклонён невнятно: %v", err)
	}
	_, _, err = parsePlanFile([]byte(`{"format":"remnabot-plan","version":2,"plan":{"code":"abcd","name":"x","addsub_traffic_gb":1000001}}`))
	if err == nil || !strings.Contains(err.Error(), "трафик доп-подписки") {
		t.Fatalf("импорт принял трафик вне границ: %v", err)
	}
}

// writesCount — сколько раз в панель писали условия доп-подписки.
func (p *addSubTermsPanel) writesCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.writes)
}

// Купившие, пока доп-подписка была выключена, получают её синхронизацией на
// сегодняшних условиях тарифа, и дальше она ведётся как проданная.
func TestAddSubBackfill_BoughtWhileOff(t *testing.T) {
	cases := []struct {
		name       string
		base       bool
		traffic    *int
		squads     []string
		wantBytes  int64
		wantSquads string
	}{
		{"общие условия", false, nil, nil, 10 * gb, "g1"},
		{"свои условия тарифа", false, intp(25), []string{"s2"}, 25 * gb, "s2"},
		{"базовый тариф", true, nil, nil, 10 * gb, "g1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			a, _, fs, panel := addSubTermsApp(t)
			p := vipPlan(t, fs, model.PlanAvailAll)

			a.botCfg.AddSub.Enabled = false
			snap := a.planSnapshotOf(p, p.Duration(1), 1)
			if tc.base {
				snap = a.planSnapshot(1)
			}
			if snap.AddSubSold() {
				t.Fatal("сделка при выключенной доп-подписке записана с опцией")
			}
			_ = fs.SetUserSnapshot(ctx, 555, snap)

			a.botCfg.AddSub.Enabled = true
			p.AddSubTrafficGB, p.AddSubSquads = tc.traffic, tc.squads
			_ = fs.SavePlan(ctx, p)

			cli, _, opt := a.addSubOptions(false)
			if st := a.addSubBackfill(ctx, cli, opt); st.err != nil || st.ok != 1 || st.failed != 0 {
				t.Fatalf("синхронизация: %+v", st)
			}
			if bytes, squads := panel.last(t); bytes != tc.wantBytes || squads != tc.wantSquads {
				t.Fatalf("доп-подписка: трафик %d, сквады %q; ожидалось %d, %q", bytes, squads, tc.wantBytes, tc.wantSquads)
			}
			if !a.addSubSoldTo(ctx, 555) {
				t.Fatal("снимок пользователя остался без опции")
			}
			before := panel.writesCount()
			a.syncAddSub(ctx, 555, false)
			if panel.writesCount() == before {
				t.Fatal("после синхронизации доп-подписка не ведётся как проданная")
			}
			if bytes, squads := panel.last(t); bytes != tc.wantBytes || squads != tc.wantSquads {
				t.Fatalf("условия после синхронизации: трафик %d, сквады %q", bytes, squads)
			}
		})
	}
}

// Выключенный и удалённый тариф выдаче не мешают: у выключенного берутся его
// условия, у удалённого — общие.
func TestAddSubBackfill_DisabledOrDeletedPlan(t *testing.T) {
	no := false
	cases := []struct {
		name       string
		code       string
		wantBytes  int64
		wantSquads string
	}{
		{"тариф выключен", "vipvipvipvip", 25 * gb, "s2"},
		{"тариф удалён", "goneplangone", 10 * gb, "g1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			a, _, fs, panel := addSubTermsApp(t)
			p := vipPlan(t, fs, model.PlanAvailAll)
			p.Enabled = false
			p.AddSubTrafficGB, p.AddSubSquads = intp(25), []string{"s2"}
			_ = fs.SavePlan(ctx, p)
			_ = fs.SetUserSnapshot(ctx, 555, &model.PlanSnapshot{Code: tc.code, Months: 1, AddSub: &no})

			cli, _, opt := a.addSubOptions(false)
			if st := a.addSubBackfill(ctx, cli, opt); st.err != nil || st.ok != 1 || st.failed != 0 {
				t.Fatalf("синхронизация: %+v", st)
			}
			if bytes, squads := panel.last(t); bytes != tc.wantBytes || squads != tc.wantSquads {
				t.Fatalf("доп-подписка: трафик %d, сквады %q; ожидалось %d, %q", bytes, squads, tc.wantBytes, tc.wantSquads)
			}
			if !a.addSubSoldTo(ctx, 555) {
				t.Fatal("снимок пользователя остался без опции")
			}
		})
	}
}

// Без доп-подписки остаются сделка тарифа, у которого опция выключена, и
// истёкшая подписка; снимок при этом не трогается.
func TestAddSubBackfill_NotGranted(t *testing.T) {
	no := false
	cases := []struct {
		name    string
		mode    string
		expired bool
	}{
		{"опция выключена у тарифа", model.PlanAddSubOff, false},
		{"подписка истекла", model.PlanAddSubOn, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			a, _, fs, panel := addSubTermsApp(t)
			panel.expired = tc.expired
			p := vipPlan(t, fs, model.PlanAvailAll)
			p.AddSub = tc.mode
			_ = fs.SavePlan(ctx, p)
			_ = fs.SetUserSnapshot(ctx, 555, &model.PlanSnapshot{Code: p.Code, Months: 1, AddSub: &no})

			cli, _, opt := a.addSubOptions(false)
			if st := a.addSubBackfill(ctx, cli, opt); st.err != nil || st.ok != 0 || st.failed != 0 {
				t.Fatalf("синхронизация: %+v", st)
			}
			if panel.writesCount() != 0 {
				t.Fatal("доп-подписка создана")
			}
			if a.addSubSoldTo(ctx, 555) {
				t.Fatal("снимок пользователя получил опцию")
			}
		})
	}
}

// Сбой чтения тарифа — ошибка в итоге синхронизации, а не тихий пропуск.
func TestAddSubBackfill_PlanReadError(t *testing.T) {
	ctx := context.Background()
	a, _, fs, panel := addSubTermsApp(t)
	no := false
	_ = fs.SetUserSnapshot(ctx, 555, &model.PlanSnapshot{Code: "vipvipvipvip", Months: 1, AddSub: &no})
	a.store = planErrStore{fs}

	cli, _, opt := a.addSubOptions(false)
	if st := a.addSubBackfill(ctx, cli, opt); st.err != nil || st.ok != 0 || st.failed != 1 {
		t.Fatalf("синхронизация: %+v", st)
	}
	if panel.writesCount() != 0 {
		t.Fatal("доп-подписка создана без условий тарифа")
	}
}

// syncTap нажимает кнопку синхронизации и ждёт итогового сообщения.
func syncTap(t *testing.T, a *App, fm *fakeMsg, data string) {
	t.Helper()
	planTap(t, a, data)
	deadline := time.Now().Add(5 * time.Second)
	for a.addSubSyncing.Load() || !strings.Contains(fm.last(), "Синхронизация завершена") {
		if time.Now().After(deadline) {
			t.Fatalf("синхронизация не завершилась: %q", fm.last())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Экран синхронизации даёт два режима: «обновить» ничего не выдаёт купившим
// без доп-подписки, «выдать всем» — выдаёт; дальше «обновить» ведёт её.
func TestAddSubSync_TwoModes(t *testing.T) {
	ctx := context.Background()
	a, fm, fs, panel := addSubTermsApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)
	a.botCfg.AddSub.Enabled = false
	_ = fs.SetUserSnapshot(ctx, 555, a.planSnapshotOf(p, p.Duration(1), 1))
	a.botCfg.AddSub.Enabled = true

	planTap(t, a, "addsub:sync")
	if fm.lastKBButton("addsub:sync:upd") == "" || fm.lastKBButton("addsub:sync:all") == "" {
		t.Fatalf("на экране нет двух режимов: %v", fm.allCallbackData())
	}

	syncTap(t, a, fm, "addsub:sync:upd")
	if panel.writesCount() != 0 || a.addSubSoldTo(ctx, 555) {
		t.Fatal("«обновить» выдало доп-подписку купившему без неё")
	}
	if !strings.Contains(fm.last(), "Обновить тем, у кого есть") {
		t.Fatalf("итог не называет режим: %q", fm.last())
	}

	syncTap(t, a, fm, "addsub:sync:all")
	if panel.writesCount() != 1 || !a.addSubSoldTo(ctx, 555) {
		t.Fatalf("«выдать всем» не выдало доп-подписку: записей %d", panel.writesCount())
	}
	if !strings.Contains(fm.last(), "Выдать всем") {
		t.Fatalf("итог не называет режим: %q", fm.last())
	}

	a.botCfg.AddSub.TrafficGB = 11
	syncTap(t, a, fm, "addsub:sync:upd")
	if bytes, _ := panel.last(t); panel.writesCount() != 2 || bytes != 11*gb {
		t.Fatalf("«обновить» не записало условия: записей %d, трафик %d", panel.writesCount(), bytes)
	}
}

// Опция продана, а доп-подписки в панели ещё нет: «обновить» её не создаёт,
// кнопка экрана прежних версий — создаёт.
func TestAddSubSync_UpdateCreatesNothing(t *testing.T) {
	a, fm, _, panel := addSubTermsApp(t)
	syncTap(t, a, fm, "addsub:sync:upd")
	if panel.writesCount() != 0 {
		t.Fatal("«обновить» создало доп-подписку")
	}
	syncTap(t, a, fm, "addsub:sync:go")
	if panel.writesCount() != 1 {
		t.Fatalf("кнопка прежнего экрана не создала доп-подписку: записей %d", panel.writesCount())
	}
}

// Экран выбора режима остаётся подписью к баннеру раздела: длиннее предела
// он ушёл бы обычным сообщением.
func TestAddSubSync_ConfirmFitsCaption(t *testing.T) {
	for _, lang := range []string{"ru", "en"} {
		text := i18n.T(lang, "addsub.sync_confirm")
		runes := len([]rune(text))
		units := len(utf16.Encode([]rune(stripHTMLTags(text))))
		if runes > 1000 || units > 1000 {
			t.Fatalf("%s: экран синхронизации — %d символов, %d единиц UTF-16; предел подписи 1000", lang, runes, units)
		}
	}
}

// «Обновить» не трогает доп-подписку, оставшуюся в панели от прошлой сделки,
// если нынешняя куплена без опции: её возвращает только «выдать всем».
func TestAddSubSync_UpdateSkipsUnsoldDeal(t *testing.T) {
	ctx := context.Background()
	a, _, fs, panel := addSubTermsApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)
	no := false
	_ = fs.SetUserSnapshot(ctx, 555, &model.PlanSnapshot{Code: p.Code, Months: 1, AddSub: &no})
	panel.created = true

	cli, _, opt := a.addSubOptions(false)
	opt.OnlyExisting = true
	if st := a.addSubBackfill(ctx, cli, opt); st.err != nil || st.ok != 0 || st.failed != 0 {
		t.Fatalf("синхронизация: %+v", st)
	}
	if panel.writesCount() != 0 || a.addSubSoldTo(ctx, 555) {
		t.Fatal("«обновить» вернуло доп-подписку купившему без неё")
	}
}

// Клиенту на триале синхронизация пишет общие условия, даже если у прошлой
// сделки были свои.
func TestAddSubBackfill_TrialUsesCommon(t *testing.T) {
	ctx := context.Background()
	a, _, fs, panel := addSubTermsApp(t)
	_ = fs.SetUserSnapshot(ctx, 555, &model.PlanSnapshot{Months: 1, AddSubTrafficGB: intp(25), AddSubSquads: []string{"s2"}})
	_ = fs.SetSubExpiry(ctx, 555, "2030-01-01T00:00:00Z", "trial")

	cli, _, opt := a.addSubOptions(false)
	if st := a.addSubBackfill(ctx, cli, opt); st.err != nil || st.ok != 1 {
		t.Fatalf("синхронизация: %+v", st)
	}
	if bytes, squads := panel.last(t); bytes != 10*gb || squads != "g1" {
		t.Fatalf("триал получил условия тарифа: %d %q", bytes, squads)
	}
}

// Доп-подписка создана, а шаг после неё упал: опция всё равно записана в
// снимок, иначе созданную выключило бы первое же продление бонусом.
func TestAddSubBackfill_GrantSurvivesPartialFailure(t *testing.T) {
	ctx := context.Background()
	a, _, fs, panel := addSubTermsApp(t)
	p := vipPlan(t, fs, model.PlanAvailAll)
	no := false
	_ = fs.SetUserSnapshot(ctx, 555, &model.PlanSnapshot{Code: p.Code, Months: 1, AddSub: &no})
	panel.created, panel.renamed = true, true

	cli, _, opt := a.addSubOptions(false)
	opt.MigrateLegacyName = true
	if st := a.addSubBackfill(ctx, cli, opt); st.err != nil || st.ok != 0 || st.failed != 1 {
		t.Fatalf("синхронизация: %+v", st)
	}
	if panel.writesCount() != 1 || !a.addSubSoldTo(ctx, 555) {
		t.Fatalf("созданная доп-подписка не отмечена в снимке: записей %d", panel.writesCount())
	}
}

// Запись опции в снимок ждёт замок выдачи: иначе она затёрла бы сделку,
// оплаченную в этот момент.
func TestAddSubGrant_WaitsForPurchaseLock(t *testing.T) {
	ctx := context.Background()
	a, _, fs, _ := addSubTermsApp(t)
	no, yes := false, true
	_ = fs.SetUserSnapshot(ctx, 555, &model.PlanSnapshot{Months: 1, AddSub: &no})

	lk := &a.finalizeUserLk[extLockIndex("555")]
	lk.Lock()
	done := make(chan error, 1)
	go func() { done <- a.grantAddSub(ctx, 555, &model.PlanSnapshot{Months: 1, AddSub: &yes}) }()
	select {
	case <-done:
		lk.Unlock()
		t.Fatal("снимок записан в обход замка выдачи")
	case <-time.After(100 * time.Millisecond):
	}
	lk.Unlock()
	if err := <-done; err != nil || !a.addSubSoldTo(ctx, 555) {
		t.Fatalf("опция не записана после освобождения замка: %v", err)
	}
}

// Сделка сменилась, пока создавалась доп-подписка: опция по прежней сделке в
// новую не записывается.
func TestAddSubGrant_SkipsChangedDeal(t *testing.T) {
	ctx := context.Background()
	a, _, fs, _ := addSubTermsApp(t)
	no, yes := false, true
	_ = fs.SetUserSnapshot(ctx, 555, &model.PlanSnapshot{Code: "newplannewpl", Months: 1, AddSub: &no})

	if err := a.grantAddSub(ctx, 555, &model.PlanSnapshot{Code: "vipvipvipvip", Months: 1, AddSub: &yes}); err != nil {
		t.Fatal(err)
	}
	if a.addSubSoldTo(ctx, 555) {
		t.Fatal("опция прежней сделки записана в новую")
	}
}

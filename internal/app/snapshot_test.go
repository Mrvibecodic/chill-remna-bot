package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// snapPanel — панель, запоминающая тело последнего PATCH/POST по пользователю.
func snapPanel(t *testing.T, got *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u1","tag":"CHILLBOT","username":"tg_555","subscriptionUrl":"https://sub/x","expireAt":"2030-01-01T00:00:00Z"}]}`))
			return
		}
		// Ловим только апдейт пользователя: следом идёт POST на сброс трафика
		// с пустым телом, и он затирал бы пойманный запрос.
		if (r.Method == http.MethodPatch || r.Method == http.MethodPost) && strings.HasSuffix(r.URL.Path, "/api/users") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			*got = body
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/x","expireAt":"2030-06-01T00:00:00Z"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func snapApp(t *testing.T, panelURL string) (*App, *fakeStore) {
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
		Currency: "₽",
		Base:     map[int]string{1: "150"},
		Traffic:  map[int]int{1: 50},
		Devices:  map[int]int{1: 3},
	}}
	a.botCfg.Plan.ActiveInternalSquads = []string{"squad-now"}
	a.botCfg.NormalizePricing()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panelURL, APIToken: "t"})
	return a, fs
}

// Главный смысл снимка: условия фиксируются при выставлении счёта. Если между
// «нажал купить» и «оплатил» админ поменял лимиты, применяться должно то, что
// человеку продали, а не то, что стало в конфиге.
func TestFinalizePurchase_AppliesSnapshotNotCurrentConfig(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)

	sold := a.planSnapshot(1)
	if sold.DeviceLimit != 3 || sold.TrafficGB != 50 || len(sold.IntSquads) != 1 {
		t.Fatalf("снимок снят неверно: %+v", sold)
	}

	// Админ правит тариф уже после выставления счёта.
	a.botCfg.Pricing.Devices[1] = 99
	a.botCfg.Pricing.Traffic[1] = 1
	a.botCfg.Plan.ActiveInternalSquads = []string{"squad-later"}

	if _, _, err := a.finalizePurchase(ctx, u, 1, model.PayMethodYooKassa, "150 ₽", "yk_snap_1", sold); err != nil {
		t.Fatal(err)
	}
	if patched == nil {
		t.Fatal("панель не получила запрос")
	}
	if got := patched["hwidDeviceLimit"]; got != float64(3) {
		t.Fatalf("применён лимит устройств из текущего конфига, а не из сделки: %v", got)
	}
	if got := patched["trafficLimitBytes"]; got != float64(50*1024*1024*1024) {
		t.Fatalf("применён трафик не из сделки: %v", got)
	}
	sq, _ := patched["activeInternalSquads"].([]any)
	if len(sq) != 1 || sq[0] != "squad-now" {
		t.Fatalf("применены сквады не из сделки: %v", patched["activeInternalSquads"])
	}
}

// Снимок обязан осесть и в платеже, и в пользователе: иначе продление и сверка
// лимитов снова начнут гадать по текущему конфигу.
func TestFinalizePurchase_PersistsSnapshot(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)

	if _, _, err := a.finalizePurchase(ctx, u, 1, model.PayMethodStars, "100 ⭐", "st_snap_1", nil); err != nil {
		t.Fatal(err)
	}
	paid, _ := fs.PaidPayments(ctx)
	if len(paid) != 1 || paid[0].Snapshot == nil {
		t.Fatalf("снимок не записан в платёж: %+v", paid)
	}
	if paid[0].Snapshot.DeviceLimit != 3 || paid[0].Snapshot.Months != 1 {
		t.Fatalf("снимок платежа неполный: %+v", paid[0].Snapshot)
	}
	got, _ := fs.GetUser(ctx, u)
	if got == nil || got.Snapshot == nil || got.Snapshot.TrafficGB != 50 {
		t.Fatalf("снимок не записан в пользователя: %+v", got)
	}
}

// Отпечаток входит в ключ идемпотентности автосписания: при смене условий он
// обязан меняться, иначе ЮKassa вернёт прежний платёж со старой суммой.
func TestSnapshotFingerprintChangesWithTerms(t *testing.T) {
	a := &model.PlanSnapshot{Months: 1, DeviceLimit: 3, Price: "150"}
	b := &model.PlanSnapshot{Months: 1, DeviceLimit: 3, Price: "200"}
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("отпечаток не изменился при смене цены")
	}
	if a.Fingerprint() != (&model.PlanSnapshot{Months: 1, DeviceLimit: 3, Price: "150"}).Fingerprint() {
		t.Fatal("отпечаток нестабилен при одинаковых условиях")
	}
	var nilSnap *model.PlanSnapshot
	if nilSnap.Fingerprint() == a.Fingerprint() {
		t.Fatal("пустой снимок не должен совпадать с непустым")
	}
}

// Отпечаток снимка описывает УСЛОВИЯ сделки. Код и имя тарифа в него входить
// не должны: иначе обновление бота (в снимках появился код) и переименование
// тарифа рассылали бы всем «условия автопродления изменились» на ровном месте.
func TestFingerprintIgnoresPlanIdentity(t *testing.T) {
	old := &model.PlanSnapshot{Months: 1, DeviceLimit: 3, TrafficGB: 50, Price: "150"}
	withPlan := &model.PlanSnapshot{Months: 1, DeviceLimit: 3, TrafficGB: 50, Price: "150",
		Code: "base", Name: "Базовый"}
	renamed := &model.PlanSnapshot{Months: 1, DeviceLimit: 3, TrafficGB: 50, Price: "150",
		Code: "base", Name: "Личный"}
	if old.Fingerprint() != withPlan.Fingerprint() {
		t.Fatalf("появление кода тарифа изменило отпечаток: %s против %s",
			old.Fingerprint(), withPlan.Fingerprint())
	}
	if withPlan.Fingerprint() != renamed.Fingerprint() {
		t.Fatalf("переименование тарифа изменило отпечаток: %s против %s",
			withPlan.Fingerprint(), renamed.Fingerprint())
	}
	// А изменение самих условий по-прежнему видно.
	cheaper := &model.PlanSnapshot{Months: 1, DeviceLimit: 3, TrafficGB: 50, Price: "100"}
	if old.Fingerprint() == cheaper.Fingerprint() {
		t.Fatal("изменение цены обязано менять отпечаток")
	}
}

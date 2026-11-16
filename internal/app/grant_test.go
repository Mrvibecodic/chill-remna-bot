package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// grantPanel — панель для выдачи: учётка бота с заданным статусом и концом
// срока, запоминает тела PATCH/POST по пользователю.
type grantPanel struct {
	mu      sync.Mutex
	status  string
	expire  string
	patches []map[string]any
	resets  int
	// failPatch — панель отвечает ошибкой на продление; failFind — на поиск.
	failPatch bool
	failFind  bool
}

func (p *grantPanel) patchCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.patches)
}

func newGrantPanel(t *testing.T, expire string) (*grantPanel, *httptest.Server) {
	t.Helper()
	gp := &grantPanel{status: "ACTIVE", expire: expire}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		gp.mu.Lock()
		defer gp.mu.Unlock()
		path := r.URL.Path
		if gp.failFind && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"message":"upstream timeout"}`))
			return
		}
		switch {
		case strings.Contains(path, "/by-username/"):
			// Аккаунт кабинета по почте ищется по имени учётки бота.
			name := path[strings.LastIndex(path, "/")+1:]
			_, _ = w.Write([]byte(`{"response":{"uuid":"u2","tag":"CHILLBOT","username":"` + name + `","status":"` + gp.status +
				`","subscriptionUrl":"https://sub.example.com/y","expireAt":"` + gp.expire + `"}}`))
		case strings.Contains(path, "/by-telegram-id/"):
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u1","tag":"CHILLBOT","username":"tg_555","telegramId":555,"status":"` + gp.status +
				`","subscriptionUrl":"https://sub.example.com/x","expireAt":"` + gp.expire + `"}]}`))
		case strings.HasSuffix(path, "/internal-squads"):
			_, _ = w.Write([]byte(`{"response":{"internalSquads":[{"uuid":"sq-vip","name":"Fast <EU>"}]}}`))
		case strings.HasSuffix(path, "/external-squads"):
			_, _ = w.Write([]byte(`{"response":{"externalSquads":[]}}`))
		case strings.Contains(path, "reset-traffic"):
			gp.resets++
			_, _ = w.Write([]byte(`{"response":{"uuid":"u1"}}`))
		case (r.Method == http.MethodPatch || r.Method == http.MethodPost) && strings.HasSuffix(path, "/api/users") && gp.failPatch:
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"message":"upstream timeout"}`))
		case (r.Method == http.MethodPatch || r.Method == http.MethodPost) && strings.HasSuffix(path, "/api/users"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gp.patches = append(gp.patches, body)
			exp, _ := body["expireAt"].(string)
			_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub.example.com/x","expireAt":"` + exp + `"}}`))
		default:
			_, _ = w.Write([]byte(`{"response":{"uuid":"u1"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return gp, srv
}

const grantUser int64 = 555

func grantApp(t *testing.T, panelURL string) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:       &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:       fm,
		wiz:       map[int64]*wizard{},
		ui:        map[int64]*uiState{},
		store:     fs,
		runInline: true,
	}
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru", Pricing: model.Pricing{
		Currency: "₽",
		Base:     map[int]string{1: "150"},
		Traffic:  map[int]int{1: 50},
		Devices:  map[int]int{1: 3},
	}}
	a.botCfg.NormalizePricing()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panelURL, APIToken: "t"})
	ctx := context.Background()
	_ = fs.SavePlan(ctx, &model.Plan{Code: "vip", Name: "Премиум", Enabled: true, Currency: "₽",
		TrafficGB: 300, DeviceLimit: 5, IntSquads: []string{"sq-vip"},
		Durations: []model.PlanDuration{{Months: 1, Base: "300"}, {Months: 3, Base: "800"}}})
	_ = fs.UpsertUser(ctx, grantUser)
	return a, fm, fs
}

// grantNonce — номер сделки с кнопки «Выдать» последнего подтверждения.
func grantNonce(t *testing.T, fm *fakeMsg) string {
	t.Helper()
	return grantNonceFor(t, fm, "555")
}

func grantNonceFor(t *testing.T, fm *fakeMsg, uid string) string {
	t.Helper()
	data := fm.allCallbackData()
	for i := len(data) - 1; i >= 0; i-- {
		if rest, ok := strings.CutPrefix(data[i], "usr:grok:"+uid+":"); ok {
			return rest
		}
	}
	t.Fatalf("нет кнопки «Выдать»; экраны:\n%s", fm.joined())
	return ""
}

func grantPayments(fs *fakeStore) []*model.Payment {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var out []*model.Payment
	for _, p := range fs.pays {
		if p.Method == model.PayMethodGrant {
			out = append(out, p)
		}
	}
	return out
}

// Полный путь: карточка → тариф → срок → подтверждение → выдача. Подписка
// выдана по условиям тарифа, записана сделкой «выдача», клиент оповещён, а
// выручкой и «платил» выдача не считается.
func TestGrant_FullFlow(t *testing.T) {
	gp, srv := newGrantPanel(t, time.Now().UTC().AddDate(0, 0, 10).Format(time.RFC3339))
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()
	// Пригласивший не получает ни бонуса, ни процента: выдача — не оплата.
	a.botCfg.Referral = model.ReferralConfig{Enabled: true, Init: true, BonusKind: model.ReferralBonusBalance,
		BonusValue: 50, OnFirstPay: true, Percent: 10}
	_ = fs.UpsertUser(ctx, 777)
	fs.users[grantUser].ReferredBy = 777

	a.handleCallback(ctx, cb(100, "usr:view:555"))
	if !strings.Contains(strings.Join(fm.allCallbackData(), " "), "usr:gr:555") {
		t.Fatal("в карточке нет кнопки «Выдать подписку»")
	}
	a.handleCallback(ctx, cb(100, "usr:gr:555"))
	if !strings.Contains(fm.last(), "Выдать подписку") {
		t.Fatalf("экран выбора тарифа: %s", fm.last())
	}
	a.handleCallback(ctx, cb(100, "usr:grp:555:"+planEditHash("vip")))
	if !strings.Contains(fm.last(), "Выдать «Премиум»") {
		t.Fatalf("экран сроков: %s", fm.last())
	}
	a.handleCallback(ctx, cb(100, "usr:grt:555:3"))
	confirm := fm.last()
	for _, want := range []string{"Проверьте выдачу", "Премиум", "3 мес", "Устройств: до 5", "Fast &lt;EU&gt;", "Счётчик трафика обнулится"} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("в подтверждении нет %q:\n%s", want, confirm)
		}
	}
	nonce := grantNonce(t, fm)
	a.handleCallback(ctx, cb(100, "usr:grok:555:"+nonce))

	if gp.patchCount() != 1 {
		t.Fatalf("PATCH в панель: %d, ждём 1", gp.patchCount())
	}
	body := gp.patches[0]
	if body["hwidDeviceLimit"] != float64(5) || body["trafficLimitBytes"] != float64(300*1024*1024*1024) {
		t.Fatalf("условия в панель не из тарифа: %v", body)
	}
	pays := grantPayments(fs)
	if len(pays) != 1 {
		t.Fatalf("записей о выдаче: %d", len(pays))
	}
	p := pays[0]
	if p.Status != model.PaymentPaid || p.Months != 3 || p.Snapshot == nil || p.Snapshot.Code != "vip" ||
		!strings.HasPrefix(p.ExtID, "grant:555:") {
		t.Fatalf("запись о выдаче: %+v", p)
	}
	u, _ := fs.GetUser(ctx, grantUser)
	if u.Snapshot == nil || u.Snapshot.Code != "vip" || u.NotifyKind != "paid" {
		t.Fatalf("пользователь после выдачи: %+v", u)
	}
	if !strings.Contains(fm.joined(), "Клиент получил сообщение") {
		t.Fatalf("в итоге нет доставки клиенту:\n%s", fm.joined())
	}
	if !strings.Contains(fm.joined(), "Вам выдана подписка") || !strings.Contains(fm.joined(), "тариф «Премиум» на 3 месяца") {
		t.Fatalf("клиенту не пришло сообщение:\n%s", fm.joined())
	}
	if r, _ := fs.GetUser(ctx, 777); r.Balance != 0 || r.RefEarned != 0 || fs.users[grantUser].RefBonusPaid {
		t.Fatalf("выдача начислила рефералку: баланс %d, заработано %d", r.Balance, r.RefEarned)
	}
	if paid, _ := fs.HasPaidPayment(ctx, grantUser); paid {
		t.Fatal("выдача засчитана как оплата")
	}
	if list, _ := fs.PaidPayments(ctx); len(list) != 0 {
		t.Fatalf("выдача попала в выручку: %+v", list)
	}
	if last, _ := fs.LastPaidSubPayment(ctx, grantUser); last == nil || last.Method != model.PayMethodGrant {
		t.Fatalf("выдача не последняя сделка (сверка лимитов откатит её): %+v", last)
	}

	// Повторное нажатие той же кнопки — без второй выдачи.
	a.handleCallback(ctx, cb(100, "usr:grok:555:"+nonce))
	if gp.patchCount() != 1 || len(grantPayments(fs)) != 1 {
		t.Fatalf("повторное нажатие выдало ещё раз: PATCH %d, записей %d", gp.patchCount(), len(grantPayments(fs)))
	}
}

// Свой срок в днях: условия с самого короткого срока, длительность — днями,
// стоимость окна — долей цены.
func TestGrant_CustomDays(t *testing.T) {
	gp, srv := newGrantPanel(t, time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339))
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "usr:gr:555"))
	a.handleCallback(ctx, cb(100, "usr:grp:555:"+planEditHash("vip")))
	a.handleCallback(ctx, cb(100, "usr:grd:555"))
	a.handleMessage(ctx, msgText(100, "0"))
	if !strings.Contains(fm.last(), "от 1 до 3650") {
		t.Fatalf("неверное число принято: %s", fm.last())
	}
	a.handleMessage(ctx, msgText(100, "10"))
	if !strings.Contains(fm.last(), "10 дн.") {
		t.Fatalf("подтверждение своего срока: %s", fm.last())
	}
	a.handleCallback(ctx, cb(100, "usr:grok:555:"+grantNonce(t, fm)))
	pays := grantPayments(fs)
	if len(pays) != 1 || pays[0].Months != 1 || pays[0].Snapshot.Days != 10 || pays[0].Snapshot.Paid != "100" {
		t.Fatalf("выдача днями: %+v / %+v", pays, pays[0].Snapshot)
	}
	exp, _ := time.Parse(time.RFC3339, gp.patches[0]["expireAt"].(string))
	if d := time.Until(exp).Hours() / 24; d < 9.9 || d > 10.1 {
		t.Fatalf("срок в панели: %.2f дн., ждём 10", d)
	}
	if !strings.Contains(fm.joined(), "на 10 дней") {
		t.Fatalf("клиенту срок не словами:\n%s", fm.joined())
	}
}

// Заблокированному (в боте или подпиской) выдать нельзя: продление не включает
// отключённую подписку.
func TestGrant_BlockedRefused(t *testing.T) {
	gp, srv := newGrantPanel(t, time.Now().UTC().AddDate(0, 0, 10).Format(time.RFC3339))
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()

	gp.status = "DISABLED"
	a.handleCallback(ctx, cb(100, "usr:gr:555"))
	if !strings.Contains(fm.last(), "заблокирован") || strings.Contains(strings.Join(fm.allCallbackData(), " "), "usr:grp:") {
		t.Fatalf("отключённой подписке предложена выдача: %s", fm.last())
	}

	gp.status = "ACTIVE"
	a.handleCallback(ctx, cb(100, "usr:gr:555"))
	a.handleCallback(ctx, cb(100, "usr:grp:555:"+planEditHash("vip")))
	a.handleCallback(ctx, cb(100, "usr:grt:555:1"))
	nonce := grantNonce(t, fm)
	fs.users[grantUser].Blocked = true
	a.handleCallback(ctx, cb(100, "usr:grok:555:"+nonce))
	if gp.patchCount() != 0 || len(grantPayments(fs)) != 0 {
		t.Fatal("заблокированному выдано")
	}
}

// Кнопка старого экрана и изменённые условия тарифа ничего не выдают.
func TestGrant_StaleAndChanged(t *testing.T) {
	gp, srv := newGrantPanel(t, time.Now().UTC().AddDate(0, 0, 10).Format(time.RFC3339))
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "usr:grok:555:deadbeef0000"))
	if !strings.Contains(fm.joined(), "Выбор устарел") || gp.patchCount() != 0 {
		t.Fatalf("кнопка без выбора: %s", fm.last())
	}

	a.handleCallback(ctx, cb(100, "usr:gr:555"))
	a.handleCallback(ctx, cb(100, "usr:grp:555:"+planEditHash("vip")))
	a.handleCallback(ctx, cb(100, "usr:grt:555:1"))
	nonce := grantNonce(t, fm)
	p, _ := fs.GetPlan(ctx, "vip")
	p.DeviceLimit = 7
	_ = fs.SavePlan(ctx, p)
	a.handleCallback(ctx, cb(100, "usr:grok:555:"+nonce))
	if gp.patchCount() != 0 || !strings.Contains(fm.last(), "Условия тарифа изменились") || !strings.Contains(fm.last(), "до 7") {
		t.Fatalf("выдано по устаревшим условиям: PATCH %d\n%s", gp.patchCount(), fm.last())
	}
	if nonce == grantNonce(t, fm) {
		t.Fatal("новое подтверждение со старым номером сделки")
	}
}

// Тариф «по списку»: выданный человек попадает в список — иначе «Продлить»
// сочтёт тариф закрытым.
func TestGrant_ListPlanAddsAccess(t *testing.T) {
	_, srv := newGrantPanel(t, time.Now().UTC().AddDate(0, 0, 10).Format(time.RFC3339))
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()
	p, _ := fs.GetPlan(ctx, "vip")
	p.Availability = model.PlanAvailList
	_ = fs.SavePlan(ctx, p)

	a.handleCallback(ctx, cb(100, "usr:gr:555"))
	if !strings.Contains(strings.Join(fm.allCallbackData(), " "), "usr:grp:555:"+planEditHash("vip")) {
		t.Fatal("тариф «по списку» не предложен к выдаче")
	}
	a.handleCallback(ctx, cb(100, "usr:grp:555:"+planEditHash("vip")))
	a.handleCallback(ctx, cb(100, "usr:grt:555:1"))
	if !strings.Contains(fm.last(), "будет добавлен в список") {
		t.Fatalf("нет пояснения про список: %s", fm.last())
	}
	if ok, _ := fs.HasPlanAccess(ctx, "vip", grantUser, ""); ok {
		t.Fatal("допуск выдан до выдачи")
	}
	a.handleCallback(ctx, cb(100, "usr:grok:555:"+grantNonce(t, fm)))
	if ok, _ := fs.HasPlanAccess(ctx, "vip", grantUser, ""); !ok {
		t.Fatal("после выдачи человек не в списке тарифа")
	}
	if !a.planAccessibleFor(ctx, p, grantUser) {
		t.Fatal("выданный тариф «по списку» человеку закрыт")
	}
	// Тариф выбрал администратор: «тариф стал недоступен покупателю» здесь ложь.
	if strings.Contains(fm.joined(), "Оплачен тариф, который сейчас недоступен") {
		t.Fatalf("админу ушло предупреждение о доступности тарифа:\n%s", fm.joined())
	}
}

// Новое поле снимка не меняет отпечаток условий: иначе всем подписчикам
// пришло бы «условия изменились».
func TestFingerprintIgnoresSwitchDays(t *testing.T) {
	a := &model.PlanSnapshot{Code: "vip", Months: 1, DeviceLimit: 3, Price: "150"}
	b := *a
	b.SwitchDays = 12
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("SwitchDays попал в отпечаток условий")
	}
}

// Зачёт при смене тарифа записывается в снимок сделки — его показывает
// карточка платежа.
func TestFinalize_RecordsSwitchDays(t *testing.T) {
	var patched map[string]any
	srv := snapPanelExpiry(t, &patched, time.Now().UTC().AddDate(0, 0, 20).Format(time.RFC3339))
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	fs.users[555].Snapshot = &model.PlanSnapshot{Code: "base", Months: 1, Price: "150", Currency: "₽"}
	fs.users[555].SubExpireAt = time.Now().UTC().AddDate(0, 0, 20).Format(time.RFC3339)
	snap := &model.PlanSnapshot{Code: "vip", Months: 1, Price: "300", Currency: "₽"}
	if _, _, err := a.finalizePurchase(ctx, 555, 1, model.PayMethodYooKassa, "300 ₽", "yk_sw_1", snap); err != nil {
		t.Fatal(err)
	}
	paid, _ := fs.PaidPayments(ctx)
	if len(paid) != 1 || paid[0].Snapshot == nil || paid[0].Snapshot.SwitchDays >= 0 {
		t.Fatalf("дни зачёта не записаны: %+v", paid[0].Snapshot)
	}
}

// Панель не продлила: ничего не записано, админ видит причину и карточку со
// сроком из панели, а не приглашение повторить вслепую.
func TestGrant_PanelFailure(t *testing.T) {
	gp, srv := newGrantPanel(t, time.Now().UTC().AddDate(0, 0, 10).Format(time.RFC3339))
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()
	gp.failPatch = true

	a.handleCallback(ctx, cb(100, "usr:gr:555"))
	a.handleCallback(ctx, cb(100, "usr:grp:555:"+planEditHash("vip")))
	a.handleCallback(ctx, cb(100, "usr:grt:555:1"))
	a.handleCallback(ctx, cb(100, "usr:grok:555:"+grantNonce(t, fm)))
	if len(grantPayments(fs)) != 0 {
		t.Fatal("после отказа панели выдача записана")
	}
	all := fm.joined()
	if !strings.Contains(all, "Выдать подписку не удалось") || !strings.Contains(all, "сверьте срок в карточке") {
		t.Fatalf("нет честного итога ошибки:\n%s", all)
	}
	if !strings.Contains(fm.last(), "Подписка активна до") {
		t.Fatalf("после ошибки нет карточки со сроком: %s", fm.last())
	}
	if a.getUI(100).grant != nil {
		t.Fatal("выбор выдачи не снят")
	}
}

// Аккаунт кабинета по почте: выдача проходит, сообщение в Telegram не
// отправляется — чата у такого аккаунта нет.
func TestGrant_EmailAccount(t *testing.T) {
	gp, srv := newGrantPanel(t, time.Now().UTC().AddDate(0, 0, 10).Format(time.RFC3339))
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()
	const uid int64 = -1000000031
	_ = fs.UpsertUser(ctx, uid)

	a.handleCallback(ctx, cb(100, "usr:gr:-1000000031"))
	a.handleCallback(ctx, cb(100, "usr:grp:-1000000031:"+planEditHash("vip")))
	a.handleCallback(ctx, cb(100, "usr:grt:-1000000031:1"))
	a.handleCallback(ctx, cb(100, "usr:grok:-1000000031:"+grantNonceFor(t, fm, "-1000000031")))
	if gp.patchCount() != 1 {
		t.Fatalf("PATCH в панель: %d", gp.patchCount())
	}
	pays := grantPayments(fs)
	if len(pays) != 1 || pays[0].TelegramID != uid {
		t.Fatalf("выдача почтовому аккаунту не записана: %+v", pays)
	}
	all := fm.joined()
	if strings.Contains(all, "Вам выдана подписка") || strings.Contains(all, "Клиент получил сообщение") ||
		!strings.Contains(all, "нет Telegram — сообщение не отправлялось") {
		t.Fatalf("итог выдачи аккаунту без чата:\n%s", all)
	}
	if !strings.Contains(all, "Подписка выдана") {
		t.Fatalf("нет итога админу:\n%s", all)
	}
}

// Панель не ответила — карточка так и пишет, а не «подписки нет».
func TestUserCard_PanelDown(t *testing.T) {
	gp, srv := newGrantPanel(t, time.Now().UTC().AddDate(0, 0, 10).Format(time.RFC3339))
	a, fm, _ := grantApp(t, srv.URL)
	ctx := context.Background()
	gp.failFind = true
	a.handleCallback(ctx, cb(100, "usr:view:555"))
	if !strings.Contains(fm.last(), "Панель не ответила") || strings.Contains(fm.last(), "Активной подписки в панели нет") {
		t.Fatalf("карточка при недоступной панели: %s", fm.last())
	}
}

// Telegram не принял сообщение клиенту — итог так и говорит.
func TestGrant_ReportClientNotDelivered(t *testing.T) {
	_, srv := newGrantPanel(t, time.Now().UTC().AddDate(0, 0, 10).Format(time.RFC3339))
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "usr:gr:555"))
	a.handleCallback(ctx, cb(100, "usr:grp:555:"+planEditHash("vip")))
	a.handleCallback(ctx, cb(100, "usr:grt:555:1"))
	nonce := grantNonce(t, fm)
	fm.kbFail = true
	a.handleCallback(ctx, cb(100, "usr:grok:555:"+nonce))
	all := fm.joined()
	if len(grantPayments(fs)) != 1 || !strings.Contains(all, "Сообщение клиенту не доставлено") || strings.Contains(all, "Клиент получил сообщение") {
		t.Fatalf("итог при недоставке:\n%s", all)
	}
}

type grantListFailStore struct{ *fakeStore }

func (grantListFailStore) GrantPlanAccess(context.Context, string, int64, string) error {
	return errors.New("база недоступна")
}

// Допуск к тарифу «по списку» не записался — админ узнаёт об этом из итога.
func TestGrant_ReportListFailed(t *testing.T) {
	_, srv := newGrantPanel(t, time.Now().UTC().AddDate(0, 0, 10).Format(time.RFC3339))
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()
	p, _ := fs.GetPlan(ctx, "vip")
	p.Availability = model.PlanAvailList
	_ = fs.SavePlan(ctx, p)
	a.store = grantListFailStore{fs}
	a.handleCallback(ctx, cb(100, "usr:gr:555"))
	a.handleCallback(ctx, cb(100, "usr:grp:555:"+planEditHash("vip")))
	a.handleCallback(ctx, cb(100, "usr:grt:555:1"))
	a.handleCallback(ctx, cb(100, "usr:grok:555:"+grantNonce(t, fm)))
	if !strings.Contains(fm.joined(), "Добавить в список тарифа не удалось") {
		t.Fatalf("сбой допуска не показан:\n%s", fm.joined())
	}
}

// Предупреждение про автопродление и итог считаются той же проверкой, что и
// сам переход автосписания: есть переход — есть и строка, нет — нет.
func TestGrant_AutoPayWarningMatchesAction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  string
		moves bool
	}{
		{"другой тариф", "base", true},
		{"снимок без кода", "", true},
		{"тот же тариф", "vip", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, srv := newGrantPanel(t, time.Now().UTC().AddDate(0, 0, 10).Format(time.RFC3339))
			a, fm, fs := grantApp(t, srv.URL)
			ctx := context.Background()
			var snap *model.PlanSnapshot
			if tc.code != "" {
				snap = &model.PlanSnapshot{Code: tc.code, Months: 1}
			}
			_ = fs.SetAutoPay(ctx, &model.AutoPay{TelegramID: grantUser, Method: model.PayMethodYooKassa, MethodID: "pm_1",
				Months: 1, Enabled: true, Snapshot: snap})
			a.handleCallback(ctx, cb(100, "usr:gr:555"))
			a.handleCallback(ctx, cb(100, "usr:grp:555:"+planEditHash("vip")))
			a.handleCallback(ctx, cb(100, "usr:grt:555:1"))
			warned := strings.Contains(fm.last(), "Включено автопродление")
			a.handleCallback(ctx, cb(100, "usr:grok:555:"+grantNonce(t, fm)))
			reported := strings.Contains(fm.joined(), "Автопродление перешло на этот тариф")
			ap, _ := fs.GetAutoPay(ctx, grantUser)
			moved := ap != nil && planCodeOf(ap.Snapshot) == "vip" && tc.code != "vip"
			if warned != tc.moves || reported != tc.moves || moved != tc.moves {
				t.Fatalf("предупреждение %v, итог %v, переход %v; ждём %v", warned, reported, moved, tc.moves)
			}
		})
	}
}

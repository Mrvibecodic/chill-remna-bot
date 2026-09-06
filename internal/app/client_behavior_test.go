package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

func reminderApp(t *testing.T) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		store: fs,
		ui:    map[int64]*uiState{},
	}
	a.botCfg = &model.BotConfig{
		Installed: true, Language: "ru",
		Reminders: model.RemindersConfig{Enabled: true, DaysList: []int{3, 1}, TrialEnabled: true, TrialDaysBefore: 1, Init: true},
	}
	return a, fm, fs
}

// Telegram не принял сообщение — окно напоминания обязано остаться
// открытым, иначе единственное «подписка кончается» пропадает навсегда.
func TestReminders_FailedSendKeepsWindowOpen(t *testing.T) {
	a, fm, fs := reminderApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = fs.UpsertUser(ctx, 777)
	_ = fs.SetSubExpiry(ctx, 777, now.Add(48*time.Hour).Format(time.RFC3339), "paid")

	fm.kbFail = true
	a.remindOnce(ctx)
	if u, _ := fs.GetUser(ctx, 777); u == nil || u.NotifySent != "" {
		t.Fatalf("окно закрыто при неудачной отправке: %+v", u)
	}

	fm.kbFail = false
	a.remindOnce(ctx)
	u, _ := fs.GetUser(ctx, 777)
	if u == nil || !strings.Contains(u.NotifySent, "3") {
		t.Fatalf("после успешной отправки окно должно закрыться: %+v", u)
	}
}

// Человек заблокировал бота — Telegram отказывает
// всегда. Окно обязано закрыться после нескольких попыток, иначе запись
// дёргает Telegram каждые полчаса до самого истечения подписки.
func TestReminders_FailedSendGivesUp(t *testing.T) {
	a, fm, fs := reminderApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = fs.UpsertUser(ctx, 777)
	_ = fs.SetSubExpiry(ctx, 777, now.Add(48*time.Hour).Format(time.RFC3339), "paid")

	fm.kbFail = true
	for i := 0; i < remindSendTries; i++ {
		a.remindOnce(ctx)
	}
	u, _ := fs.GetUser(ctx, 777)
	if u == nil || !strings.Contains(u.NotifySent, "3") {
		t.Fatalf("после %d отказов окно должно закрыться: %+v", remindSendTries, u)
	}
	before := len(fm.texts)
	a.remindOnce(ctx)
	if len(fm.texts) != before {
		t.Fatalf("окно закрыто, а попытки продолжаются")
	}
}

// Заблокированному напоминания не шлём — бот ему уже отказал во всём
// остальном.
func TestReminders_SkipsBlocked(t *testing.T) {
	a, fm, fs := reminderApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = fs.UpsertUser(ctx, 777)
	_ = fs.SetSubExpiry(ctx, 777, now.Add(48*time.Hour).Format(time.RFC3339), "paid")
	_ = fs.SetBlocked(ctx, 777, true)

	a.remindOnce(ctx)
	if len(fm.texts) != 0 {
		t.Fatalf("заблокированному ушло напоминание: %v", fm.texts)
	}
}

// У аккаунта кабинета по почте чата нет — отправлять некуда.
func TestReminders_SkipsAccountsWithoutChat(t *testing.T) {
	a, fm, fs := reminderApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = fs.UpsertUser(ctx, -42)
	_ = fs.SetSubExpiry(ctx, -42, now.Add(48*time.Hour).Format(time.RFC3339), "paid")

	a.remindOnce(ctx)
	if len(fm.texts) != 0 {
		t.Fatalf("напоминание ушло в несуществующий чат: %v", fm.texts)
	}
}

// Срок правили в панели — напоминание считает дни по панели, а не по
// отставшему полю в базе бота.
func TestReminders_TrustsPanelExpiry(t *testing.T) {
	a, fm, fs := reminderApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = fs.UpsertUser(ctx, 777)
	_ = fs.SetSubExpiry(ctx, 777, now.Add(48*time.Hour).Format(time.RFC3339), "paid")

	// В панели подписки нет вовсе — писать «скоро кончится» нельзя.
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[]}`))
	}))
	defer gone.Close()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: gone.URL, APIToken: "t"})
	a.remindOnce(ctx)
	if len(fm.texts) != 0 {
		t.Fatalf("подписки в панели нет, а напоминание ушло: %v", fm.texts)
	}
	// Ответ панели «такого нет» — окончательный: окно закрываем, иначе запись
	// дёргала бы панель каждые полчаса до самого истечения подписки.
	if u, _ := fs.GetUser(ctx, 777); u == nil || u.NotifySent == "" {
		t.Fatalf("окно не закрыто — тик будет ходить в панель вечно: %+v", u)
	}

	// А вот молчание панели окно закрывать не должно: ответа не было.
	_ = fs.SetSubExpiry(ctx, 777, now.Add(48*time.Hour).Format(time.RFC3339), "paid")
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: dead.URL, APIToken: "t"})
	a.remindOnce(ctx)
	if len(fm.texts) != 0 {
		t.Fatalf("панель молчит, а напоминание ушло: %v", fm.texts)
	}
	if u, _ := fs.GetUser(ctx, 777); u == nil || u.NotifySent != "" {
		t.Fatalf("молчание панели закрыло окно: %+v", u)
	}

	// В панели срок продлили на месяц — рано напоминать.
	far := now.Add(30 * 24 * time.Hour).Format(time.RFC3339)
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[{"username":"u","status":"ACTIVE","expireAt":"` + far +
			`","subscriptionUrl":"https://sub.example/x","telegramId":777}]}`))
	}))
	defer ext.Close()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: ext.URL, APIToken: "t"})
	a.remindOnce(ctx)
	if len(fm.texts) != 0 {
		t.Fatalf("срок в панели продлён, а бот пишет «скоро кончится»: %v", fm.texts)
	}
}

func rwSigned(t *testing.T, a *App, body string) {
	t.Helper()
	m := hmac.New(sha256.New, []byte(a.botCfg.Webhook.RemnawaveSecret))
	m.Write([]byte(body))
	if _, err := a.HandleRemnawaveWebhook(context.Background(), hex.EncodeToString(m.Sum(nil)), []byte(body)); err != nil {
		t.Fatalf("доставка события: %v", err)
	}
}

// Панель переотправляет событие, не дождавшись ответа. Три доставки —
// одно сообщение человеку.
func TestRemnawaveWebhook_RetryDoesNotDuplicate(t *testing.T) {
	a, fm, _ := torrentApp(t)
	body := `{"scope":"user","event":"user.expired","timestamp":"2026-09-06T10:00:00.000Z","data":{"telegramId":42}}`
	for i := 0; i < 3; i++ {
		rwSigned(t, a, body)
	}
	if n := countContains(fm.texts, "Подписка истекла"); n != 1 {
		t.Fatalf("повторы дали %d сообщений вместо одного: %v", n, fm.texts)
	}
	// Telegram отказал — повтор панели обязан пройти: иначе дедуп выключает
	// ровно тот механизм, ради которого нужен.
	fm2 := &fakeMsg{kbFail: true}
	a2, _, _ := torrentApp(t)
	a2.msg = fm2
	dead := `{"scope":"user","event":"user.expired","timestamp":"2026-09-06T09:00:00.000Z","data":{"telegramId":42}}`
	rwSigned(t, a2, dead)
	fm2.kbFail = false
	rwSigned(t, a2, dead)
	if n := countContains(fm2.texts, "Подписка истекла"); n != 2 {
		t.Fatalf("повтор после отказа Telegram проглочен: %v", fm2.texts)
	}

	// Другое событие того же человека (другой timestamp) не глотается.
	rwSigned(t, a, strings.Replace(body, "10:00:00", "12:00:00", 1))
	if n := countContains(fm.texts, "Подписка истекла"); n != 2 {
		t.Fatalf("новое событие проглочено дедупликацией: %v", fm.texts)
	}
}

// Отключение и включение доступа в панели человек обязан увидеть, а
// кэш «есть подписка» — сброситься.
func TestRemnawaveWebhook_DisabledEnabled(t *testing.T) {
	a, fm, _ := torrentApp(t)
	a.subMu.Lock()
	a.subCache = map[int64]subCacheEntry{42: {has: true, expireAt: time.Now().Add(time.Hour)}}
	a.subMu.Unlock()

	rwSigned(t, a, `{"scope":"user","event":"user.disabled","timestamp":"2026-09-06T10:00:00.000Z","data":{"telegramId":42}}`)
	if countContains(fm.texts, "Доступ приостановлен") != 1 {
		t.Fatalf("об отключении не сообщили: %v", fm.texts)
	}
	a.subMu.Lock()
	_, cached := a.subCache[42]
	a.subMu.Unlock()
	if cached {
		t.Fatalf("кэш подписки не сброшен — бот полминуты врёт, что доступ есть")
	}

	rwSigned(t, a, `{"scope":"user","event":"user.enabled","timestamp":"2026-09-06T11:00:00.000Z","data":{"telegramId":42}}`)
	if countContains(fm.texts, "Доступ восстановлен") != 1 {
		t.Fatalf("о включении не сообщили: %v", fm.texts)
	}
}

// Страница подписки не отвечает — второй заход не должен снова ждать
// обхода всех путей.
func TestAppConfig_NegativeResultCached(t *testing.T) {
	a, _, _ := newTestApp(t)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if ce := a.fetchAppConfig(context.Background(), srv.URL, srv.URL+"/sub/x"); ce != nil {
		t.Fatalf("конфига нет, а вернулся: %+v", ce)
	}
	first := hits
	if first == 0 {
		t.Fatalf("первый заход вообще не сходил на страницу")
	}
	if ce := a.fetchAppConfig(context.Background(), srv.URL, srv.URL+"/sub/x"); ce != nil {
		t.Fatalf("конфига нет, а вернулся: %+v", ce)
	}
	if hits != first {
		t.Fatalf("отрицательный ответ не закэширован: запросов было %d, стало %d", first, hits)
	}
}

// У бота, который конфиг уже видел, отрицательный кэш
// тоже обязан работать — иначе правка не помогает никому, кто хоть раз
// открывал «Подключить» успешно.
func TestAppConfig_NegativeCacheWithStalePositive(t *testing.T) {
	a, _, _ := newTestApp(t)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a.connectMu.Lock()
	a.connectCache = &connectCacheEntry{base: srv.URL, std: &appConfig{}, fetchedAt: time.Now().Add(-time.Hour)}
	a.connectMu.Unlock()

	a.fetchAppConfig(context.Background(), srv.URL, srv.URL+"/sub/x")
	first := hits
	a.fetchAppConfig(context.Background(), srv.URL, srv.URL+"/sub/x")
	if hits != first {
		t.Fatalf("протухшая запись отключила отрицательный кэш: было %d, стало %d", first, hits)
	}
}

// Отменённый запрос (человек закрыл мини-апп) не должен отравлять кэш
// остальным: страница-то жива.
func TestAppConfig_CancelledRequestDoesNotPoisonCache(t *testing.T) {
	a, _, _ := newTestApp(t)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"platforms":{"ios":[{"id":"a","name":"App","installationStep":{"buttons":[{"buttonLink":"https://x","buttonText":{"en":"go"}}]}}]}}`))
	}))
	defer srv.Close()

	dead, cancel := context.WithCancel(context.Background())
	cancel()
	a.fetchAppConfig(dead, srv.URL, srv.URL+"/sub/x")

	if ce := a.fetchAppConfig(context.Background(), srv.URL, srv.URL+"/sub/x"); ce == nil {
		t.Fatalf("отменённый запрос отравил кэш: страница жива, а конфига нет")
	}
}

// /: подмена «сырой» ошибки безопасным текстом не должна съедать
// сообщения, которые изначально написаны для человека.
func TestClientErr_KeepsHumanMessages(t *testing.T) {
	a, _, _ := newTestApp(t)
	ctx := context.Background()
	msg := "Пополнение баланса отключено."
	if got := a.clientErr(ctx, 1, "тест", errUserText(msg)); got != msg {
		t.Fatalf("готовый текст подменён: %q", got)
	}
	// А внутренности наружу по-прежнему не уходят.
	raw := errors.New(`нет связи с панелью: Get "http://127.0.0.1:44617/api/users/by-telegram-id/9530": context deadline exceeded`)
	got := a.clientErr(ctx, 1, "тест", raw)
	for _, leak := range []string{"127.0.0.1", "44617", "9530", "deadline"} {
		if strings.Contains(got, leak) {
			t.Fatalf("наружу уехало %q: %q", leak, got)
		}
	}
	// Одно сообщение — один ❌, а не два подряд.
	if strings.Count(got, "❌") > 1 {
		t.Fatalf("двойной значок ошибки: %q", got)
	}
}

// Код обращения обязан совпадать у разных людей при одной причине —
// иначе по нему нечего сопоставлять.
func TestErrCode_SameCauseSameCode(t *testing.T) {
	a := errors.New(`нет связи с панелью: Get "http://10.0.0.5:8080/api/users/by-telegram-id/1000000002": context deadline exceeded`)
	b := errors.New(`нет связи с панелью: Get "http://10.0.0.5:9091/api/users/by-telegram-id/1000000003": context deadline exceeded`)
	if errCode(a) != errCode(b) {
		t.Fatalf("одна причина — разные коды: %s vs %s", errCode(a), errCode(b))
	}
	other := errors.New("хранилище недоступно")
	if errCode(a) == errCode(other) {
		t.Fatalf("разные причины дали один код")
	}
	// А вот код ответа — это и есть причина: 401 и 500 склеивать нельзя.
	if errCode(errors.New("панель вернула HTTP 401")) == errCode(errors.New("панель вернула HTTP 500")) {
		t.Fatalf("разные коды ответа дали один код обращения")
	}
}

// Флаг «бонус выплачен» один на обе выплаты, поэтому и повтор возможен
// только целиком. Иначе приглашённому платят на каждой покупке.
func TestReferral_NoRepeatInviteeBonus(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	ctx := context.Background()
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru", Referral: model.ReferralConfig{
		Enabled: true, Init: true, OnFirstPay: true,
		BonusKind: model.ReferralBonusDays, BonusValue: 7,
		InviteeKind: model.ReferralBonusBalance, InviteeValue: 30,
	}}
	const inviter, invitee int64 = 100500, 777
	_ = fs.UpsertUser(ctx, inviter)
	_ = fs.UpsertUser(ctx, invitee)
	_ = fs.SetReferredBy(ctx, invitee, inviter)

	// Панель не настроена — бонус днями пригласившему не проходит.
	for i := 0; i < 3; i++ {
		a.grantReferralBonus(ctx, invitee)
	}
	u, _ := fs.GetUser(ctx, invitee)
	if u == nil || u.Balance != 0 {
		t.Fatalf("приглашённому начислили без выплаты пригласившему: %+v", u)
	}
}

// Обратная сторона : в режиме «бонус по ссылке» второго захода не будет
// никогда — там откладывать нечего, приветственный бонус обязан дойти.
func TestReferral_LinkModePaysInviteeAnyway(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	ctx := context.Background()
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru", Referral: model.ReferralConfig{
		Enabled: true, Init: true, OnFirstPay: false,
		BonusKind: model.ReferralBonusDays, BonusValue: 7,
		InviteeKind: model.ReferralBonusBalance, InviteeValue: 30,
	}}
	const inviter, invitee int64 = 100500, 777
	_ = fs.UpsertUser(ctx, inviter)
	_ = fs.UpsertUser(ctx, invitee)
	_ = fs.SetReferredBy(ctx, invitee, inviter)

	a.payReferralBonus(ctx, invitee)
	u, _ := fs.GetUser(ctx, invitee)
	if u == nil || u.Balance != 3000 {
		t.Fatalf("приветственный бонус потерян: %+v", u)
	}
	if !u.RefBonusPaid {
		t.Fatalf("отметка не поставлена — повтора всё равно не будет: %+v", u)
	}
}

// Панель шлёт повтор, не дождавшись ответа, — обе доставки идут
// одновременно. Проверка и отметка по разные стороны отправки давали два
// сообщения.
func TestRemnawaveWebhook_ConcurrentRetriesDeliverOnce(t *testing.T) {
	a, fm, _ := torrentApp(t)
	fm.sendDelay = 50 * time.Millisecond
	body := `{"scope":"user","event":"user.expired","timestamp":"2026-09-06T10:00:00.000Z","data":{"telegramId":42}}`
	m := hmac.New(sha256.New, []byte(a.botCfg.Webhook.RemnawaveSecret))
	m.Write([]byte(body))
	sig := hex.EncodeToString(m.Sum(nil))

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = a.HandleRemnawaveWebhook(context.Background(), sig, []byte(body))
		}()
	}
	wg.Wait()
	if n := countContains(fm.texts, "Подписка истекла"); n != 1 {
		t.Fatalf("одновременные повторы дали %d сообщений: %v", n, fm.texts)
	}
}

// Отказ платёжного шлюза по таймауту — это шлюз, а не панель. Раньше
// проверка типа ошибки стояла первой и зачисляла в «панель» самый частый
// отказ платёжки.
func TestErrKind_GatewayTimeoutIsGateway(t *testing.T) {
	err := fmt.Errorf("шлюз ЮKassa: счёт на пополнение: %w", context.DeadlineExceeded)
	if got := errKind(err); got != "gateway" {
		t.Fatalf("таймаут шлюза классифицирован как %q", got)
	}
	perr := fmt.Errorf("нет связи с панелью: %w", context.DeadlineExceeded)
	if got := errKind(perr); got != "panel" {
		t.Fatalf("таймаут панели классифицирован как %q", got)
	}
}

// В мини-апп текст попадает как есть, без разметки Telegram.
func TestMiniApp_ErrorHasNoMarkup(t *testing.T) {
	a, _, _ := newTestApp(t)
	txt := stripHTMLTags(a.clientErr(context.Background(), 1, "мини-апп",
		errors.New("нет связи с панелью: dial tcp: i/o timeout")))
	if strings.Contains(txt, "<") || strings.Contains(txt, ">") {
		t.Fatalf("в мини-апп уехала разметка: %q", txt)
	}
}

// Отрицательный ответ у одного человека (например, ссылка отозвана) не
// должен на минуту выключать «Подключить» всем остальным.
func TestAppConfig_NegativeCacheIsPerSubscription(t *testing.T) {
	a, _, _ := newTestApp(t)
	// Конфиг отдаётся по сессионной куке, которую ставит страница подписки:
	// у отозванной ссылки куки не будет, у рабочей — будет.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "app-config") {
			if c, err := r.Cookie("sess"); err != nil || c.Value != "ok" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"platforms":{"ios":[{"id":"a","name":"App","installationStep":{"buttons":[{"buttonLink":"https://x","buttonText":{"en":"go"}}]}}]}}`))
			return
		}
		if strings.Contains(r.URL.Path, "/ok") {
			http.SetCookie(w, &http.Cookie{Name: "sess", Value: "ok", Path: "/"})
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if ce := a.fetchAppConfig(context.Background(), srv.URL, srv.URL+"/revoked"); ce != nil {
		t.Fatalf("по отозванной ссылке конфига быть не должно")
	}
	if ce := a.fetchAppConfig(context.Background(), srv.URL, srv.URL+"/ok"); ce == nil {
		t.Fatalf("чужая неудача выключила «Подключить» всем")
	}
}

// Двойной тап по «купить» в мини-аппе с паузой: первая покупка сдвинула конец
// срока, и ключ сделки у второй уже другой — без отдельной защиты она
// списывала деньги во второй раз.
func TestMiniCheckoutBalance_SecondTapAfterSuccess(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const uid int64 = 555
	_ = fs.UpsertUser(ctx, uid)
	_ = fs.AddBalance(ctx, uid, 99000*3)
	p := vipPlan(t, fs, model.PlanAvailAll)
	start := int64(99000 * 3)

	if r := a.MiniCheckout(ctx, uid, p.Code, 1, model.PayMethodBalance, "", false); !r.OK {
		t.Fatalf("первая покупка не прошла: %+v", r)
	}
	// Второе нажатие — уже после того, как выдача сдвинула срок подписки.
	if r := a.MiniCheckout(ctx, uid, p.Code, 1, model.PayMethodBalance, "", false); r.OK {
		t.Fatalf("второе нажатие прошло как отдельная покупка")
	}
	u, _ := fs.GetUser(ctx, uid)
	if u == nil || u.Balance != start-99000 {
		t.Fatalf("списано дважды: баланс %d, ожидался %d", u.Balance, start-99000)
	}
}

// Пригласившего нет на панели (раздаёт ссылки, сам
// ничего не покупал). Бонус днями ему начислять некуда и это не изменится —
// держать из-за него приветственный бонус приглашённого нельзя.
func TestReferral_InviterMissingInPanelStillPaysInvitee(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	ctx := context.Background()
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru", Referral: model.ReferralConfig{
		Enabled: true, Init: true, OnFirstPay: true,
		BonusKind: model.ReferralBonusDays, BonusValue: 7,
		InviteeKind: model.ReferralBonusBalance, InviteeValue: 30,
	}}
	// Панель отвечает «такого пользователя нет».
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[]}`))
	}))
	defer srv.Close()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	const inviter, invitee int64 = 100500, 777
	_ = fs.UpsertUser(ctx, inviter)
	_ = fs.UpsertUser(ctx, invitee)
	_ = fs.SetReferredBy(ctx, invitee, inviter)

	for i := 0; i < 3; i++ {
		a.grantReferralBonus(ctx, invitee)
	}
	u, _ := fs.GetUser(ctx, invitee)
	if u == nil || u.Balance != 3000 {
		t.Fatalf("приветственный бонус не дошёл ровно один раз: %+v", u)
	}
	if !u.RefBonusPaid {
		t.Fatalf("отметка не поставлена — поход в панель повторится на каждой покупке: %+v", u)
	}
}

// Авария панели за прокси не должна выдаваться за отказ платёжки —
// иначе человек идёт платить другим способом, хотя лежала панель.
func TestErrKind_PanelBehindProxyIsPanel(t *testing.T) {
	err := errors.New("панель вернула HTTP 502: <html><head><title>502 Bad Gateway</title></head>")
	if got := errKind(err); got != "panel" {
		t.Fatalf("авария панели классифицирована как %q", got)
	}
}

// Код обращения не должен зависеть от куска ответа панели — иначе
// десять человек с одним сбоем назовут десять разных кодов.
func TestErrCode_StableAcrossResponseNoise(t *testing.T) {
	a := errors.New(`панель вернула HTTP 500: {"timestamp":"2026-09-06T10:11:12.345Z","path":"/api/users/1000000002","trace":"af13ee"}`)
	b := errors.New(`панель вернула HTTP 500: {"timestamp":"2026-09-06T21:02:59.001Z","path":"/api/users/1000000003","trace":"zq90bc"}`)
	if errCode(a) != errCode(b) {
		t.Fatalf("один сбой — разные коды: %s vs %s", errCode(a), errCode(b))
	}
	c := errors.New(`панель вернула HTTP 401: {"timestamp":"2026-09-06T10:11:12.345Z","path":"/api/users/1000000002","trace":"af13ee"}`)
	if errCode(a) == errCode(c) {
		t.Fatalf("500 и 401 склеились в один код")
	}
}

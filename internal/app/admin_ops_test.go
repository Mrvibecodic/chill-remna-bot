package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/storage"
)

// Пин версии в стабильном канале ставят намеренно — сам compose объясняет, что
// «:v1» держит установку без прыжка на 2.0.0. Обновление не вправе его снимать.
func TestTagInChannel(t *testing.T) {
	cases := []struct {
		tag, ch string
		want    bool
	}{
		{"v1", "stable", true},
		{"v1.4.4", "stable", true},
		{"latest", "stable", true},
		{"dev", "stable", false},
		{"", "stable", false},
		{"vintage", "stable", false},
		{"dev", "dev", true},
		{"v1", "dev", false},
		{"latest", "dev", false},
	}
	for _, c := range cases {
		if got := tagInChannel(c.tag, c.ch); got != c.want {
			t.Fatalf("tagInChannel(%q,%q)=%v, ожидалось %v", c.tag, c.ch, got, c.want)
		}
	}
}

// Правки в админке, сделанные пока мастер открыт, обязаны пережить его
// финиш. Раньше мастер писал поверх снимок, снятый на старте.
func TestReconfigure_KeepsParallelEdits(t *testing.T) {
	srv := panelStub(7)
	defer srv.Close()
	a, fm, fs := newTestApp(t)
	ctx := context.Background()
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.mu.Lock()
	if a.wiz == nil {
		a.wiz = map[int64]*wizard{}
	}
	a.mu.Unlock()

	// Мастер открыт: снимок настроек сделан.
	a.handleMessage(ctx, msgText(100, "/setup"))
	a.mu.Lock()
	w := a.wiz[100]
	a.mu.Unlock()
	if w == nil || !w.reconfig {
		t.Fatal("переустановка не запущена")
	}

	// Пока он открыт, админ правит соседние настройки.
	a.mu.Lock()
	a.botCfg.YooKassa.ShopID = "shop-2"
	a.botCfg.Trial.Enabled = true
	a.mu.Unlock()

	// Мастер доходит до конца.
	a.handleCallback(ctx, cb(100, "db:sqlite"))
	a.handleCallback(ctx, cb(100, "loc:remote"))
	a.handleCallback(ctx, cb(100, "inst:docs"))
	a.handleMessage(ctx, msgText(100, srv.URL))
	a.handleMessage(ctx, msgText(100, "api-token-xyz"))
	a.handleCallback(ctx, cb(100, "apiprot:no"))

	if fs.cfg == nil {
		t.Fatalf("конфиг не сохранён; лог:\n%s", fm.joined())
	}
	if fs.cfg.YooKassa.ShopID != "shop-2" {
		t.Fatalf("правка админки откатилась мастером: %q", fs.cfg.YooKassa.ShopID)
	}
	if !fs.cfg.Trial.Enabled {
		t.Fatal("включённый параллельно триал откатился мастером")
	}
	if fs.cfg.Panel.APIToken != "api-token-xyz" {
		t.Fatalf("поля самого мастера не применились: %q", fs.cfg.Panel.APIToken)
	}
}

// Брошенный мастер съедал весь ввод в админке до перезапуска процесса.
func TestReconfigure_AbandonedWizardDoesNotEatAdminInput(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	a.store = fs
	a.mu.Lock()
	if a.wiz == nil {
		a.wiz = map[int64]*wizard{}
	}
	a.botCfg.Installed = true
	a.mu.Unlock()
	a.handleMessage(ctx, msgText(planAdmin, "/setup"))
	a.mu.Lock()
	live := a.wiz[planAdmin] != nil
	a.mu.Unlock()
	if !live {
		t.Fatal("мастер не запущен")
	}

	// Админ ушёл в другой раздел и нажал «изменить значение».
	a.getUI(planAdmin).adminInput = "currency"
	a.handleMessage(ctx, msgText(planAdmin, "USD"))

	a.mu.Lock()
	still := a.wiz[planAdmin] != nil
	a.mu.Unlock()
	if still {
		t.Fatal("брошенный мастер не погашен — он и дальше будет съедать ввод")
	}
	if got := a.pricing().Currency; got != "USD" {
		t.Fatalf("ввод съеден мастером: валюта %q", got)
	}
}

// На шаге с кнопками текст больше не пропадает молча.
func TestWizard_TextOnButtonStepAnswers(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	a.store = fs
	a.mu.Lock()
	if a.wiz == nil {
		a.wiz = map[int64]*wizard{}
	}
	a.botCfg.Installed = true
	a.mu.Unlock()
	a.handleMessage(ctx, msgText(planAdmin, "/setup"))
	fm.texts = nil

	a.handleMessage(ctx, msgText(planAdmin, "что-то не то"))
	if !strings.Contains(fm.joined(), "жду нажатия кнопки") {
		t.Fatalf("на шаге с кнопками текст пропал молча:\n%s", fm.joined())
	}
	if !hasCB(fm.allCallbackData(), "rcfg:cancel") {
		t.Fatalf("нет кнопки отмены: %v", fm.allCallbackData())
	}
}

// Выбор базы в мастере применялся немедленно. «Передумал и нажал
// Назад» оставлял бота работать с пустым хранилищем, и выбор переживал
// перезапуск.
func TestReconfigure_DBChoiceNotAppliedUntilFinish(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	a.store = fs
	a.cfg.DataDir = t.TempDir()
	a.newStore = func(_, _ string) (storage.Storage, error) { return &fakeStore{}, nil }
	a.mu.Lock()
	if a.wiz == nil {
		a.wiz = map[int64]*wizard{}
	}
	a.botCfg.Installed = true
	a.mu.Unlock()

	a.handleMessage(ctx, msgText(planAdmin, "/setup"))
	a.handleCallback(ctx, cb(planAdmin, "db:sqlite"))

	a.mu.Lock()
	live := a.store
	w := a.wiz[planAdmin]
	a.mu.Unlock()
	if live != storage.Storage(fs) {
		t.Fatal("боевое хранилище подменено до подтверждения")
	}
	if w == nil || w.pendingDBKind != model.DBSQLite {
		t.Fatalf("выбор не запомнен: %+v", w)
	}
	if _, err := os.Stat(filepath.Join(a.cfg.DataDir, "bootstrap.json")); err == nil {
		t.Fatal("выбор записан на диск до подтверждения — переживёт перезапуск")
	}

	// Отмена мастера ничего не меняет.
	a.handleCallback(ctx, cb(planAdmin, "rcfg:cancel"))
	a.mu.Lock()
	after := a.store
	a.mu.Unlock()
	if after != storage.Storage(fs) {
		t.Fatal("после отмены бот остался на другой базе")
	}
}

// В поле «валюта» проходило что угодно, включая строку с переносами и
// разметкой, — она уезжала в подписи кнопок, и Telegram отвергал сообщение.
func TestCurrency_InputAndOutput(t *testing.T) {
	bad := []string{
		"<b>USD",
		strings.Repeat("долларов ", 40),
		"US\nD",
		"US\u200bD",
	}
	for _, v := range bad {
		if validCurrencyInput(v) {
			t.Fatalf("мусор принят как валюта: %q", v)
		}
	}
	for _, v := range []string{"", "USD", "kzt", "RUB", "₽", "руб"} {
		if !validCurrencyInput(v) {
			t.Fatalf("нормальная валюта отвергнута: %q", v)
		}
	}
	// Второй рубеж: уже сохранённый мусор не должен печататься.
	for _, v := range bad {
		if got := curSymbol(v); got != curRUB {
			t.Fatalf("мусор напечатан как валюта: %q → %q", v, got)
		}
	}
	// А осмысленные символы подменять рублями нельзя — это было бы враньём.
	for _, v := range []string{"$", "€", "USD"} {
		if got := curSymbol(v); got == curRUB {
			t.Fatalf("валюта %q подменена рублями", v)
		}
	}
}

// В поле порта проходил любой текст; бот показывал 8080, переписывал
// compose на 8080, а веб-сервер после перезапуска не поднимался вовсе.
func TestListenAddr_Validation(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"18080", ":18080", true},
		{":18080", ":18080", true},
		{"0.0.0.0:18080", "0.0.0.0:18080", true},
		{"127.0.0.1:8080", "127.0.0.1:8080", true},
		{"порт", "", false},
		{":порт", "", false},
		{"0", "", false},
		{"70000", "", false},
		{"", "", false},
		{"8080 ", ":8080", true},
		// IPv6: скобки обязаны сохраниться, иначе net.Listen отвечает
		// «too many colons in address» и веб-сервер не поднимается.
		{"[::]:8080", "[::]:8080", true},
		{"[::1]:18080", "[::1]:18080", true},
		{"localhost:8080", "localhost:8080", true},
	}
	for _, c := range cases {
		got, ok := normalizeListenAddr(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("normalizeListenAddr(%q) = %q,%v; ожидалось %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// Сохранённый мусор не должен попадать в http.Server —
// иначе веб-сервер не поднимется и вебхуки всех платёжек умрут молча.
func TestWebhookServer_IgnoresBrokenAddr(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.Webhook.ListenAddr = ":порт"
	if addr, _, _ := a.WebhookServer(); addr != ":8080" {
		t.Fatalf("в http.Server уехал мусор: %q", addr)
	}
	if got := a.webhookListenPort(); got != "8080" {
		t.Fatalf("экран показывает не то, что применяется: %q", got)
	}
	a.botCfg.Webhook.ListenAddr = "0.0.0.0:18080"
	if addr, _, _ := a.WebhookServer(); addr != "0.0.0.0:18080" {
		t.Fatalf("нормальный адрес не сохранён: %q", addr)
	}
}

// Адрес возврата уезжает в API платёжки, ссылка Tribute — в кнопку.
func TestGatewayAndButtonURLs(t *testing.T) {
	bad := []string{
		"вернуться", "t.me", "ftp://x.y", "https://", "tg://resolve?domain=x",
		"https://exa\u200bmple.com/x", // невидимый символ внутри адреса
		"https://" + strings.Repeat("a", 300) + ".com",
	}
	for _, v := range bad {
		if validGatewayReturnURL(v) {
			t.Fatalf("в шлюз уедет %q", v)
		}
	}
	for _, v := range []string{"https://t.me/mybot", "http://example.com/x"} {
		if !validGatewayReturnURL(v) {
			t.Fatalf("нормальный адрес отвергнут: %q", v)
		}
	}
	// Битый адрес не отправляем в шлюз даже если он уже сохранён.
	if got := gatewayReturnURL("напишите в поддержку"); got != "https://t.me" {
		t.Fatalf("мусор уехал в шлюз: %q", got)
	}
	// Ссылка кнопки: tg:// допустим, мусор — нет.
	if !validButtonURL("tg://resolve?domain=tribute") {
		t.Fatal("tg:// должен быть годной ссылкой кнопки")
	}
	if validButtonURL("напишите в поддержку") {
		t.Fatal("текст принят как ссылка кнопки")
	}
}

// Рассылку нельзя было остановить (только погасив контейнер), а
// заблокировавшие бота тратили по два обращения на каждую рассылку вечно.
func TestBroadcast_StopAndUnreachable(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	a.store = fs
	fm.forbidden = map[int64]bool{202: true}
	for _, id := range []int64{201, 202, 203} {
		_ = fs.UpsertUser(ctx, id)
	}

	a.getUI(planAdmin).broadcastText = "привет"
	a.onBroadcast(ctx, planAdmin, "send")
	waitFor(t, func() bool { return !a.bcastRunning.Load() })

	if fs.unreachable[202] == "" {
		t.Fatal("заблокировавший бота не помечен — следующая рассылка снова на него потратится")
	}
	if u, _ := fs.GetUser(ctx, 202); u != nil && u.Blocked {
		t.Fatal("человека забанили за то, что он заблокировал бота")
	}
	if !strings.Contains(fm.joined(), "Заблокировали бота: 1") {
		t.Fatalf("итог не разделяет отказ и блокировку:\n%s", fm.last())
	}

	// Кнопка «остановить» есть и работает.
	if !hasCB(fm.allCallbackData(), "bc:stop") {
		t.Fatalf("нет кнопки остановки: %v", fm.allCallbackData())
	}
	fm.texts = nil
	a.onBroadcast(ctx, planAdmin, "stop")
	if !strings.Contains(fm.joined(), "Сейчас рассылки нет") {
		t.Fatalf("остановка несуществующей рассылки:\n%s", fm.joined())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("не дождались завершения")
}

// Остановка бота обрывала недоделанное — деньги списаны, подписки нет.
// Drain обязан дать «денежной» работе доиграть, но не дольше бюджета:
// docker убивает контейнер через десять секунд после сигнала.
func TestDrain_WaitsForMoneyWorkThenGivesUp(t *testing.T) {
	a, _, _ := newTestApp(t)
	done := make(chan struct{})
	a.trackMoney("тест", func(ctx context.Context) {
		time.Sleep(30 * time.Millisecond)
		close(done)
	})
	if !a.Drain(2 * time.Second) {
		t.Fatal("Drain не дождался короткой задачи")
	}
	select {
	case <-done:
	default:
		t.Fatal("задача не доиграла")
	}

	// Зависшая задача бюджет не растягивает, и контекст ей отменяют.
	b, _, _ := newTestApp(t)
	cancelled := make(chan struct{})
	b.trackMoney("зависшая", func(ctx context.Context) {
		<-ctx.Done()
		close(cancelled)
	})
	if b.Drain(50 * time.Millisecond) {
		t.Fatal("Drain отчитался об успехе, хотя задача не завершилась")
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("контекст зависшей задачи не отменён — процесс не остановится")
	}
}

// Отказ веб-сервера гасит бота только если веб-часть кому-то нужна.
func TestWebRequired(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true}
	if a.WebRequired() {
		t.Fatal("всё выключено, а веб считается обязательным — бот уйдёт в перезапуск на ровном месте")
	}
	a.botCfg.Webhook.Enabled = true
	if !a.WebRequired() {
		t.Fatal("вебхуки включены, а отказ веба будет проигнорирован — платежи не доедут")
	}
	a.botCfg.Webhook.Enabled = false
	a.botCfg.MiniApp.Enabled = true
	a.botCfg.MiniApp.Init = true
	if !a.WebRequired() {
		t.Fatal("мини-апп включён, а отказ веба будет проигнорирован")
	}
}

// Журнал платежей больше не дублируется в лог процесса.
func TestPayLog_NotInProcessLog(t *testing.T) {
	var buf bytes.Buffer
	a, _, fs := newTestApp(t)
	a.store = fs
	a.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	a.payLog(context.Background(), "yookassa", "pay-1", 1000000002, "verified", "amount=%s", "1500.00 RUB")

	out := buf.String()
	for _, leak := range []string{"1000000002", "pay-1", "1500.00"} {
		if strings.Contains(out, leak) {
			t.Fatalf("в лог процесса уехало %q: %s", leak, out)
		}
	}
	// В журнале платежей запись при этом есть — админу она нужна.
	if rows, _ := fs.PayLogs(context.Background(), "pay-1", 0, 10); len(rows) == 0 {
		t.Fatal("запись не попала в журнал платежей")
	}
}

// Проверка «бот жив» не смотрела на базу — отвалившееся хранилище
// давало бодрое «всё хорошо», и оркестратор ничего не перезапускал.
func TestHealthy_ChecksDatabase(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	if err := a.Healthy(context.Background()); err != nil {
		t.Fatalf("здоровый бот признан больным: %v", err)
	}
	fs.pingErr = errors.New("connection refused")
	if err := a.Healthy(context.Background()); err == nil {
		t.Fatal("база отвалилась, а проверка отвечает «всё хорошо»")
	}
}

// «сегодня» в сводке считалось по всемирным суткам, а время человеку
// печаталось по Москве — выручка обнулялась в три ночи.
func TestAnalytics_TodayIsMoscowDay(t *testing.T) {
	// 01:30 по Москве = 22:30 предыдущего дня по UTC.
	now := time.Date(2026, 9, 6, 22, 30, 0, 0, time.UTC)
	dayStart := dayStartFor(now)

	// Платёж в 00:30 МСК того же дня обязан попасть в «сегодня».
	if paid := time.Date(2026, 9, 6, 21, 30, 0, 0, time.UTC); !paid.After(dayStart) {
		t.Fatal("платёж после московской полуночи не попал в «сегодня»")
	}
	// А платёж до неё — не обязан.
	if before := time.Date(2026, 9, 6, 20, 30, 0, 0, time.UTC); before.After(dayStart) {
		t.Fatal("платёж до московской полуночи попал в «сегодня»")
	}
	// Ровно та же граница, что печатается человеку.
	if got := dayStart.In(displayTZ).Format("15:04"); got != "00:00" {
		t.Fatalf("сутки начинаются не в полночь по поясу показа: %s", got)
	}
}

// Панель прилегла в момент «Одобрить» — кнопки уже удалены нажатием.
// Заявка не должна оставаться без единого способа её одобрить.
func TestP2P_StuckRequestStaysApprovable(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	a.store = fs
	req := &model.P2PRequest{ID: 77, TelegramID: 555, Months: 1, Price: "150", Status: model.P2PSubmitted, Screenshot: "web"}
	if err := fs.CreateP2PRequest(ctx, req); err != nil {
		t.Fatal(err)
	}

	// Панель не настроена — выдача не проходит, карточку обязаны вернуть.
	fm.texts = nil
	a.handleCallback(ctx, cb(planAdmin, "adm:pok:77"))
	if !hasCB(fm.allCallbackData(), "adm:pok:77") {
		t.Fatalf("после неудачи одобрить заявку негде: %v", fm.allCallbackData())
	}
	if r, _ := fs.GetP2PRequest(ctx, 77); r == nil || r.Status != model.P2PSubmitted {
		t.Fatalf("статус заявки не откатился: %+v", r)
	}

	// И её же видно на экране висящих заявок.
	fm.texts = nil
	a.handleCallback(ctx, cb(planAdmin, "adm:pending"))
	if !strings.Contains(fm.joined(), "Заявки на рассмотрении") {
		t.Fatalf("экран висящих заявок не открылся:\n%s", fm.joined())
	}
	if !hasCB(fm.allCallbackData(), "adm:pok:77") {
		t.Fatalf("в списке нет кнопки одобрения: %v", fm.allCallbackData())
	}
}

// Удалили человека, а его незакрытый счёт остался. Сверка добивала его и
// заводила человека заново — с деньгами, но без принятых документов и допуска.
func TestReconcile_DeletedUserIsNotResurrected(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	a.store = fs
	const gone int64 = 909090

	pi := &model.PendingInvoice{ID: 5, Method: "yookassa", ExtID: "pay-gone", TelegramID: gone, Purpose: "topup", Kopecks: 50000}
	if err := fs.AddPendingInvoice(ctx, pi); err != nil {
		t.Fatal(err)
	}
	if ok := a.reconcileFinalize(ctx, fs, pi, "500.00 RUB"); ok {
		t.Fatal("подписка выдана удалённому пользователю")
	}
	if u, _ := fs.GetUser(ctx, gone); u != nil {
		t.Fatalf("удалённый пользователь заведён заново: %+v", u)
	}
	if !strings.Contains(fm.joined(), "удалённого пользователя") {
		t.Fatalf("админа не позвали разобраться:\n%s", fm.joined())
	}
	// Счёт закрыт — сверка не будет крутить его сутки.
	if p := fs.pending[5]; p == nil || !p.Resolved {
		t.Fatal("счёт остался незакрытым — сверка будет крутить его сутки")
	}
}

// Незавершённое ожидание ввода из другого раздела не должно гасить мастер
// переустановки: админ мог нажать «изменить цену», передумать и уйти в
// переустановку — первый же текст в мастере погасил бы его на середине.
func TestReconfigure_StaleAdminInputDoesNotKillWizard(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	a.store = fs
	a.mu.Lock()
	if a.wiz == nil {
		a.wiz = map[int64]*wizard{}
	}
	a.botCfg.Installed = true
	a.mu.Unlock()

	// Админ нажал «изменить значение» в другом разделе и не ответил.
	a.getUI(planAdmin).adminInput = "currency"

	a.handleMessage(ctx, msgText(planAdmin, "/setup"))
	if got := a.getUI(planAdmin).adminInput; got != "" {
		t.Fatalf("брошенное ожидание не снято: %q", got)
	}

	// Теперь текст в мастере остаётся в мастере.
	a.handleMessage(ctx, msgText(planAdmin, "что-то"))
	a.mu.Lock()
	alive := a.wiz[planAdmin] != nil
	a.mu.Unlock()
	if !alive {
		t.Fatal("мастер погашен текстом, адресованным ему самому")
	}
}

// Обработчик может дожить до остановки и запустить денежную задачу ровно
// тогда, когда её уже ждут. Увеличение счётчика рядом с ожиданием ломает
// WaitGroup вплоть до паники.
func TestDrain_TaskStartedWhileDraining(t *testing.T) {
	a, _, _ := newTestApp(t)
	release := make(chan struct{})
	a.trackMoney("держит", func(ctx context.Context) { <-release })

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.trackMoney("поздняя", func(ctx context.Context) {})
		}()
	}
	go func() { time.Sleep(20 * time.Millisecond); close(release) }()
	if a.Drain(2*time.Second) == false && len(release) == 0 {
		// Не успели — это допустимо; паники быть не должно, её проверяет сам факт возврата.
		t.Log("дренаж не уложился в бюджет")
	}
	wg.Wait()
}

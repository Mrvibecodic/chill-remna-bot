package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"remnabot/internal/backupfile"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
)

func lastText(fm *fakeMsg) string {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.texts) == 0 {
		return ""
	}
	return fm.texts[len(fm.texts)-1]
}

func TestBackupPasswordBounds(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	for _, pass := range []string{"коротко", strings.Repeat("я", backupPassMax+1)} {
		a.handleCallback(ctx, cb(100, "bk:pass"))
		a.handleMessage(ctx, msgText(100, pass))
		if a.backupCfg().Password != "" || a.getUI(100).adminInput != "bk_pass" ||
			!strings.Contains(lastText(fm), "Нужно от 8 до 128 символов") {
			t.Fatalf("пароль %d симв. принят", len([]rune(pass)))
		}
	}
	// Ровно на границах — принимается; пробелы по краям отрезаются.
	a.handleMessage(ctx, msgText(100, "  "+strings.Repeat("п", backupPassMin)+"  "))
	if a.backupCfg().Password != strings.Repeat("п", backupPassMin) {
		t.Fatalf("пароль на нижней границе: %q", a.backupCfg().Password)
	}
}

func TestBackupIntervalInput(t *testing.T) {
	a, _, _ := bkTestApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "bk:h:6"))
	for _, data := range []string{"bk:h:5", "bk:h:-1", "bk:h:999"} {
		a.handleCallback(ctx, cb(100, data))
		if a.backupCfg().Hours != 0 {
			t.Fatalf("%s: недопустимый интервал не сброшен: %d", data, a.backupCfg().Hours)
		}
		a.handleCallback(ctx, cb(100, "bk:h:6"))
	}
	a.handleCallback(ctx, cb(100, "bk:h:x"))
	if a.backupCfg().Hours != 6 {
		t.Fatal("нечисловой интервал изменил настройку")
	}
	for _, h := range model.BackupIntervals {
		a.handleCallback(ctx, cb(100, "bk:h:"+itoa(h)))
		if a.backupCfg().Hours != h {
			t.Fatalf("интервал %d не принят", h)
		}
	}
}

// Пока идёт бэкап, второй бэкап и восстановление не начинаются, а файл на
// восстановление не теряется.
func TestBackupBusy(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	bkSetPassword(t, a, "пароль-бэкапа")
	a.handleCallback(ctx, cb(100, "bk:now"))
	_, file, _ := lastDoc(t, fm)
	restoreFile(t, a, fm, file)

	a.bkMu.Lock()
	n := docCount(fm)
	a.handleCallback(ctx, cb(100, "bk:now"))
	a.handleCallback(ctx, cb(100, "bk:apply"))
	a.bkMu.Unlock()
	if docCount(fm) != n || a.paused() || !strings.Contains(fm.joined(), "Уже идёт бэкап или восстановление") {
		t.Fatal("при занятом бэкапе что-то началось")
	}
	if p := a.bkPendPeek(100); p == nil || p.info == nil {
		t.Fatal("файл на восстановление потерян после отказа «занято»")
	}
	// Повторное восстановление, пока первое идёт, — отказ, фаза первого не
	// трогается, файл остаётся.
	a.restoring.Store(true)
	a.restorePhase.Store(phaseReplace)
	a.onBackup(ctx, 100, "apply")
	if a.restorePhase.Load() != phaseReplace || a.bkPendPeek(100) == nil || !a.bkMu.TryLock() {
		t.Fatal("второе восстановление задело первое")
	}
	a.bkMu.Unlock()
}

func TestBackupRestoreFileProblems(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	bkSetPassword(t, a, "пароль-бэкапа")

	// Больше 20 МБ — Telegram такой не отдаст.
	a.handleCallback(ctx, cb(100, "bk:restore"))
	a.handleDocument(ctx, docMsg(100, "big", maxDumpBytes+1))
	if !strings.Contains(lastText(fm), "Файл больше 20 МБ") {
		t.Fatal("большой файл не отклонён")
	}
	// Не скачался.
	a.handleCallback(ctx, cb(100, "bk:restore"))
	a.handleDocument(ctx, docMsg(100, "нет-такого", 10))
	if !strings.Contains(lastText(fm), "Файл не скачался") {
		t.Fatal("ошибка скачивания не показана")
	}

	seal := func(plain []byte) []byte {
		f, err := backupfile.Seal(plain, "пароль-бэкапа")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	gzipped := func(s string) []byte {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		_, _ = zw.Write([]byte(s))
		_ = zw.Close()
		return b.Bytes()
	}
	// Заперт верно, но внутри не gzip / не бэкап.
	for name, plain := range map[string][]byte{"не gzip": []byte("просто байты"), "не бэкап": gzipped("{}\n")} {
		restoreFile(t, a, fm, seal(plain))
		if !strings.Contains(lastText(fm), "Файл повреждён или обрезан") || a.bkPendPeek(100) != nil {
			t.Fatalf("%s: файл не отклонён", name)
		}
	}
	// Бэкап более новой версии бота.
	var raw bytes.Buffer
	if _, err := st.WriteBackup(ctx, &raw, "test"); err != nil {
		t.Fatal(err)
	}
	newer := strings.Replace(raw.String(), `"schema":`, `"schema":9`, 1)
	restoreFile(t, a, fm, seal(gzipped(newer)))
	if !strings.Contains(lastText(fm), "более новой версией бота") || hasButton(fm, "bk:apply") {
		t.Fatal("файл новее бота не отклонён")
	}
}

// failingStore — хранилище, где восстановление срывается.
type failingStore struct {
	storage.Storage
	err error
}

func (s *failingStore) RestoreBackup(context.Context, func() (io.Reader, error), *model.RestoreGuard) error {
	return s.err
}

func TestBackupRestoreFailureKeepsWorking(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New("диск переполнен"), "Не получилось, база осталась прежней: диск переполнен"},
		{storage.ErrBackupFormat, "Файл повреждён или обрезан.\nБаза осталась прежней."},
		{storage.ErrBackupNewer, "более новой версией бота. Сначала обновите бота.\nБаза осталась прежней."},
	} {
		a, fm, st := bkTestApp(t)
		ctx := context.Background()
		bkSetPassword(t, a, "пароль-бэкапа")
		a.handleCallback(ctx, cb(100, "bk:now"))
		_, file, _ := lastDoc(t, fm)
		restoreFile(t, a, fm, file)
		a.store = &failingStore{Storage: st, err: tc.err}
		a.handleCallback(ctx, cb(100, "bk:apply"))
		if !strings.Contains(fm.joined(), tc.want) {
			t.Fatalf("%v: текст сбоя: %q", tc.err, lastText(fm))
		}
		if a.paused() || a.RestartRequested() {
			t.Fatalf("%v: после сбоя бот остался в техработах", tc.err)
		}
		if err := a.saveConfigOnly(ctx); err != nil {
			t.Fatalf("%v: конфиг не сохраняется после сбоя: %v", tc.err, err)
		}
	}
}

// Начатая работа не закончилась — базу не трогаем, техработы снимаем.
func TestBackupRestoreNotQuiet(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	a.quietWaitD = 200 * time.Millisecond
	bkSetPassword(t, a, "пароль-бэкапа")
	a.handleCallback(ctx, cb(100, "bk:now"))
	_, file, _ := lastDoc(t, fm)
	if err := st.UpsertUser(ctx, 1000000002); err != nil {
		t.Fatal(err)
	}
	restoreFile(t, a, fm, file)
	if !a.enterWork() {
		t.Fatal("работа не началась")
	}
	n := docCount(fm)
	a.handleCallback(ctx, cb(100, "bk:apply"))
	a.leaveWork()
	if !strings.Contains(fm.joined(), "Начатые операции не закончились") || docCount(fm) != n {
		t.Fatal("восстановление пошло, не дождавшись начатой работы")
	}
	if !hasUser(t, st, 1000000002) || a.paused() {
		t.Fatal("база тронута или техработы не сняты")
	}
}

// Большая база: файл больше 20 МБ уходит с предупреждением, что через бота
// его не восстановить.
func TestBackupBigFileCaption(t *testing.T) {
	if testing.Short() {
		t.Skip("долгий")
	}
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		junk := make([]byte, 6<<20)
		_, _ = rand.Read(junk)
		if err := st.AddPayLog(ctx, &model.PayLogEntry{ExtID: "big", Stage: "x", Detail: base64.StdEncoding.EncodeToString(junk)}); err != nil {
			t.Fatal(err)
		}
	}
	bkSetPassword(t, a, "пароль-бэкапа")
	a.handleCallback(ctx, cb(100, "bk:now"))
	_, file, _ := lastDoc(t, fm)
	if len(file) <= maxDumpBytes || !strings.Contains(fm.joined(), "Больше 20 МБ") {
		t.Fatalf("файл %d МБ без предупреждения", len(file)>>20)
	}
}

func TestLimitWriterAndRetry(t *testing.T) {
	var b bytes.Buffer
	lw := &limitWriter{w: &b, left: 5}
	if n, err := lw.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := lw.Write([]byte("abc")); !errors.Is(err, errBackupTooBig) || b.String() != "abc" {
		t.Fatalf("лимит не сработал: %v %q", err, b.String())
	}
	tooBig := &textErr{msg: "больше 50 МБ", err: errBackupTooBig}
	if got := backupRetryAfter(tooBig, 24); got != 24*time.Hour {
		t.Fatalf("большой файл: повтор через %s", got)
	}
	if got := backupRetryAfter(tooBig, 1); got != time.Hour {
		t.Fatalf("большой файл, интервал 1 ч: %s", got)
	}
	if got := backupRetryAfter(errors.New("сеть"), 24); got != backupRetry {
		t.Fatalf("сбой сети: повтор через %s", got)
	}
}

// Текст админа считается паролем от файла, только пока файл его ждёт.
func (a *App) wantsPass(chatID int64) bool {
	a.bkPendMu.Lock()
	defer a.bkPendMu.Unlock()
	p := a.bkPend[chatID]
	return p != nil && p.wantPass
}

func TestBackupRestorePasswordStates(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	// Файла нет — обычный текст админа паролем не считается.
	a.handleMessage(ctx, msgText(100, "пароль"))
	if strings.Contains(fm.joined(), "Пароль не подходит") {
		t.Fatal("текст без файла принят за пароль")
	}
	bkSetPassword(t, a, "пароль-файла")
	a.handleCallback(ctx, cb(100, "bk:now"))
	_, file, _ := lastDoc(t, fm)
	bkSetPassword(t, a, "другой-пароль")
	restoreFile(t, a, fm, file)
	if !a.wantsPass(100) {
		t.Fatal("файл не ждёт пароль")
	}
	// Другой ввод важнее пароля от файла.
	a.handleCallback(ctx, cb(100, "bk:pass"))
	a.handleMessage(ctx, msgText(100, "новый-пароль-1"))
	if a.backupCfg().Password != "новый-пароль-1" {
		t.Fatal("ввод пароля бэкапа съеден как пароль от файла")
	}
	// Пока файл проверяется, новый пароль не запускает вторую проверку.
	a.bkPendMu.Lock()
	a.bkPend[100].checking = true
	a.bkPendMu.Unlock()
	a.handleMessage(ctx, msgText(100, "пароль-файла"))
	if hasButton(fm, "bk:apply") {
		t.Fatal("вторая проверка поверх идущей")
	}
	a.bkPendMu.Lock()
	a.bkPend[100].checking = false
	a.bkPendMu.Unlock()
	a.handleMessage(ctx, msgText(100, "пароль-файла"))
	if !hasButton(fm, "bk:apply") || a.wantsPass(100) {
		t.Fatal("верный пароль не открыл файл")
	}
	// Уход в главное меню снимает файл.
	a.handleMessage(ctx, msgText(100, "/start"))
	if a.bkPendPeek(100) != nil {
		t.Fatal("файл пережил уход в меню")
	}
	// Протухший файл пароль уже не ждёт.
	restoreFile(t, a, fm, file)
	a.bkPendMu.Lock()
	a.bkPend[100].at = time.Now().Add(-bkPendTTL - time.Minute)
	a.bkPendMu.Unlock()
	n := len(fm.texts)
	a.handleMessage(ctx, msgText(100, "пароль-файла"))
	if hasButton(fm, "bk:apply") && len(fm.texts) > n && strings.Contains(lastText(fm), "заменена целиком") {
		t.Fatal("протухший файл открылся")
	}
}

// «Отмена» во время скачивания: скачанное никуда не ложится.
func TestBackupCancelDuringDownload(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	bkSetPassword(t, a, "пароль-бэкапа")
	a.handleCallback(ctx, cb(100, "bk:now"))
	_, file, _ := lastDoc(t, fm)
	a.handleCallback(ctx, cb(100, "bk:restore"))
	a.runInline = false
	release := make(chan struct{})
	fm.mu.Lock()
	fm.downloads = map[string][]byte{"slow": file}
	fm.mu.Unlock()
	a.msg = &slowDownload{fakeMsg: fm, release: release}
	a.handleDocument(ctx, docMsg(100, "slow", int64(len(file))))
	a.handleCallback(ctx, cb(100, "bk:cancel"))
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !fm.downloaded() {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if a.bkPendPeek(100) != nil || hasButton(fm, "bk:apply") {
		t.Fatal("после «Отмены» скачанный файл всё равно открылся")
	}
}

type slowDownload struct {
	*fakeMsg
	release chan struct{}
}

func (s *slowDownload) Download(ctx context.Context, id string) ([]byte, error) {
	<-s.release
	defer s.markDownloaded()
	return s.fakeMsg.Download(ctx, id)
}

// «Отмена», нажатая пока файл проверялся, не даёт появиться подтверждению.
func TestBackupCancelDuringCheck(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	var raw bytes.Buffer
	if _, err := st.WriteBackup(ctx, &raw, "test"); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	file, _ := backupfile.Seal(gz.Bytes(), "пароль-файла")
	p := &bkPending{file: file}
	a.bkPendSet(100, p)
	// Админ нажал «Отмена» и прислал новый файл, пока проверялся старый.
	a.bkPendTake(100)
	other := &bkPending{file: file}
	a.bkPendSet(100, other)
	if a.bkPendReplace(100, p, &bkPending{}) {
		t.Fatal("старая проверка подменила новый файл")
	}
	a.bkPendTake(100)
	a.openBackupFile(ctx, 100, other, "пароль-файла", nil)
	if hasButton(fm, "bk:apply") || a.bkPendPeek(100) != nil {
		t.Fatal("подтверждение после отмены")
	}
}

func TestShowRestorePreviewLabels(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	info := &storage.BackupInfo{CreatedAt: "2026-11-03T11:00:00Z", App: "<b>x</b>", DB: model.DBPostgres,
		Rows: map[string]int64{"users": 7, "payments": 3, "plans": 2, "promo_codes": 1}}
	a.showRestorePreview(ctx, 100, &bkPending{info: info})
	txt := lastText(fm)
	for _, want := range []string{"03.11.2026 14:00", "PostgreSQL", "&lt;b&gt;x&lt;/b&gt;", "Пользователей: 7 · оплат: 3 · тарифов: 2 · промокодов: 1", "пополнения и траты баланса"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("в подтверждении нет %q: %q", want, txt)
		}
	}
	info.DB = "<i>"
	a.showRestorePreview(ctx, 100, &bkPending{info: info})
	if !strings.Contains(lastText(fm), "&lt;i&gt;") {
		t.Fatal("тип базы из файла не экранирован")
	}
}

func TestStarsDuringRestorePhases(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	a.restoring.Store(true)
	msg := &models.Message{From: &models.User{ID: 1000000001}, Chat: models.Chat{ID: 1000000001, Type: models.ChatTypePrivate},
		SuccessfulPayment: &models.SuccessfulPayment{TotalAmount: 50, TelegramPaymentChargeID: "ch-wait", InvoicePayload: "stars:1"}}
	a.restorePhase.Store(phaseWait)
	a.handle(context.Background(), nil, &models.Update{Message: msg})
	if !a.Drain(5 * time.Second) {
		t.Fatal("оплата в фазе ожидания не доиграла")
	}
	if a.moneyActive.Load() != 0 || strings.Contains(fm.joined(), "Во время замены базы") {
		t.Fatal("оплата в фазе ожидания не проведена как обычно")
	}
	// Возврат во время техработ: молча, его догонит сверка.
	n := len(fm.texts)
	a.handle(context.Background(), nil, &models.Update{Message: &models.Message{Chat: models.Chat{ID: 1000000001, Type: models.ChatTypePrivate},
		RefundedPayment: &models.RefundedPayment{TelegramPaymentChargeID: "r"}}})
	if len(fm.texts) != n {
		t.Fatal("на возврат звёзд ответили «техработами»")
	}
	// В группе на техработы не отвечаем.
	a.handle(context.Background(), nil, &models.Update{Message: &models.Message{Text: "hi", Chat: models.Chat{ID: -100, Type: models.ChatTypeGroup}}})
	if len(fm.texts) != n {
		t.Fatal("бот ответил в группе")
	}
}

func TestRestartHooks(t *testing.T) {
	a, _, _ := bkTestApp(t)
	called := false
	a.SetRestart(func() { called = true })
	a.requestRestart()
	if !called || !a.RestartRequested() {
		t.Fatal("перезапуск не запрошен")
	}
	if !a.EnterWork() {
		t.Fatal("веб-запрос не начался")
	}
	a.LeaveWork()
	if a.bgActive.Load() != 0 {
		t.Fatal("счётчик веб-запросов не вернулся к нулю")
	}
}

// После перезапуска: сообщение не доставлено — повторить при следующем
// старте; сверка без панели — одна тревога, повтор через 5 минут.
func TestRestoreGuardRetries(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	a.bkState.Guard = &model.RestoreGuard{At: time.Now().UTC().Format(time.RFC3339), BackupAt: "2026-11-01T10:00:00Z", Chat: 100}
	fm.kbFail = true
	a.noticeRestored(ctx)
	fm.kbFail = false
	if a.restoreGuard().Notified {
		t.Fatal("недоставленное сообщение отмечено доставленным")
	}
	a.noticeRestored(ctx)
	if !a.restoreGuard().Notified {
		t.Fatal("доставленное сообщение не отмечено")
	}

	now := time.Now()
	a.guardTick(ctx, now)
	a.guardTick(ctx, now.Add(time.Minute))
	a.guardTick(ctx, now.Add(guardSyncRetry+time.Second))
	if strings.Count(fm.joined(), "Не получилось сверить сроки") != 1 || !a.restoreSyncPending() {
		t.Fatalf("тревога о сверке: %d раз", strings.Count(fm.joined(), "Не получилось сверить сроки"))
	}
	// Без метки восстановления ничего не делается.
	a.bkState.Guard = nil
	a.guardTick(ctx, now.Add(time.Hour))
	a.noticeRestored(ctx)
	if a.restoreSyncPending() || a.restoredBefore(time.Now()) || a.restoreChanged(1) {
		t.Fatal("защита сработала без восстановления")
	}
}

// Сверка сроков: постранично, без доп-подписок, без двойников и без тех,
// кого нет в базе бота.
func TestSyncExpiryFromPanel(t *testing.T) {
	a, _, st := bkTestApp(t)
	ctx := context.Background()
	for _, id := range []int64{1000000001, 1000000003, 1000000004, 1000000005, -1700000000001} {
		_ = st.UpsertUser(ctx, id)
		_ = st.SetSubExpiry(ctx, id, "2026-11-10T00:00:00Z", "trial")
	}
	a.botCfg.AddSub.UsernameSuffix = "_extra"
	var mu sync.Mutex
	pages := map[string]string{
		"0": `{"uuid":"a","username":"tg_1000000001","telegramId":1000000001,"expireAt":"2027-01-01T00:00:00Z"},` +
			`{"uuid":"b","username":"tg_1000000001_extra","telegramId":1000000001,"expireAt":"2030-01-01T00:00:00Z"},` +
			`{"uuid":"c","username":"x","telegramId":1000000003,"expireAt":"2027-02-01T00:00:00Z"}`,
		"250": `{"uuid":"d","username":"y","telegramId":1000000003,"expireAt":"2027-03-01T00:00:00Z"},` +
			`{"uuid":"e","username":"tg_1000000004","telegramId":1000000004,"expireAt":"2027-04-01T00:00:00Z"},` +
			`{"uuid":"f","username":"tg_1000000099","telegramId":1000000099,"expireAt":"2027-04-01T00:00:00Z"},` +
			`{"uuid":"g","username":"tg_0","telegramId":0,"expireAt":"2027-04-01T00:00:00Z"},` +
			`{"uuid":"h","username":"tg_-1700000000001","expireAt":"2027-05-01T00:00:00Z"},` +
			`{"uuid":"i","username":"tg_1000000005","telegramId":1000000005,"expireAt":"2026-11-05T00:00:00Z"}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"users":[` + pages[r.URL.Query().Get("start")] + `],"total":257}}`))
	}))
	t.Cleanup(srv.Close)
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	changed, err := a.syncExpiryFromPanel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 4 || changed[0] != -1700000000001 || changed[1] != 1000000001 || changed[2] != 1000000004 || changed[3] != 1000000005 {
		t.Fatalf("обновлены: %v", changed)
	}
	u, _ := st.GetUser(ctx, 1000000003)
	if u.SubExpireAt != "2026-11-10T00:00:00Z" {
		t.Fatal("двойник в панели перезаписал срок")
	}
	if u, _ := st.GetUser(ctx, 1000000099); u != nil {
		t.Fatal("сверка завела пользователя, которого не было в базе")
	}
	if u, _ := st.GetUser(ctx, 1000000001); u.NotifyKind != "paid" {
		t.Fatalf("тип напоминания после продления: %q", u.NotifyKind)
	}
	// Срок в панели урезали — вид подписки не меняется.
	if u, _ := st.GetUser(ctx, 1000000005); u.NotifyKind != "trial" || !sameMoment(u.SubExpireAt, "2026-11-05T00:00:00Z") {
		t.Fatalf("урезанный срок: %q %q", u.NotifyKind, u.SubExpireAt)
	}
	// Без панели — ошибка, а не «всё сверено».
	a.panel = nil
	if _, err := a.syncExpiryFromPanel(ctx); err == nil {
		t.Fatal("сверка без панели прошла")
	}
}

// trapStore — хранилище, которое помнит, какие проходы до него дошли.
type trapStore struct {
	storage.Storage
	mu    sync.Mutex
	calls []string
}

func (s *trapStore) hit(name string) {
	s.mu.Lock()
	s.calls = append(s.calls, name)
	s.mu.Unlock()
}
func (s *trapStore) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.calls
	s.calls = nil
	return out
}
func (s *trapStore) ListAutoPay(ctx context.Context) ([]model.AutoPay, error) {
	s.hit("autopay")
	return s.Storage.ListAutoPay(ctx)
}
func (s *trapStore) UsersForNotify(ctx context.Context) ([]model.User, error) {
	s.hit("reminders")
	return s.Storage.UsersForNotify(ctx)
}
func (s *trapStore) ListSubRepairTargets(ctx context.Context) ([]storage.SubRepairTarget, error) {
	s.hit("subrepair")
	return s.Storage.ListSubRepairTargets(ctx)
}
func (s *trapStore) ListTrialResetTargets(ctx context.Context, maxResets, limit int) ([]storage.TrialResetTarget, error) {
	s.hit("trialreset")
	return s.Storage.ListTrialResetTargets(ctx, maxResets, limit)
}
func (s *trapStore) ListTrafficBonuses(ctx context.Context, limit int) ([]storage.TrafficBonusTarget, error) {
	s.hit("bonus")
	return s.Storage.ListTrafficBonuses(ctx, limit)
}
func (s *trapStore) DueTorrentUnblocks(ctx context.Context, now string) ([]model.TorrentReport, error) {
	s.hit("torrent")
	return s.Storage.DueTorrentUnblocks(ctx, now)
}
func (s *trapStore) ListUnresolvedPending(ctx context.Context, before string, limit int) ([]model.PendingInvoice, error) {
	s.hit("reconcile")
	return s.Storage.ListUnresolvedPending(ctx, before, limit)
}

// Фоновые проходы: во время восстановления не начинается ни один; пока сроки
// не сверены с панелью, стоят те, что смотрят на срок подписки.
func TestBackgroundPassesRespectRestore(t *testing.T) {
	a, _, st := bkTestApp(t)
	ctx := context.Background()
	ts := &trapStore{Storage: st}
	a.store = ts
	a.panel = panelUsersStub(t, "")
	a.botCfg.YooKassa = model.YooKassaConfig{Enabled: true, AutoPay: true, ShopID: "s", SecretKey: "k"}
	a.botCfg.Reminders.Enabled = true
	a.botCfg.Reminders.TrialEnabled = true
	a.botCfg.Trial = model.TrialConfig{Enabled: true, Days: 3, ResetUnused: true, ResetUnusedMax: 1}
	run := func() []string {
		a.autoPayOnce(ctx)
		a.remindOnce(ctx)
		a.repairSubscriptions(ctx)
		a.resetTrialsOnce(ctx)
		a.sweepBonusTrafficOnce(ctx)
		a.torrentUnblockOnce(ctx)
		a.reconcileOnce(ctx)
		return ts.got()
	}
	all := []string{"autopay", "reminders", "subrepair", "trialreset", "bonus", "torrent", "reconcile"}
	// Обычный режим — каждый проход доходит до базы (иначе проверка ниже
	// ничего не доказывает).
	if got := run(); strings.Join(got, ",") != strings.Join(all, ",") {
		t.Fatalf("без восстановления дошли не все проходы: %v", got)
	}
	a.restoring.Store(true)
	if got := run(); len(got) != 0 {
		t.Fatalf("во время восстановления начались: %v", got)
	}
	a.restoring.Store(false)
	a.bkState.Guard = &model.RestoreGuard{At: time.Now().UTC().Format(time.RFC3339)}
	if got := run(); strings.Join(got, ",") != "torrent,reconcile" {
		t.Fatalf("до сверки сроков работают: %v", got)
	}
	a.bkState.Guard.Synced = true
	if got := run(); strings.Join(got, ",") != strings.Join(all, ",") {
		t.Fatalf("после сверки сроков стоят: %v", got)
	}
	if a.bgActive.Load() != 0 {
		t.Fatal("счётчик фоновой работы не вернулся к нулю")
	}
}

// tgServer — сервер Bot API для отдельного бота.
func tgServer(t *testing.T, fail bool) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot can't initiate conversation with a user"}`))
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"b","username":"store_bot"}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7,"date":1,"chat":{"id":100,"type":"private"}}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestTGBackupSender(t *testing.T) {
	ctx := context.Background()
	const tok = "1000000002:backup-bot-token-aaaaaaaaaaaaaaaaaaaaaaa"
	srv, seen := tgServer(t, false)
	s, err := newTGBackupSender(tok, bot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if name, err := s.Check(ctx); err != nil || name != "store_bot" {
		t.Fatalf("Check: %q %v", name, err)
	}
	if err := s.SendText(ctx, 100, "привет"); err != nil {
		t.Fatal(err)
	}
	if err := s.SendFile(ctx, 100, "f.bak", []byte("данные"), "подпись"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/bot" + tok + "/getMe", "/bot" + tok + "/sendMessage", "/bot" + tok + "/sendDocument"}
	if strings.Join(*seen, " ") != strings.Join(want, " ") {
		t.Fatalf("запросы к Bot API: %v", *seen)
	}

	bad, _ := tgServer(t, true)
	s, _ = newTGBackupSender(tok, bot.WithServerURL(bad.URL))
	err = s.SendText(ctx, 100, "x")
	if !errors.Is(err, bot.ErrorForbidden) || strings.Contains(err.Error(), tok) {
		t.Fatalf("ошибка отдельного бота: %v", err)
	}
	if _, err := newTGBackupSender(""); err == nil {
		t.Fatal("пустой токен принят")
	}
}

// Сбои отдельного бота: проверка токена, отправка файла — токен нигде не
// светится.
func TestSeparateBotFailures(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	const tok = "1000000002:backup-bot-token-aaaaaaaaaaaaaaaaaaaaaaa"
	a.newBackupSender = func(string) (backupSender, error) { return nil, errors.New("сломан " + tok) }
	a.handleCallback(ctx, cb(100, "bk:dbot"))
	a.handleMessage(ctx, msgText(100, tok))
	if a.backupCfg().BotToken != "" || !strings.Contains(lastText(fm), "Токен не подошёл: сломан ***") {
		t.Fatalf("сбой проверки токена: %q", lastText(fm))
	}
	if a.getUI(100).adminInput != "" {
		t.Fatal("после неудачной проверки ввод остался взведённым")
	}
	fs := &fakeSender{name: "store_bot"}
	a.newBackupSender = func(string) (backupSender, error) { return fs, nil }
	a.handleCallback(ctx, cb(100, "bk:dbot"))
	a.handleMessage(ctx, msgText(100, tok))
	if a.backupCfg().BotToken != tok {
		t.Fatal("отдельный бот не сохранён")
	}
	bkSetPassword(t, a, "пароль-бэкапа")
	a.newBackupSender = func(string) (backupSender, error) { return &brokenSender{tok: tok}, nil }
	a.handleCallback(ctx, cb(100, "bk:now"))
	if !strings.Contains(fm.joined(), "Telegram не принял файл: нет связи ***") || strings.Contains(fm.joined(), tok) {
		t.Fatalf("сбой отправки через отдельного бота: %q", lastText(fm))
	}
	if a.backupCfg().LastAt != "" {
		t.Fatal("неудачный бэкап отмечен отправленным")
	}
}

type brokenSender struct{ tok string }

func (b *brokenSender) Check(context.Context) (string, error)         { return "x", nil }
func (b *brokenSender) SendText(context.Context, int64, string) error { return nil }
func (b *brokenSender) SendFile(context.Context, int64, string, []byte, string) error {
	return errors.New("нет связи " + b.tok)
}

// Файл перерос лимит Telegram: тревога админу, повтор — по расписанию, а не
// каждый час; время бэкапа не отмечается.
func TestBackupTooBigScheduled(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	bkSetPassword(t, a, "пароль-бэкапа")
	a.handleCallback(ctx, cb(100, "bk:h:24"))
	a.uploadLimitD = 64
	now := time.Now()
	a.backupOnce(ctx, now)
	if docCount(fm) != 0 || !strings.Contains(fm.joined(), "файл больше 50 МБ") || !strings.Contains(fm.joined(), "через 24 ч") {
		t.Fatalf("большой файл: %q", lastText(fm))
	}
	if a.backupCfg().LastAt != "" || !a.bkNextTry.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("повтор после большого файла: %s", a.bkNextTry.Sub(now))
	}
	a.handleCallback(ctx, cb(100, "bk:now"))
	if !strings.Contains(lastText(fm), "файл больше 50 МБ") {
		t.Fatal("«Сделать сейчас» не сказало про размер")
	}
}

// Ранние выходы расписания: бот не настроен, бэкап уже идёт, база не
// подключена.
func TestBackupOnceGuards(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	bkSetPassword(t, a, "пароль-бэкапа")
	a.handleCallback(ctx, cb(100, "bk:h:1"))
	now := time.Now()
	a.botCfg.Installed = false
	a.backupOnce(ctx, now)
	a.botCfg.Installed = true
	a.bkMu.Lock()
	a.backupOnce(ctx, now)
	a.bkMu.Unlock()
	if docCount(fm) != 0 {
		t.Fatal("бэкап ушёл на ненастроенном боте или поверх идущего")
	}
	a.store = nil
	a.backupOnce(ctx, now)
	if !strings.Contains(fm.joined(), "база не подключена") {
		t.Fatal("нет базы — нет тревоги")
	}
	a.store = st
	c, cancel := context.WithCancel(ctx)
	cancel()
	a.RunBackups(c) // с отменённым контекстом сразу выходит
}

func TestSameMoment(t *testing.T) {
	if !sameMoment("2026-11-03T00:00:00.000Z", "2026-11-03T03:00:00+03:00") {
		t.Fatal("один момент в разных записях")
	}
	if sameMoment("2026-11-03T00:00:00Z", "2026-11-04T00:00:00Z") || !sameMoment("x", "x") || sameMoment("", "2026-11-03T00:00:00Z") {
		t.Fatal("sameMoment")
	}
}

// Пополнение из окна после восстановления тоже идёт на проверку, баланс не
// начисляется.
func TestRestoreGuardTopUp(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	_ = st.UpsertUser(ctx, 1000000001)
	a.bkState.Guard = &model.RestoreGuard{At: time.Now().UTC().Format(time.RFC3339), Synced: true}
	pi := &model.PendingInvoice{Method: model.PayMethodPlatega, ExtID: "pl-old", TelegramID: 1000000001, Purpose: "topup",
		Kopecks: 50000, CreatedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)}
	if err := st.AddPendingInvoice(ctx, pi); err != nil {
		t.Fatal(err)
	}
	if a.reconcileFinalize(ctx, st, pi, "500.00 RUB") {
		t.Fatal("пополнение из окна выдано")
	}
	a.flushReview(ctx)
	u, _ := st.GetUser(ctx, 1000000001)
	if u.Balance != 0 || !strings.Contains(fm.joined(), "пополнение") {
		t.Fatalf("пополнение из окна: баланс %d, %q", u.Balance, lastText(fm))
	}
	// Счёт, созданный после восстановления, идёт как обычно (не в список).
	n := strings.Count(fm.joined(), "Оплаты на ручную проверку")
	fresh := &model.PendingInvoice{Method: model.PayMethodPlatega, ExtID: "pl-new", TelegramID: 1000000001, Purpose: "topup",
		Kopecks: 100, CreatedAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339)}
	_ = st.AddPendingInvoice(ctx, fresh)
	a.reconcileFinalize(ctx, st, fresh, "1.00 RUB")
	a.flushReview(ctx)
	if strings.Count(fm.joined(), "Оплаты на ручную проверку") != n {
		t.Fatal("свежий счёт ушёл на проверку")
	}
	if u, _ := st.GetUser(ctx, 1000000001); u.Balance != 100 {
		t.Fatalf("свежее пополнение не начислено: %d", u.Balance)
	}
	// Длинный список режется на несколько сообщений.
	for i := 0; i < reviewChunk+1; i++ {
		a.addReview("x", "", 1, "1", 1, "id", nil)
	}
	before := strings.Count(fm.joined(), "Оплаты на ручную проверку")
	a.flushReview(ctx)
	if strings.Count(fm.joined(), "Оплаты на ручную проверку") != before+2 {
		t.Fatal("длинный список не разбит на сообщения")
	}
}

// Сбой панели посреди сверки — ошибка, а не «всё сверено».
func TestSyncExpiryPanelError(t *testing.T) {
	a, _, _ := bkTestApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	if _, err := a.syncExpiryFromPanel(context.Background()); err == nil {
		t.Fatal("сбой панели не дал ошибки")
	}
	a.store = nil
	if a.reviewNoted(context.Background(), "x") != true {
		t.Fatal("без базы отметка «уже на проверке» должна считаться поставленной")
	}
}

// panelByName — панель, которая знает пользователей только по имени.
func panelByName(t *testing.T, users map[string]string) *remnawave.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if name, ok := strings.CutPrefix(r.URL.Path, "/api/users/by-username/"); ok {
			if exp, found := users[name]; found {
				_, _ = w.Write([]byte(`{"response":{"uuid":"u-` + name + `","username":"` + name + `","expireAt":"` + exp + `"}}`))
				return
			}
		}
		if r.URL.Path == "/api/users" {
			_, _ = w.Write([]byte(`{"response":{"users":[],"total":0}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"User not found"}`))
	}))
	t.Cleanup(srv.Close)
	return remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

// Первое списание после восстановления — только по сроку, подтверждённому
// панелью: срок разошёлся — обновить и не списывать; человека в панели нет —
// отложить; заплатил уже после восстановления — как обычно.
func TestAutoPayExpiryConfirmed(t *testing.T) {
	a, _, st := bkTestApp(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)
	a.bkState.Guard = &model.RestoreGuard{At: at.Format(time.RFC3339), Synced: true}
	exp := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	for _, id := range []int64{1000000001, 1000000002, 1000000003} {
		_ = st.UpsertUser(ctx, id)
		_ = st.SetSubExpiry(ctx, id, exp.Format(time.RFC3339), "paid")
		_ = st.SetAutoPay(ctx, &model.AutoPay{TelegramID: id, Method: model.PayMethodYooKassa, MethodID: "m", Months: 1, Enabled: true})
	}
	a.panel = panelByName(t, map[string]string{
		"tg_1000000001": "2026-11-10T00:00:00.000Z", // совпал
		"tg_1000000002": "2027-01-10T00:00:00.000Z", // продлён после бэкапа
	})
	now := time.Now()
	ap := func(id int64) *model.AutoPay {
		return &model.AutoPay{TelegramID: id, LastPayAt: at.Add(-24 * time.Hour).Format(time.RFC3339)}
	}
	if !a.autoPayExpiryConfirmed(ctx, ap(1000000001), exp, now) {
		t.Fatal("подтверждённый панелью срок не пустил списание")
	}
	if a.autoPayExpiryConfirmed(ctx, ap(1000000002), exp, now) {
		t.Fatal("списание по сроку из файла, хотя панель продлила")
	}
	if u, _ := st.GetUser(ctx, 1000000002); !sameMoment(u.SubExpireAt, "2027-01-10T00:00:00Z") {
		t.Fatalf("срок из панели не перенесён: %q", u.SubExpireAt)
	}
	if a.autoPayExpiryConfirmed(ctx, ap(1000000003), exp, now) {
		t.Fatal("списание человеку, которого панель не знает")
	}
	if rec := a.getAutoPay(ctx, 1000000003); rec == nil || rec.NextTryAt == "" {
		t.Fatal("автопродление неподтверждённого не отложено")
	}
	paidAfter := &model.AutoPay{TelegramID: 1000000003, LastPayAt: at.Add(time.Minute).Format(time.RFC3339)}
	if !a.autoPayExpiryConfirmed(ctx, paidAfter, exp, now) {
		t.Fatal("заплатившему после восстановления проверка не нужна")
	}
	a.bkState.Guard = nil
	if !a.autoPayExpiryConfirmed(ctx, ap(1000000003), exp, now) {
		t.Fatal("без восстановления проверка не нужна")
	}
}

// После восстановления автопродление стоит, пока админ его не включит.
func TestAutoPayHeldAfterRestore(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	ts := &trapStore{Storage: st}
	a.store = ts
	a.botCfg.YooKassa = model.YooKassaConfig{Enabled: true, AutoPay: true, ShopID: "s", SecretKey: "k"}
	a.bkState.Guard = &model.RestoreGuard{At: time.Now().UTC().Format(time.RFC3339), Synced: true, AutoPayHeld: true, Chat: 100}
	a.autoPayOnce(ctx)
	if got := ts.got(); len(got) != 0 {
		t.Fatalf("автопродление пошло до подтверждения админа: %v", got)
	}
	a.noticeRestored(ctx)
	if !hasButton(fm, "bk:apon") || !strings.Contains(fm.joined(), "Автопродление после восстановления стоит") {
		t.Fatal("после перезапуска нет кнопки включить автопродление")
	}
	a.handleCallback(ctx, cb(100, "menu:backup"))
	if !strings.Contains(lastText(fm), "Автопродление после восстановления стоит") {
		t.Fatal("экран бэкапа не говорит, что автопродление стоит")
	}
	a.handleCallback(ctx, cb(100, "bk:apon"))
	if a.autoPayHeld() {
		t.Fatal("кнопка не включила автопродление")
	}
	a.autoPayOnce(ctx)
	if got := ts.got(); len(got) != 1 {
		t.Fatalf("после включения автопродление не пошло: %v", got)
	}
}

// Звёзды через настоящую сверку: у постоянного клиента длинная история
// журнала, оплата из окна приходит админу один раз.
func TestStarsReviewOnceWithLongHistory(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	_ = st.UpsertUser(ctx, 1000000002)
	for i := 0; i < 60; i++ {
		_ = st.AddPayLog(ctx, &model.PayLogEntry{ExtID: "old-" + itoa(i), TelegramID: 1000000002, Method: "stars", Stage: "paid"})
	}
	a.botCfg.Stars.Enabled = true
	a.bkState.Guard = &model.RestoreGuard{At: time.Now().UTC().Format(time.RFC3339), Synced: true}
	fm.starTx = []models.StarTransaction{{ID: "st-window", Amount: 50, Date: int(time.Now().Add(-time.Hour).Unix()),
		Source: &models.TransactionPartner{Type: models.TransactionPartnerTypeUser, User: &models.TransactionPartnerUser{
			User: models.User{ID: 1000000002}, InvoicePayload: "stars:1"}}}}
	for i := 0; i < 3; i++ {
		a.reconcileStars(ctx)
	}
	if n := strings.Count(fm.joined(), "st-window"); n != 1 {
		t.Fatalf("оплата из окна пришла админу %d раз", n)
	}
	if paid, _ := st.PaymentByExtID(ctx, "st-window"); paid {
		t.Fatal("оплата из окна выдана автоматически")
	}
}

// Список не дошёл до админа — счёт не закрывается и найдётся снова.
func TestReviewNotDeliveredKeepsInvoice(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	_ = st.UpsertUser(ctx, 1000000001)
	a.bkState.Guard = &model.RestoreGuard{At: time.Now().UTC().Format(time.RFC3339), Synced: true}
	pi := &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "yk-w", TelegramID: 1000000001, Months: 1,
		CreatedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)}
	_ = st.AddPendingInvoice(ctx, pi)
	open := func() int {
		l, _ := st.ListUnresolvedPending(ctx, time.Now().UTC().Format(time.RFC3339), 10)
		return len(l)
	}
	fm.kbFail = true
	a.reconcileFinalize(ctx, st, pi, "1 RUB")
	a.flushReview(ctx)
	fm.kbFail = false
	if open() != 1 {
		t.Fatal("счёт закрыт, хотя список не дошёл")
	}
	list, _ := st.ListUnresolvedPending(ctx, time.Now().UTC().Format(time.RFC3339), 10)
	a.reconcileFinalize(ctx, st, &list[0], "1 RUB")
	a.flushReview(ctx)
	if open() != 0 || !strings.Contains(fm.joined(), "yk-w") || !strings.Contains(fm.joined(), "ЮKassa") {
		t.Fatalf("после доставки счёт не закрыт или способ оплаты не назван: %q", lastText(fm))
	}
}

// После сверки сроков админ получает заявки на перевод из файла.
func TestRestoredP2PReview(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	_ = st.UpsertUser(ctx, 1000000001)
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	_ = st.CreateP2PRequest(ctx, &model.P2PRequest{ID: 501, TelegramID: 1000000001, Months: 1, Price: "199", Status: model.P2PAwaiting, CreatedAt: old})
	_ = st.CreateP2PRequest(ctx, &model.P2PRequest{ID: 502, TelegramID: 1000000001, Months: 1, Price: "299", Status: model.P2PSubmitted, CreatedAt: old})
	_ = st.CreateP2PRequest(ctx, &model.P2PRequest{ID: 503, TelegramID: 1000000001, Months: 1, Price: "99", Status: model.P2PApproved, CreatedAt: old})
	a.bkState.Guard = &model.RestoreGuard{At: time.Now().UTC().Format(time.RFC3339)}
	a.panel = panelUsersStub(t, "")
	a.guardTick(ctx, time.Now())
	txt := fm.joined()
	if !strings.Contains(txt, "Заявки на перевод из восстановленной базы") || !strings.Contains(txt, "#501") ||
		!strings.Contains(txt, "#502") || strings.Contains(txt, "#503") {
		t.Fatalf("заявки на перевод после сверки: %q", txt)
	}
}

// Токены ботов не попадают в тексты ошибок для админа.
func TestErrTextRedactsTokens(t *testing.T) {
	a, _, _ := bkTestApp(t)
	err := errors.New("Post https://api.telegram.org/bot" + a.cfg.BotToken + "/sendDocument: timeout; другой 1:xyz-token")
	got := a.errText(err, "1:xyz-token")
	if strings.Contains(got, a.cfg.BotToken) || strings.Contains(got, "xyz-token") || !strings.Contains(got, "***") {
		t.Fatalf("errText: %q", got)
	}
	if a.errText(errors.New("<b>")) != "&lt;b&gt;" {
		t.Fatal("errText не экранирует")
	}
}

// Автопродление через настоящий проход: срок в панели позже файла — бот
// обновляет срок и не списывает.
func TestAutoPayOnceConfirmsWithPanel(t *testing.T) {
	a, _, st := bkTestApp(t)
	ctx := context.Background()
	a.botCfg.YooKassa = model.YooKassaConfig{Enabled: true, AutoPay: true, AutoPayDays: 3, ShopID: "s", SecretKey: "k"}
	a.bkState.Guard = &model.RestoreGuard{At: time.Now().UTC().Format(time.RFC3339), Synced: true}
	exp := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	_ = st.UpsertUser(ctx, 1000000001)
	_ = st.SetSubExpiry(ctx, 1000000001, exp.Format(time.RFC3339), "paid")
	_ = st.SetAutoPay(ctx, &model.AutoPay{TelegramID: 1000000001, Method: model.PayMethodYooKassa, MethodID: "m", Months: 1, Enabled: true})
	later := exp.Add(30 * 24 * time.Hour).Format(time.RFC3339)
	a.panel = panelByName(t, map[string]string{"tg_1000000001": later})
	a.autoPayOnce(ctx)
	if u, _ := st.GetUser(ctx, 1000000001); !sameMoment(u.SubExpireAt, later) {
		t.Fatalf("проход автопродления не сверил срок с панелью: %q", u.SubExpireAt)
	}
	if rec := a.getAutoPay(ctx, 1000000001); rec == nil || rec.LastPayAt != "" || rec.PaidPeriod != "" {
		t.Fatalf("автопродление списало по сроку из файла: %+v", rec)
	}
}

// Настройки бэкапа из конфига прошлой версии при первом запуске уезжают в
// свою таблицу.
func TestBackupStateMovesOutOfConfig(t *testing.T) {
	_, _, st := bkTestApp(t)
	ctx := context.Background()
	cfg := &model.BotConfig{Installed: true, Language: "ru",
		Backup: &model.BackupConfig{Hours: 12, Password: "старый-пароль", ChatID: -1000000000003}}
	if err := st.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	b, _ := rebootApp(t, st)
	if bc := b.backupCfg(); bc.Password != "старый-пароль" || bc.Hours != 12 || bc.ChatID != -1000000000003 {
		t.Fatalf("настройки не перенесены: %+v", bc)
	}
	if got, ok, _ := st.LoadBackupState(ctx); !ok || got.Password != "старый-пароль" {
		t.Fatal("настройки не записаны в свою таблицу")
	}
	if c, _, _ := st.LoadConfig(ctx); c.Backup != nil {
		t.Fatal("настройки остались в конфиге")
	}
	// Конфиг, сохранённый без поля (старый образ после отката), их не трогает.
	_ = st.SaveConfig(ctx, &model.BotConfig{Installed: true, Language: "ru"})
	c, _ := rebootApp(t, st)
	if c.backupCfg().Password != "старый-пароль" {
		t.Fatal("сохранение конфига без поля стёрло настройки бэкапа")
	}
}

package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"remnabot/internal/crypto"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
)

// bkTestApp — бот на настоящей SQLite: бэкап и восстановление проверяются
// на живой базе, а не на двойнике.
func bkTestApp(t *testing.T) (*App, *fakeMsg, storage.Storage) {
	t.Helper()
	a, fm, _ := newTestApp(t)
	c, err := crypto.NewFromKeyMaterial([]byte("ключ-сервера"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := storage.Open(model.DBSQLite, filepath.Join(t.TempDir(), "bot.db"), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a.store = st
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.cfg.BotToken = "1000000001:main-bot-token-aaaaaaaaaaaaaaaaaaaaaaaaa"
	a.runInline = true
	if err := a.saveConfigOnly(ctx); err != nil {
		t.Fatal(err)
	}
	return a, fm, st
}

// rebootApp — тот же бот после перезапуска: конфиг читается из базы заново.
func rebootApp(t *testing.T, st storage.Storage) (*App, *fakeMsg) {
	t.Helper()
	a, fm, _ := newTestApp(t)
	a.store = st
	a.runInline = true
	if err := a.loadConfigIfStore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.botCfg == nil {
		t.Fatal("после перезапуска конфиг не прочитан")
	}
	return a, fm
}

func bkSetPassword(t *testing.T, a *App, pass string) {
	t.Helper()
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "bk:pass"))
	a.handleMessage(ctx, msgText(100, pass))
	if got := a.backupCfg().Password; got != pass {
		t.Fatalf("пароль не сохранён: %q", got)
	}
}

// lastDoc — последний файл, отправленный ботом.
func lastDoc(t *testing.T, fm *fakeMsg) (string, []byte, int64) {
	t.Helper()
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.sentDocIDs) == 0 {
		t.Fatal("бот не отправил ни одного файла")
	}
	name := fm.sentDocIDs[len(fm.sentDocIDs)-1]
	return name, fm.docs[name], fm.docTo[len(fm.docTo)-1]
}

func docCount(fm *fakeMsg) int {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	return len(fm.sentDocIDs)
}

func hasUser(t *testing.T, st storage.Storage, id int64) bool {
	t.Helper()
	u, err := st.GetUser(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u != nil
}

func hasButton(fm *fakeMsg, data string) bool {
	return slices.Contains(fm.allCallbackData(), data)
}

// cbOn — нажатие кнопки под конкретным сообщением.
func cbOn(uid int64, data string, msgID int) *models.CallbackQuery {
	q := cb(uid, data)
	q.Message = models.MaybeInaccessibleMessage{Message: &models.Message{ID: msgID, Chat: models.Chat{ID: uid}}}
	return q
}

// restoreFile — прислать файл на восстановление.
func restoreFile(t *testing.T, a *App, fm *fakeMsg, file []byte) {
	t.Helper()
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "bk:restore"))
	fm.mu.Lock()
	fm.downloads = map[string][]byte{"bk-file": file}
	fm.mu.Unlock()
	a.handleDocument(ctx, docMsg(100, "bk-file", int64(len(file))))
}

func TestBackupNowAndRestoreEndToEnd(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	if err := st.UpsertUser(ctx, 1000000001); err != nil {
		t.Fatal(err)
	}
	if err := st.AddPayment(ctx, &model.Payment{TelegramID: 1000000001, Method: "stars", Months: 1, Amount: "100", Status: model.PaymentPaid, ExtID: "bk-test-1"}); err != nil {
		t.Fatal(err)
	}
	a.handleCallback(ctx, cb(100, "menu:backup"))
	a.handleCallback(ctx, cb(100, "bk:h:6"))
	if a.backupCfg().Hours != 6 {
		t.Fatalf("интервал не сохранён: %d", a.backupCfg().Hours)
	}

	// Без пароля бэкап не делается.
	a.handleCallback(ctx, cb(100, "bk:now"))
	if docCount(fm) != 0 {
		t.Fatal("бэкап ушёл без пароля")
	}
	bkSetPassword(t, a, "пароль-бэкапа")

	a.handleCallback(ctx, cb(100, "bk:now"))
	name, file, to := lastDoc(t, fm)
	if !strings.HasPrefix(name, "chill-remna-bot-backup-") || to != 100 || len(file) == 0 {
		t.Fatalf("файл бэкапа: %q → %d, %d байт", name, to, len(file))
	}
	if a.backupCfg().LastAt == "" {
		t.Fatal("время бэкапа не записано — расписание пошлёт его снова")
	}

	// База живёт дальше: новый пользователь, другой интервал.
	if err := st.UpsertUser(ctx, 1000000002); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, 1000000001); err != nil {
		t.Fatal(err)
	}
	a.handleCallback(ctx, cb(100, "bk:h:24"))

	restoreFile(t, a, fm, file)
	if !hasButton(fm, "bk:apply") {
		t.Fatal("нет экрана подтверждения")
	}
	// До подтверждения база не меняется.
	if hasUser(t, st, 1000000001) || !hasUser(t, st, 1000000002) {
		t.Fatal("база изменилась до подтверждения")
	}
	before := docCount(fm)
	a.handleCallback(ctx, cb(100, "bk:apply"))

	if docCount(fm) != before+1 {
		t.Fatal("бэкап текущей базы перед заменой не отправлен")
	}
	if !hasUser(t, st, 1000000001) || hasUser(t, st, 1000000002) {
		t.Fatal("база не вернулась к состоянию бэкапа")
	}
	paid, err := st.PaymentByExtID(ctx, "bk-test-1")
	if err != nil || !paid {
		t.Fatalf("оплата не вернулась: %v %v", paid, err)
	}
	bs, _, err := st.LoadBackupState(ctx)
	if err != nil || bs.Hours != 6 {
		t.Fatalf("настройки бэкапа не вернулись: %+v %v", bs, err)
	}
	if g := bs.Guard; g == nil || g.Synced || g.Chat != 100 || g.BackupAt == "" || !g.AutoPayHeld {
		t.Fatalf("метка восстановления не записана вместе с заменой: %+v", g)
	}
	if !a.RestartRequested() || !a.paused() {
		t.Fatal("после восстановления бот не ушёл на перезапуск")
	}
	// Старое состояние в памяти не должно лечь поверх восстановленного.
	if err := a.saveConfigOnly(ctx); err == nil {
		t.Fatal("сохранение конфига после восстановления не запрещено")
	}
	a.markBackupSent(ctx, time.Now())
	bs, _, _ = st.LoadBackupState(ctx)
	if bs.Hours != 6 || bs.Guard == nil {
		t.Fatal("восстановленное состояние бэкапа затёрто старым")
	}

	// После перезапуска бот пишет админу, что база на месте, — один раз.
	b, fm2 := rebootApp(t, st)
	b.noticeRestored(ctx)
	b.noticeRestored(ctx)
	if strings.Count(fm2.joined(), "Бот снова работает") != 1 {
		t.Fatalf("сообщение после перезапуска: %q", fm2.joined())
	}
}

func TestBackupRestoreAsksPassword(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	if err := st.UpsertUser(ctx, 1000000001); err != nil {
		t.Fatal(err)
	}
	bkSetPassword(t, a, "пароль-файла")
	a.handleCallback(ctx, cb(100, "bk:now"))
	_, file, _ := lastDoc(t, fm)

	// Новый сервер: пароль в настройках другой.
	bkSetPassword(t, a, "другой-пароль")
	if err := st.UpsertUser(ctx, 1000000002); err != nil {
		t.Fatal(err)
	}
	restoreFile(t, a, fm, file)
	if !a.wantsPass(100) || hasButton(fm, "bk:apply") {
		t.Fatal("бот не спросил пароль от файла")
	}
	a.handleMessage(ctx, msgText(100, "неверный-пароль"))
	if !strings.Contains(fm.joined(), "Пароль не подходит") || !a.wantsPass(100) {
		t.Fatal("неверный пароль не отклонён")
	}
	a.handleMessage(ctx, msgText(100, "пароль-файла"))
	if !hasButton(fm, "bk:apply") {
		t.Fatal("после верного пароля нет подтверждения")
	}
	a.handleCallback(ctx, cb(100, "bk:apply"))
	if hasUser(t, st, 1000000002) || !hasUser(t, st, 1000000001) {
		t.Fatal("база не восстановлена")
	}
	// Восстановленный конфиг несёт пароль того файла.
	bs, _, _ := st.LoadBackupState(ctx)
	if bs.Password != "пароль-файла" {
		t.Fatalf("пароль после восстановления: %q", bs.Password)
	}
}

// «Отмена» и уход из раздела снимают ожидание файла и пароля: следующий
// документ или текст не уходит в разбор бэкапа.
func TestBackupCancelDropsWaiting(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	bkSetPassword(t, a, "пароль-файла")
	a.handleCallback(ctx, cb(100, "bk:now"))
	_, file, _ := lastDoc(t, fm)
	bkSetPassword(t, a, "другой-пароль")

	restoreFile(t, a, fm, file)
	a.handleCallback(ctx, cb(100, "menu:backup"))
	if ui := a.getUI(100); ui.adminInput != "" || ui.awaitBackup || a.bkPendPeek(100) != nil {
		t.Fatal("уход из раздела не снял ожидание пароля")
	}
	a.handleCallback(ctx, cb(100, "bk:restore"))
	a.handleCallback(ctx, cb(100, "bk:cancel"))
	if a.getUI(100).awaitBackup {
		t.Fatal("«Отмена» не сняла ожидание файла")
	}
}

// Бэкап текущей базы не ушёл — замену не начинаем.
func TestBackupRestoreStopsWithoutSafetyCopy(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	bkSetPassword(t, a, "пароль-бэкапа")
	a.handleCallback(ctx, cb(100, "bk:now"))
	_, file, _ := lastDoc(t, fm)
	if err := st.UpsertUser(ctx, 1000000002); err != nil {
		t.Fatal(err)
	}
	restoreFile(t, a, fm, file)
	fm.forbidden = map[int64]bool{100: true}
	a.handleCallback(ctx, cb(100, "bk:apply"))
	if !hasUser(t, st, 1000000002) {
		t.Fatal("база заменена без страховочного бэкапа")
	}
	if a.paused() || a.RestartRequested() {
		t.Fatal("техработы не сняты после отказа")
	}
	if err := a.saveConfigOnly(ctx); err != nil {
		t.Fatalf("конфиг не сохраняется после отказа: %v", err)
	}
}

func TestBackupRejectsForeignFile(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	restoreFile(t, a, fm, []byte("просто текст"))
	if !strings.Contains(fm.joined(), "не файл бэкапа") || hasButton(fm, "bk:apply") {
		t.Fatal("чужой файл не отклонён")
	}
}

// Файл бэкапа бот сам не удаляет никогда — только по кнопке «Убрать».
func TestBackupFileStaysInChat(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	bkSetPassword(t, a, "пароль-бэкапа")
	a.handleCallback(ctx, cb(100, "bk:now"))
	fm.mu.Lock()
	docID := fm.docMsgIDs[0]
	fm.mu.Unlock()
	if !hasButton(fm, "x:close") {
		t.Fatal("под файлом нет кнопки «Убрать»")
	}
	for _, data := range []string{"menu:home", "menu:system", "menu:backup", "bk:now", "bk:dest", "bk:pass", "inp:cancel", "menu:backup", "bk:h:2"} {
		a.handleCallback(ctx, cb(100, data))
	}
	a.handleMessage(ctx, msgText(100, "/start"))
	a.backupOnce(ctx, time.Now().Add(48*time.Hour))
	fm.mu.Lock()
	gone := slices.Contains(fm.deleted, docID)
	fm.mu.Unlock()
	if gone {
		t.Fatal("бот сам удалил файл бэкапа")
	}
	a.handleCallback(ctx, cbOn(100, "x:close", docID))
	fm.mu.Lock()
	gone = slices.Contains(fm.deleted, docID)
	fm.mu.Unlock()
	if !gone {
		t.Fatal("кнопка «Убрать» не удалила файл")
	}
}

func TestBackupSchedule(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	a.handleCallback(ctx, cb(100, "bk:h:4"))
	a.backupOnce(ctx, now)
	if docCount(fm) != 0 {
		t.Fatal("бэкап по расписанию ушёл без пароля")
	}
	bkSetPassword(t, a, "пароль-бэкапа")

	a.backupOnce(ctx, now)
	if docCount(fm) != 1 {
		t.Fatal("первый бэкап по расписанию не ушёл")
	}
	a.backupOnce(ctx, now.Add(3*time.Hour))
	if docCount(fm) != 1 {
		t.Fatal("бэкап ушёл раньше интервала")
	}
	a.backupOnce(ctx, now.Add(4*time.Hour))
	if docCount(fm) != 2 {
		t.Fatal("бэкап не ушёл по интервалу")
	}

	// Чат для бэкапов недоступен: одна попытка, тревога админу, повтор не
	// раньше чем через час.
	a.mu.Lock()
	a.bkState.ChatID = -1000000000001
	a.mu.Unlock()
	fm.forbidden = map[int64]bool{-1000000000001: true}
	t2 := now.Add(8 * time.Hour)
	a.backupOnce(ctx, t2)
	if docCount(fm) != 2 || !strings.Contains(fm.joined(), "по расписанию не отправлен") {
		t.Fatal("неудача бэкапа не подняла тревогу")
	}
	lastAt := a.backupCfg().LastAt
	a.backupOnce(ctx, t2.Add(30*time.Minute))
	if strings.Count(fm.joined(), "по расписанию не отправлен") != 1 {
		t.Fatal("после неудачи бот дёргает чат чаще раза в час")
	}
	delete(fm.forbidden, -1000000000001)
	a.backupOnce(ctx, t2.Add(time.Hour))
	if docCount(fm) != 3 {
		t.Fatal("после паузы бэкап не повторён")
	}
	if _, _, to := lastDoc(t, fm); to != -1000000000001 {
		t.Fatalf("бэкап ушёл не в выбранный чат: %d", to)
	}
	if a.backupCfg().LastAt == lastAt {
		t.Fatal("время удачного бэкапа не записано")
	}
	if strings.Count(strings.Join(fm.allCallbackData(), " "), "x:close") != 2 {
		t.Fatal("кнопка «Убрать» должна быть только под файлами в личном чате админа")
	}

	// Выключено — не шлём.
	a.handleCallback(ctx, cb(100, "bk:h:0"))
	a.backupOnce(ctx, t2.Add(100*time.Hour))
	if docCount(fm) != 3 {
		t.Fatal("бэкап ушёл при выключенном расписании")
	}
}

func TestBackupChatSetting(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	fm.forbidden = map[int64]bool{-1000000000009: true}
	a.handleCallback(ctx, cb(100, "bk:dchat"))
	a.handleMessage(ctx, msgText(100, "-1000000000009"))
	if a.backupCfg().ChatID != 0 || !strings.Contains(fm.joined(), "Не получилось написать") {
		t.Fatal("недоступный чат принят")
	}
	a.handleCallback(ctx, cb(100, "bk:dchat"))
	a.handleMessage(ctx, msgText(100, "1000000005"))
	if a.backupCfg().ChatID != 0 || a.getUI(100).adminInput != "bk_chat" {
		t.Fatal("положительный id (человек) принят как чат для бэкапов")
	}
	a.handleMessage(ctx, msgText(100, "не число"))
	if a.getUI(100).adminInput != "bk_chat" {
		t.Fatal("после неверного id бот перестал ждать ввод")
	}
	a.handleMessage(ctx, msgText(100, "-1000000000001"))
	if a.backupCfg().ChatID != -1000000000001 {
		t.Fatalf("чат не сохранён: %d", a.backupCfg().ChatID)
	}
	a.handleCallback(ctx, cb(100, "bk:dme"))
	if a.backupCfg().ChatID != 0 {
		t.Fatal("не вернулось «мне»")
	}
	// «Отмена» ввода возвращает на экран «Куда слать», а не в главное меню.
	a.handleCallback(ctx, cb(100, "bk:dchat"))
	a.handleCallback(ctx, cb(100, "inp:cancel"))
	if a.getUI(100).adminInput != "" || !hasButton(fm, "bk:dbot") {
		t.Fatal("«Отмена» не вернула на экран «Куда слать»")
	}
	fm.mu.Lock()
	last := fm.texts[len(fm.texts)-1]
	fm.mu.Unlock()
	if !strings.Contains(last, "Куда слать бэкапы") {
		t.Fatalf("после «Отмены» экран: %q", last)
	}
}

// fakeSender — отдельный бот для бэкапов.
type fakeSender struct {
	mu      sync.Mutex
	name    string
	texts   []int64
	files   []int64
	textErr error
}

func (s *fakeSender) Check(context.Context) (string, error) { return s.name, nil }
func (s *fakeSender) SendText(_ context.Context, chatID int64, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.textErr != nil {
		return s.textErr
	}
	s.texts = append(s.texts, chatID)
	return nil
}
func (s *fakeSender) SendFile(_ context.Context, chatID int64, _ string, _ []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files = append(s.files, chatID)
	return nil
}

func TestBackupViaSeparateBot(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	fs := &fakeSender{name: "store_bot", textErr: bot.ErrorForbidden}
	tokens := map[string]bool{}
	a.newBackupSender = func(token string) (backupSender, error) {
		tokens[token] = true
		return fs, nil
	}
	const tok = "1000000002:backup-bot-token-aaaaaaaaaaaaaaaaaaaaaaa"

	a.handleCallback(ctx, cb(100, "bk:dbot"))
	a.handleMessage(ctx, msgText(100, "не токен"))
	a.handleMessage(ctx, msgText(100, a.cfg.BotToken))
	if len(tokens) != 0 || !strings.Contains(fm.joined(), "токен этого же бота") {
		t.Fatalf("кривой токен или токен этого бота принят: %v %q", tokens, fm.joined())
	}
	// Админ ещё не запускал отдельного бота — тот не может ему написать.
	a.handleMessage(ctx, msgText(100, tok))
	if a.backupCfg().BotToken != "" || !strings.Contains(fm.joined(), "@store_bot не может вам написать") {
		t.Fatal("бот, который не может написать админу, принят")
	}
	fs.textErr = nil
	a.handleCallback(ctx, cb(100, "bk:dbot"))
	a.handleMessage(ctx, msgText(100, tok))
	if bc := a.backupCfg(); bc.BotToken != tok || bc.BotName != "store_bot" || !slices.Equal(fs.texts, []int64{100}) {
		t.Fatalf("отдельный бот не сохранён: %+v %v", a.backupCfg(), fs.texts)
	}
	if strings.Contains(fm.joined(), tok) {
		t.Fatal("токен попал в чат")
	}

	bkSetPassword(t, a, "пароль-бэкапа")
	a.handleCallback(ctx, cb(100, "bk:now"))
	if docCount(fm) != 0 || !slices.Equal(fs.files, []int64{100}) {
		t.Fatalf("файл ушёл не через отдельного бота: основной %d, отдельный %v", docCount(fm), fs.files)
	}

	a.handleCallback(ctx, cb(100, "bk:dnobot"))
	if a.backupCfg().BotToken != "" {
		t.Fatal("отдельный бот не отключён")
	}
}

// Ошибка отдельного бота не выдаёт его токен.
func TestRedactToken(t *testing.T) {
	base := errors.New("Post https://api.telegram.org/bot1:secret/sendMessage: timeout")
	err := redactToken(base, "1:secret")
	if strings.Contains(err.Error(), "secret") || !errors.Is(err, base) {
		t.Fatalf("redactToken: %v", err)
	}
}

func TestBackupDue(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	cases := []struct {
		name string
		bc   model.BackupConfig
		want bool
	}{
		{"выключено", model.BackupConfig{Hours: 0, Password: "p", LastAt: ""}, false},
		{"без пароля", model.BackupConfig{Hours: 1, LastAt: ""}, false},
		{"ни разу", model.BackupConfig{Hours: 1, Password: "p"}, true},
		{"рано", model.BackupConfig{Hours: 2, Password: "p", LastAt: at(-119 * time.Minute)}, false},
		{"ровно", model.BackupConfig{Hours: 2, Password: "p", LastAt: at(-2 * time.Hour)}, true},
		{"метка из будущего", model.BackupConfig{Hours: 24, Password: "p", LastAt: at(48 * time.Hour)}, true},
		{"битая метка", model.BackupConfig{Hours: 24, Password: "p", LastAt: "вчера"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := backupDue(tc.bc, now); got != tc.want {
				t.Fatalf("backupDue = %v, ждали %v", got, tc.want)
			}
		})
	}
}

// Во время восстановления бот не принимает оплату звёздами и отвечает
// «техработы», фоновые задачи и запросы не начинаются.
func TestBackupMaintenanceGate(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	a.restoring.Store(true)
	a.handle(ctx, nil, &models.Update{PreCheckoutQuery: &models.PreCheckoutQuery{ID: "pc", From: &models.User{ID: 1000000001}}})
	fm.mu.Lock()
	pre := slices.Clone(fm.preOK)
	fm.mu.Unlock()
	if !slices.Equal(pre, []bool{false}) {
		t.Fatalf("предпроверка оплаты звёздами во время восстановления: %v", pre)
	}
	a.handle(ctx, nil, &models.Update{Message: &models.Message{Text: "/start", From: &models.User{ID: 1000000001},
		Chat: models.Chat{ID: 1000000001, Type: models.ChatTypePrivate}}})
	if !strings.Contains(fm.joined(), "Техработы") {
		t.Fatal("пользователь не получил «техработы»")
	}
	n := len(fm.texts)
	a.handle(ctx, nil, &models.Update{CallbackQuery: cb(1000000001, "menu:buy")})
	if len(fm.texts) != n || !slices.ContainsFunc(fm.cbTexts, func(s string) bool { return strings.Contains(s, "Техработы") }) {
		t.Fatal("на нажатие кнопки — новое сообщение вместо всплывающей надписи")
	}
	// Оплата звёздами посреди замены — тревога админу.
	a.restorePhase.Store(phaseReplace)
	a.handle(ctx, nil, &models.Update{Message: &models.Message{From: &models.User{ID: 1000000001},
		Chat:              models.Chat{ID: 1000000001, Type: models.ChatTypePrivate},
		SuccessfulPayment: &models.SuccessfulPayment{TotalAmount: 50, TelegramPaymentChargeID: "ch-1"}}})
	if !strings.Contains(fm.joined(), "<code>ch-1</code>") {
		t.Fatal("оплата посреди замены не дошла до админа")
	}
	if a.EnterWork() {
		t.Fatal("веб-запрос начат во время восстановления")
	}
	if err := a.saveConfigOnly(ctx); err == nil {
		t.Fatal("конфиг сохраняется во время восстановления")
	}
}

// Восстановление ждёт начатые фоновые проходы и запросы, а не дождавшись —
// не трогает базу.
func TestBackupWaitQuiet(t *testing.T) {
	a, _, _ := bkTestApp(t)
	ctx := context.Background()
	a.quietWaitD = 300 * time.Millisecond
	if !a.enterWork() {
		t.Fatal("работа не началась")
	}
	a.restoring.Store(true)
	a.restorePhase.Store(phaseWait)
	if a.waitQuiet(ctx) {
		t.Fatal("восстановление не дождалось начатой работы")
	}
	a.leaveWork()
	if !a.waitQuiet(ctx) || a.restorePhase.Load() != phaseReplace {
		t.Fatal("восстановление не пошло, когда работа закончилась")
	}
}

// panelUsersStub — панель со списком пользователей.
func panelUsersStub(t *testing.T, users string) *remnawave.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/users" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"response":{"users":[` + users + `],"total":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{}}`))
	}))
	t.Cleanup(srv.Close)
	return remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

// После восстановления: пока сроки не сверены с панелью, автопродление и
// напоминания стоят; сверка переносит сроки из панели; оплаты, созданные
// до восстановления, сверки сами не выдают — уходят админу списком.
func TestRestoreGuardAfterRestart(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	for _, id := range []int64{1000000001, 1000000002} {
		if err := st.UpsertUser(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.SetSubExpiry(ctx, 1000000001, "2026-11-10T00:00:00Z", "paid")
	_ = st.SetSubExpiry(ctx, 1000000002, "2026-12-01T00:00:00Z", "paid")
	if err := st.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "yk-old",
		TelegramID: 1000000001, Months: 1, CreatedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	bkSetPassword(t, a, "пароль-бэкапа")
	a.handleCallback(ctx, cb(100, "bk:now"))
	_, file, _ := lastDoc(t, fm)
	restoreFile(t, a, fm, file)
	a.handleCallback(ctx, cb(100, "bk:apply"))
	if !a.RestartRequested() {
		t.Fatal("восстановление не прошло")
	}

	b, fm2 := rebootApp(t, st)
	if !b.restoreSyncPending() {
		t.Fatal("после перезапуска сроки считаются сверенными")
	}
	// Панель: первому продлили подписку после даты бэкапа, у второго
	// срок совпадает, доп-подписка первого не в счёт.
	b.panel = panelUsersStub(t, `{"uuid":"u1","username":"tg_1000000001","telegramId":1000000001,"expireAt":"2027-01-10T00:00:00.000Z"},`+
		`{"uuid":"u2","username":"tg_1000000002","telegramId":1000000002,"expireAt":"2026-12-01T00:00:00.000Z"},`+
		`{"uuid":"u3","username":"tg_1000000001_addsub","telegramId":1000000001,"tag":"CHILLBOT_ADD","expireAt":"2030-01-01T00:00:00Z"}`)
	b.guardTick(ctx, time.Now())
	if b.restoreSyncPending() {
		t.Fatal("сверка сроков не прошла")
	}
	u, _ := st.GetUser(ctx, 1000000001)
	if !sameMoment(u.SubExpireAt, "2027-01-10T00:00:00Z") {
		t.Fatalf("срок из панели не перенесён: %q", u.SubExpireAt)
	}
	if !b.restoreChanged(1000000001) || b.restoreChanged(1000000002) {
		t.Fatalf("список разошедшихся сроков: %v", b.restoreGuard().Changed)
	}
	if !strings.Contains(fm2.joined(), "Обновлено: 1") {
		t.Fatal("админ не узнал об итогах сверки")
	}

	// Счёт из восстановленной базы, найденный оплаченным, — не выдаётся.
	list, _ := st.ListUnresolvedPending(ctx, time.Now().UTC().Format(time.RFC3339), 10)
	if len(list) != 1 {
		t.Fatalf("незакрытые счета после восстановления: %d", len(list))
	}
	if b.reconcileFinalize(ctx, st, &list[0], "199.00 RUB") {
		t.Fatal("счёт из восстановленной базы выдан автоматически")
	}
	b.flushReview(ctx)
	if !strings.Contains(fm2.joined(), "Оплаты на ручную проверку") || !strings.Contains(fm2.joined(), "yk-old") {
		t.Fatal("счёт не пришёл админу на проверку")
	}
	if paid, _ := st.PaymentByExtID(ctx, "yk-old"); paid {
		t.Fatal("по счёту из окна записан платёж")
	}

	// Оплата звёздами до восстановления — в список один раз.
	b.botCfg.Stars.Enabled = true
	tx := models.StarTransaction{ID: "st-old", Amount: 50, Date: int(time.Now().Add(-time.Hour).Unix()),
		Source: &models.TransactionPartner{Type: models.TransactionPartnerTypeUser, User: &models.TransactionPartnerUser{
			User: models.User{ID: 1000000002}, InvoicePayload: "stars:1"}}}
	b.reconcileStarTx(ctx, &tx)
	b.flushReview(ctx)
	b.reconcileStarTx(ctx, &tx)
	b.flushReview(ctx)
	if strings.Count(fm2.joined(), "st-old") != 1 {
		t.Fatalf("оплата звёздами на проверку: %d раз", strings.Count(fm2.joined(), "st-old"))
	}
	if paid, _ := st.PaymentByExtID(ctx, "st-old"); paid {
		t.Fatal("оплата звёздами из окна выдана автоматически")
	}
}

package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
	"remnabot/internal/yookassa"
)

// fakeMsg перехватывает исходящие сообщения вместо реального Telegram.
type fakeMsg struct {
	mu       sync.Mutex
	texts    []string
	seq      int
	live     map[int]string // id -> текст активных (неудалённых) сообщений
	deleted  []int
	invoices []string // currency:amount:payload
}

func (f *fakeMsg) Send(_ context.Context, _ int64, text string) int { return f.add(text) }
func (f *fakeMsg) SendKB(_ context.Context, _ int64, text string, _ [][]models.InlineKeyboardButton) int {
	return f.add(text)
}
func (f *fakeMsg) AnswerCallback(_ context.Context, _ string) {}
func (f *fakeMsg) SendPhoto(_ context.Context, _ int64, _, caption string, _ [][]models.InlineKeyboardButton) int {
	return f.add(caption)
}
func (f *fakeMsg) SendBanner(_ context.Context, _ int64, _ models.InputFile, caption string, _ []models.MessageEntity, _ models.ReplyMarkup) int {
	return f.add(caption)
}
func (f *fakeMsg) Delete(_ context.Context, _ int64, id int) {
	f.mu.Lock()
	delete(f.live, id)
	f.deleted = append(f.deleted, id)
	f.mu.Unlock()
}
func (f *fakeMsg) RemoveKeyboard(_ context.Context, _ int64)               {}
func (f *fakeMsg) SetCommandKeyboard(_ context.Context, _ int64, _ string) {}
func (f *fakeMsg) SendInvoice(_ context.Context, _ int64, title, _, payload, currency string, amount int) {
	f.mu.Lock()
	f.invoices = append(f.invoices, currency+":"+strconv.Itoa(amount)+":"+payload)
	f.mu.Unlock()
}
func (f *fakeMsg) AnswerPreCheckout(_ context.Context, _ string, _ bool, _ string) {}
func (f *fakeMsg) add(s string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, s)
	f.seq++
	if f.live == nil {
		f.live = map[int]string{}
	}
	f.live[f.seq] = s
	return f.seq
}
func (f *fakeMsg) liveCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.live) }
func (f *fakeMsg) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.texts) == 0 {
		return ""
	}
	return f.texts[len(f.texts)-1]
}
func (f *fakeMsg) joined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.texts, "\n---\n")
}

// fakeStore — хранилище в памяти (реализует storage.Storage без БД).
type fakeStore struct {
	cfg   *model.BotConfig
	users map[int64]*model.User
	reqs  map[int64]*model.P2PRequest
	pays  map[int64]*model.Payment
	media map[string]string
	seq   int64
}

func (s *fakeStore) Migrate(context.Context) error { return nil }
func (s *fakeStore) LoadConfig(context.Context) (*model.BotConfig, bool, error) {
	if s.cfg == nil {
		return nil, false, nil
	}
	cp := *s.cfg
	return &cp, true, nil
}
func (s *fakeStore) SaveConfig(_ context.Context, c *model.BotConfig) error {
	cp := *c
	s.cfg = &cp
	return nil
}
func (s *fakeStore) UpsertUser(_ context.Context, id int64) error {
	if s.users == nil {
		s.users = map[int64]*model.User{}
	}
	if s.users[id] == nil {
		s.users[id] = &model.User{TelegramID: id}
	}
	return nil
}
func (s *fakeStore) GetUser(_ context.Context, id int64) (*model.User, error) {
	if s.users == nil || s.users[id] == nil {
		return nil, nil
	}
	cp := *s.users[id]
	return &cp, nil
}

func (s *fakeStore) SetUserInfo(_ context.Context, id int64, username, firstName string) error {
	if s.users == nil || s.users[id] == nil {
		return nil // обновляем только существующую запись
	}
	s.users[id].Username = username
	s.users[id].FirstName = firstName
	return nil
}

func (s *fakeStore) HasApprovedPurchase(_ context.Context, id int64) (bool, error) {
	for _, r := range s.reqs {
		if r.TelegramID == id && r.Status == model.P2PApproved {
			return true, nil
		}
	}
	return false, nil
}
func (s *fakeStore) AddPayment(_ context.Context, p *model.Payment) error {
	if s.pays == nil {
		s.pays = map[int64]*model.Payment{}
	}
	if p.ID == 0 {
		s.seq++
		p.ID = s.seq
	}
	cp := *p
	s.pays[p.ID] = &cp
	return nil
}
func (s *fakeStore) ListPayments(_ context.Context, limit, offset int) ([]model.Payment, int, error) {
	var all []model.Payment
	for _, p := range s.pays {
		all = append(all, *p)
	}
	total := len(all)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], total, nil
}
func (s *fakeStore) HasPaidPayment(_ context.Context, id int64) (bool, error) {
	for _, p := range s.pays {
		if p.TelegramID == id && p.Status == model.PaymentPaid {
			return true, nil
		}
	}
	return false, nil
}
func (s *fakeStore) PaymentByExtID(_ context.Context, extID string) (bool, error) {
	if extID == "" {
		return false, nil
	}
	for _, p := range s.pays {
		if p.ExtID == extID {
			return true, nil
		}
	}
	return false, nil
}
func (s *fakeStore) SetP2PApproved(_ context.Context, id int64, ok bool) error {
	if s.users == nil {
		s.users = map[int64]*model.User{}
	}
	if s.users[id] == nil {
		s.users[id] = &model.User{TelegramID: id}
	}
	s.users[id].P2PApproved = ok
	return nil
}

func (s *fakeStore) ListUsers(_ context.Context, limit, offset int) ([]model.User, int, error) {
	var all []model.User
	for _, u := range s.users {
		all = append(all, *u)
	}
	total := len(all)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], total, nil
}
func (s *fakeStore) SetBlocked(_ context.Context, id int64, blocked bool) error {
	if s.users == nil {
		s.users = map[int64]*model.User{}
	}
	if s.users[id] == nil {
		s.users[id] = &model.User{TelegramID: id}
	}
	s.users[id].Blocked = blocked
	return nil
}
func (s *fakeStore) DeleteUser(_ context.Context, id int64) error {
	delete(s.users, id)
	return nil
}
func (s *fakeStore) CreateP2PRequest(_ context.Context, r *model.P2PRequest) error {
	if s.reqs == nil {
		s.reqs = map[int64]*model.P2PRequest{}
	}
	if r.ID == 0 {
		s.seq++
		r.ID = s.seq
	}
	cp := *r
	s.reqs[r.ID] = &cp
	return nil
}
func (s *fakeStore) GetP2PRequest(_ context.Context, id int64) (*model.P2PRequest, error) {
	if s.reqs == nil || s.reqs[id] == nil {
		return nil, nil
	}
	cp := *s.reqs[id]
	return &cp, nil
}
func (s *fakeStore) UpdateP2PRequest(_ context.Context, r *model.P2PRequest) error {
	if s.reqs == nil {
		s.reqs = map[int64]*model.P2PRequest{}
	}
	cp := *r
	s.reqs[r.ID] = &cp
	return nil
}
func (s *fakeStore) LoadMediaFileID(_ context.Context, section string) (string, bool, error) {
	if s.media == nil {
		return "", false, nil
	}
	id, ok := s.media[section]
	return id, ok, nil
}

func (s *fakeStore) SaveMediaFileID(_ context.Context, section, fileID string) error {
	if s.media == nil {
		s.media = map[string]string{}
	}
	s.media[section] = fileID
	return nil
}

func (s *fakeStore) Kind() string { return "fake" }
func (s *fakeStore) Close() error { return nil }

func newTestApp(t *testing.T) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg: &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg: fm,
		wiz: map[int64]*wizard{},
	}
	a.newStore = func(kind, dsn string) (storage.Storage, error) { return fs, nil }
	return a, fm, fs
}

func msgText(uid int64, text string) *models.Message {
	return &models.Message{Text: text, From: &models.User{ID: uid}, Chat: models.Chat{ID: uid}}
}
func cb(uid int64, data string) *models.CallbackQuery {
	return &models.CallbackQuery{ID: "cbid", Data: data, From: models.User{ID: uid}}
}

// panelStub — заглушка REST API панели Remnawave.
func panelStub(users int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/system/stats") {
			_, _ = w.Write([]byte(`{"response":{"users":{"totalUsers":` + itoa(users) + `}}}`))
			return
		}
		w.WriteHeader(http.StatusOK) // health
	}))
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Полный сценарий: удалённая панель, официальная установка (Caddy), /api открыт.
func TestWizard_RemoteDocs_HappyPath(t *testing.T) {
	srv := panelStub(7)
	defer srv.Close()
	a, fm, fs := newTestApp(t)
	ctx := context.Background()

	a.handleMessage(ctx, msgText(100, "/start"))
	a.handleCallback(ctx, cb(100, "lang:ru"))
	a.handleCallback(ctx, cb(100, "db:sqlite"))
	if a.store == nil {
		t.Fatalf("после выбора SQLite store не открыт; лог:\n%s", fm.joined())
	}
	a.handleCallback(ctx, cb(100, "loc:remote"))
	a.handleCallback(ctx, cb(100, "inst:docs"))
	a.handleMessage(ctx, msgText(100, srv.URL))
	a.handleMessage(ctx, msgText(100, "api-token-xyz"))
	a.handleCallback(ctx, cb(100, "apiprot:no"))

	if !a.installed() {
		t.Fatalf("бот не установлен в конце; лог:\n%s", fm.joined())
	}
	if fs.cfg == nil || fs.cfg.Panel.APIToken != "api-token-xyz" {
		t.Fatalf("конфиг сохранён неверно: %+v", fs.cfg)
	}
	if fs.cfg.Panel.Mode != model.ModeRemote || fs.cfg.Language != "ru" {
		t.Fatalf("конфиг: mode=%q lang=%q", fs.cfg.Panel.Mode, fs.cfg.Language)
	}
	if !strings.Contains(fm.last(), "7") {
		t.Fatalf("в финальном сообщении нет числа пользователей: %q", fm.last())
	}
}

// Локальный режим: способ установки и кука пропускаются, URL подставляется сам.
func TestWizard_Local_SkipsInstallAndCookie(t *testing.T) {
	srv := panelStub(3)
	defer srv.Close()
	a, fm, fs := newTestApp(t)
	ctx := context.Background()

	a.handleMessage(ctx, msgText(100, "/start"))
	a.handleCallback(ctx, cb(100, "lang:en"))
	a.handleCallback(ctx, cb(100, "db:sqlite"))
	a.handleCallback(ctx, cb(100, "loc:local"))
	// локальный режим форсит base http://remnawave:3000 — verify не достучится,
	// поэтому проверяем шаг (запрос токена), а не успешную установку.
	a.handleMessage(ctx, msgText(100, "tok"))

	w := a.wiz[100]
	if w == nil {
		t.Fatalf("состояние мастера пропало; лог:\n%s", fm.joined())
	}
	if w.cfg.Panel.Mode != model.ModeLocal || w.cfg.Panel.BaseURL == "" {
		t.Fatalf("локальный режим не выставлен: %+v", w.cfg.Panel)
	}
	// токен ввели → verify запустился и упал на health (нет связи) → не установлен
	if a.installed() {
		t.Fatal("в локальном тесте установка не должна завершиться (панель недостижима)")
	}
	_ = fs
}

// Удалённо + eGames: после токена мастер просит куку.
func TestWizard_RemoteEGames_AsksCookie(t *testing.T) {
	a, fm, _ := newTestApp(t)
	ctx := context.Background()

	a.handleMessage(ctx, msgText(100, "/start"))
	a.handleCallback(ctx, cb(100, "lang:ru"))
	a.handleCallback(ctx, cb(100, "db:sqlite"))
	a.handleCallback(ctx, cb(100, "loc:remote"))
	a.handleCallback(ctx, cb(100, "inst:egames"))
	a.handleMessage(ctx, msgText(100, "https://panel.example.com"))
	a.handleMessage(ctx, msgText(100, "token"))

	if w := a.wiz[100]; w == nil || w.step != stepCookie {
		t.Fatalf("ожидался шаг ввода куки; лог:\n%s", fm.joined())
	}
	if !strings.Contains(fm.last(), "nginx.conf") {
		t.Fatalf("в подсказке про куку нет nginx.conf: %q", fm.last())
	}
}

// Postgres без docker.sock (ctl=nil) и без env DSN → запрос DSN вручную.
func TestWizard_PostgresNoDocker_AsksDSN(t *testing.T) {
	a, fm, _ := newTestApp(t)
	ctx := context.Background()

	a.handleMessage(ctx, msgText(100, "/start"))
	a.handleCallback(ctx, cb(100, "lang:ru"))
	a.handleCallback(ctx, cb(100, "db:postgres"))

	if w := a.wiz[100]; w == nil || w.step != stepPGDSN {
		t.Fatalf("ожидался запрос DSN PostgreSQL; лог:\n%s", fm.joined())
	}
}

// Не-админ не может запустить мастер.
func TestNonAdminIgnored(t *testing.T) {
	a, fm, _ := newTestApp(t)
	a.handleMessage(context.Background(), msgText(999, "/start"))
	if _, ok := a.wiz[999]; ok {
		t.Fatal("мастер не должен стартовать для не-админа")
	}
	if !strings.Contains(fm.last(), "админ") && !strings.Contains(strings.ToLower(fm.last()), "admin") {
		t.Fatalf("ожидался отказ не-админу: %q", fm.last())
	}
}

func photoMsg(uid int64, fileID string) *models.Message {
	return &models.Message{From: &models.User{ID: uid}, Chat: models.Chat{ID: uid}, Photo: []models.PhotoSize{{FileID: fileID}}}
}

func TestP2P_FullFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/abc"}}`))
	}))
	defer srv.Close()

	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		wiz:   map[int64]*wizard{},
		ui:    map[int64]*uiState{},
		store: fs,
	}
	a.botCfg = &model.BotConfig{
		Installed: true, Language: "ru",
		P2P: model.P2PConfig{Enabled: true, Cards: []string{"CARD-1"}, Prices: map[int]string{1: "100"}, SquadUUID: "sq1"},
	}
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	ctx := context.Background()
	const user int64 = 555

	// 1) выбор плана и метода -> гейт доступа (юзер ещё не одобрен)
	a.handleCallback(ctx, cb(user, "buy:1"))
	a.handleCallback(ctx, cb(user, "method:p2p"))
	if u, _ := fs.GetUser(ctx, user); u != nil && u.P2PApproved {
		t.Fatal("на этом шаге юзер не должен быть одобрен")
	}

	// 2) админ одобряет доступ
	a.handleCallback(ctx, cb(100, "adm:uok:555"))
	if u, _ := fs.GetUser(ctx, user); u == nil || !u.P2PApproved {
		t.Fatal("админ должен был одобрить доступ")
	}

	// 3) повторный выбор -> выдаётся карта + создаётся заявка
	a.handleCallback(ctx, cb(user, "buy:1"))
	a.handleCallback(ctx, cb(user, "method:p2p"))
	if !strings.Contains(fm.joined(), "CARD-1") {
		t.Fatalf("карта не выдана:\n%s", fm.joined())
	}
	var reqID int64
	for id := range fs.reqs {
		if id > reqID {
			reqID = id
		}
	}
	if reqID == 0 {
		t.Fatal("заявка не создана")
	}

	// 4) «я оплатил» + скриншот
	a.handleCallback(ctx, cb(user, "p2p:paid:"+strconv.FormatInt(reqID, 10)))
	a.handlePhoto(ctx, photoMsg(user, "file_123"))
	if r, _ := fs.GetP2PRequest(ctx, reqID); r == nil || r.Status != model.P2PSubmitted || r.Screenshot != "file_123" {
		t.Fatalf("скриншот не сохранён: %+v", r)
	}

	// 5) админ подтверждает -> провижн в панель + ссылка юзеру
	a.handleCallback(ctx, cb(100, "adm:pok:"+strconv.FormatInt(reqID, 10)))
	if r, _ := fs.GetP2PRequest(ctx, reqID); r == nil || r.Status != model.P2PApproved {
		t.Fatalf("оплата не подтверждена: %+v", r)
	}
	if !strings.Contains(fm.joined(), "sub/abc") {
		t.Fatalf("юзеру не отправлена ссылка:\n%s", fm.joined())
	}
}

// Сценарий админки «Пользователи»: блокировка ограничивает доступ, удаление чистит запись.
func TestUsersAdmin_BlockEnforceDelete(t *testing.T) {
	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		wiz:   map[int64]*wizard{},
		ui:    map[int64]*uiState{},
		store: fs,
	}
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	const user int64 = 555

	// юзер регистрируется
	_ = fs.UpsertUser(ctx, user)

	// админ блокирует
	a.handleCallback(ctx, cb(100, "usr:block:555"))
	if u, _ := fs.GetUser(ctx, user); u == nil || !u.Blocked {
		t.Fatalf("юзер должен быть заблокирован: %+v", u)
	}

	// заблокированный юзер пишет /start -> получает отказ, меню не показывается
	fm.texts = nil
	a.handleMessage(ctx, msgText(user, "/start"))
	if !strings.Contains(fm.joined(), "ограничен") {
		t.Fatalf("заблокированному должен прийти отказ, got:\n%s", fm.joined())
	}

	// заблокированный юзер жмёт кнопку покупки -> тоже отказ (callback)
	fm.texts = nil
	a.handleCallback(ctx, cb(user, "menu:buy"))
	if !strings.Contains(fm.joined(), "ограничен") {
		t.Fatalf("callback заблокированного должен быть отклонён, got:\n%s", fm.joined())
	}

	// админ разблокирует
	a.handleCallback(ctx, cb(100, "usr:unblock:555"))
	if u, _ := fs.GetUser(ctx, user); u == nil || u.Blocked {
		t.Fatalf("юзер должен быть разблокирован: %+v", u)
	}

	// админ удаляет
	a.handleCallback(ctx, cb(100, "usr:delc:555"))
	if u, _ := fs.GetUser(ctx, user); u != nil {
		t.Fatal("после удаления записи быть не должно")
	}
}

// Жёсткий запрет: блокировка/удаление в боте НЕ трогают аккаунт в панели.
// Здесь проверяем, что DeleteUser не вызывает никаких обращений к панели.
func TestUsersAdmin_DeleteDoesNotTouchPanel(t *testing.T) {
	var panelHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panelHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		wiz:   map[int64]*wizard{},
		ui:    map[int64]*uiState{},
		store: fs,
	}
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	ctx := context.Background()

	_ = fs.UpsertUser(ctx, 555)
	a.handleCallback(ctx, cb(100, "usr:block:555"))
	a.handleCallback(ctx, cb(100, "usr:delc:555"))

	if panelHits != 0 {
		t.Fatalf("блок/удаление в боте не должны обращаться к панели, hits=%d", panelHits)
	}
}

// Single-message UI: при навигации бот удаляет предыдущий экран,
// а кросс-чат уведомления (модерация) НЕ удаляются.
func TestSingleMessageUI(t *testing.T) {
	srv := panelStub(5)
	defer srv.Close()
	a, fm, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()

	// админ открывает меню -> экран #1
	a.handleMessage(ctx, msgText(100, "/start"))
	afterStart := fm.liveCount()
	if afterStart == 0 {
		t.Fatalf("после /start должно быть видимое сообщение, live=%d", afterStart)
	}
	// переход в «Управление» -> прошлый экран удалён, остаётся текущий
	a.handleCallback(ctx, cb(100, "menu:manage"))
	// ещё переход
	a.handleCallback(ctx, cb(100, "menu:home"))
	if got := fm.liveCount(); got != afterStart {
		t.Fatalf("на экране должно оставаться только текущее (%d), а живых=%d; deleted=%v", afterStart, got, fm.deleted)
	}
	if len(fm.deleted) == 0 {
		t.Fatal("предыдущие экраны должны были удаляться")
	}
}

// Уведомление админу о заявке не должно удаляться при навигации пользователя.
func TestNotificationsArePersistent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		wiz:   map[int64]*wizard{},
		ui:    map[int64]*uiState{},
		store: fs,
	}
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru", P2P: model.P2PConfig{Enabled: true, Prices: map[int]string{1: "100"}}}
	ctx := context.Background()
	const user int64 = 555

	// пользователь просит доступ -> админу уходит постоянное уведомление
	a.handleCallback(ctx, cb(user, "buy:1"))
	a.handleCallback(ctx, cb(user, "method:p2p"))
	if !strings.Contains(fm.joined(), "просит доступ") {
		t.Fatalf("админу не пришло уведомление о запросе:\n%s", fm.joined())
	}
	notifDeleted := len(fm.deleted)

	// пользователь продолжает навигацию — это не должно удалять уведомление админу
	a.handleCallback(ctx, cb(user, "buy:1"))
	a.handleCallback(ctx, cb(user, "buy:1"))
	if len(fm.deleted) > notifDeleted {
		// допускается удаление экранов ПОЛЬЗОВАТЕЛЯ, но уведомление админу должно жить
	}
	// уведомление админу (его текст) всё ещё среди живых
	foundLive := false
	fm.mu.Lock()
	for _, txt := range fm.live {
		if strings.Contains(txt, "просит доступ") {
			foundLive = true
		}
	}
	fm.mu.Unlock()
	if !foundLive {
		t.Fatal("уведомление админу не должно удаляться при навигации пользователя")
	}
}

func TestUserLabel(t *testing.T) {
	cases := []struct {
		u    model.User
		want string
	}{
		{model.User{TelegramID: 6882779276, Username: "vasya"}, "@vasya (6882779276)"},
		{model.User{TelegramID: 7, FirstName: "Вася"}, "Вася (7)"},
		{model.User{TelegramID: 42}, "42"},
	}
	for _, c := range cases {
		if got := userLabel(&c.u); got != c.want {
			t.Fatalf("userLabel(%+v)=%q want %q", c.u, got, c.want)
		}
	}
}

func btnData(rows []models.InlineKeyboardButton) string {
	var b strings.Builder
	for _, x := range rows {
		b.WriteString(x.CallbackData + "|")
	}
	return b.String()
}

func TestNavRow(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	const user int64 = 555

	// админ -> только Главная
	if row := a.navRow(ctx, 100, true); len(row) != 1 || row[0].CallbackData != "menu:home" {
		t.Fatalf("админ должен иметь только Главную: %s", btnData(row))
	}
	// юзер без подписки -> Главная + Купить
	if row := a.navRow(ctx, user, false); btnData(row) != "menu:home|menu:buy|" {
		t.Fatalf("юзер без подписки: %s", btnData(row))
	}
	// после одобренной покупки -> Главная + Мои подписки
	_ = fs.CreateP2PRequest(ctx, &model.P2PRequest{TelegramID: user, Status: model.P2PApproved})
	if row := a.navRow(ctx, user, false); btnData(row) != "menu:home|menu:mysubs|" {
		t.Fatalf("юзер с подпиской: %s", btnData(row))
	}
}

// Кнопка «Разрешить P2P» в карточке: выдаёт доступ и уведомляет пользователя.
func TestUserCard_AllowP2P(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	const user int64 = 555
	_ = fs.UpsertUser(ctx, user)

	a.handleCallback(ctx, cb(100, "usr:p2pon:555"))
	if u, _ := fs.GetUser(ctx, user); u == nil || !u.P2PApproved {
		t.Fatalf("P2P-доступ должен быть выдан: %+v", u)
	}
	if !strings.Contains(fm.joined(), "одобрена оплата переводом") {
		t.Fatalf("пользователь должен получить уведомление:\n%s", fm.joined())
	}
	// запрет обратно
	a.handleCallback(ctx, cb(100, "usr:p2poff:555"))
	if u, _ := fs.GetUser(ctx, user); u == nil || u.P2PApproved {
		t.Fatalf("P2P-доступ должен быть снят: %+v", u)
	}
}

func successPayMsg(uid int64, payload string, amount int) *models.Message {
	return &models.Message{
		From: &models.User{ID: uid}, Chat: models.Chat{ID: uid},
		SuccessfulPayment: &models.SuccessfulPayment{Currency: "XTR", TotalAmount: amount, InvoicePayload: payload},
	}
}
func cbMsg(uid int64, data string, msgID int) *models.CallbackQuery {
	return &models.CallbackQuery{ID: "cbid", Data: data, From: models.User{ID: uid},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: msgID}}}
}

// Полный флоу Telegram Stars: выбор метода -> инвойс -> precheckout -> оплата -> провижн + лог + ссылка.
func TestStarsFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/abc"}}`))
	}))
	defer srv.Close()

	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg: &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg: fm, wiz: map[int64]*wizard{}, ui: map[int64]*uiState{}, store: fs,
	}
	a.botCfg = &model.BotConfig{
		Installed: true, Language: "ru",
		Stars: model.StarsConfig{Enabled: true, Prices: map[int]int{1: 100}},
	}
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	ctx := context.Background()
	const user int64 = 555

	a.handleCallback(ctx, cb(user, "buy:1"))
	a.handleCallback(ctx, cb(user, "method:stars"))
	if len(fm.invoices) != 1 || fm.invoices[0] != "XTR:100:stars:1" {
		t.Fatalf("инвойс не выставлен корректно: %v", fm.invoices)
	}
	// precheckout (должен ответить ok без паники)
	a.handlePreCheckout(ctx, &models.PreCheckoutQuery{ID: "pc1", InvoicePayload: "stars:1"})
	// успешная оплата
	a.handleSuccessfulPayment(ctx, successPayMsg(user, "stars:1", 100))

	if !strings.Contains(fm.joined(), "sub/abc") {
		t.Fatalf("после оплаты не пришла ссылка:\n%s", fm.joined())
	}
	if ok, _ := fs.HasPaidPayment(ctx, user); !ok {
		t.Fatal("оплата не записана в лог")
	}
	// после покупки nav показывает «Мои подписки»
	if row := a.navRow(ctx, user, false); btnData(row) != "menu:home|menu:mysubs|" {
		t.Fatalf("после Stars-оплаты ожидались Мои подписки: %s", btnData(row))
	}
}

// Уведомление-заявка удаляется после решения админа.
func TestModerationNotificationDeleted(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru", P2P: model.P2PConfig{Enabled: true, Prices: map[int]string{1: "100"}}}
	ctx := context.Background()

	// одобряем доступ пользователю по уведомлению с msgID=777
	const notifID = 777
	a.handleCallback(ctx, cbMsg(100, "adm:uok:555", notifID))
	found := false
	for _, id := range fm.deleted {
		if id == notifID {
			found = true
		}
	}
	if !found {
		t.Fatalf("уведомление (msgID=%d) должно быть удалено; deleted=%v", notifID, fm.deleted)
	}
	if u, _ := fs.GetUser(ctx, 555); u == nil || !u.P2PApproved {
		t.Fatal("доступ должен быть выдан")
	}
}

func TestPricingResolver(t *testing.T) {
	pr := model.Pricing{
		Currency: "руб",
		Base:     map[int]string{1: "150", 3: "400"},
		P2P:      map[int]string{1: "140"}, // переопределение P2P для 1 мес
		YooKassa: map[int]string{},         // нет переопределения — берётся база
		Stars:    map[int]int{1: 100},
	}
	if got := pr.Fiat(model.PayMethodP2P, 1); got != "140" {
		t.Fatalf("P2P override 1мес = %q, want 140", got)
	}
	if got := pr.Fiat(model.PayMethodP2P, 3); got != "400" {
		t.Fatalf("P2P fallback to base 3мес = %q, want 400", got)
	}
	if got := pr.Fiat(model.PayMethodYooKassa, 1); got != "150" {
		t.Fatalf("YK fallback to base 1мес = %q, want 150", got)
	}
	if got := pr.StarPrice(1); got != 100 {
		t.Fatalf("stars 1мес = %d, want 100", got)
	}
}

// Флоу ЮKassa: выбор метода -> кнопка оплаты -> проверка статуса -> провижн + лог.
func TestYooKassaFlow(t *testing.T) {
	// стуб панели Remnawave
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/yk"}}`))
	}))
	defer panel.Close()

	// стуб API ЮKassa
	yk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"pay_42","status":"pending","confirmation":{"confirmation_url":"https://yoo/p/42"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"pay_42","status":"succeeded","amount":{"value":"150.00","currency":"RUB"},"metadata":{"months":"1","telegram_id":"555"}}`))
	}))
	defer yk.Close()
	oldBase := yookassa.BaseURL
	yookassa.BaseURL = yk.URL
	defer func() { yookassa.BaseURL = oldBase }()

	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg: &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg: fm, wiz: map[int64]*wizard{}, ui: map[int64]*uiState{}, store: fs,
	}
	a.botCfg = &model.BotConfig{
		Installed: true, Language: "ru",
		YooKassa: model.YooKassaConfig{Enabled: true, ShopID: "shop", SecretKey: "sec", ReturnURL: "https://t.me"},
		Pricing:  model.Pricing{Currency: "руб", Base: map[int]string{1: "150"}},
	}
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panel.URL, APIToken: "t"})
	ctx := context.Background()
	const user int64 = 555

	a.handleCallback(ctx, cb(user, "buy:1"))
	a.handleCallback(ctx, cb(user, "method:yk"))
	if !strings.Contains(fm.joined(), "Оплат") {
		t.Fatalf("не показан запрос на оплату:\n%s", fm.joined())
	}
	// проверка оплаты -> succeeded -> провижн
	a.handleCallback(ctx, cb(user, "ykc:pay_42"))
	if !strings.Contains(fm.joined(), "sub/yk") {
		t.Fatalf("после успешной оплаты нет ссылки:\n%s", fm.joined())
	}
	if ok, _ := fs.HasPaidPayment(ctx, user); !ok {
		t.Fatal("оплата ЮKassa не записана в лог")
	}
	// идемпотентность: повторная проверка не создаёт второй платёж
	before := len(fs.pays)
	a.handleCallback(ctx, cb(user, "ykc:pay_42"))
	if len(fs.pays) != before {
		t.Fatalf("повторная проверка не должна создавать новый платёж: было %d стало %d", before, len(fs.pays))
	}
}

// Нажатие постоянной reply-кнопки «Главная» открывает меню (как /start).
func TestHomeReplyButton(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	// админ жмёт reply-кнопку «🏠 Главная»
	a.handleMessage(ctx, msgText(100, "🏠 Главная"))
	if fm.joined() == "" {
		t.Fatal("по кнопке «Главная» бот ничего не показал")
	}
	// входящее сообщение пользователя удаляется (чистота чата)
	if len(fm.deleted) == 0 {
		t.Fatal("сообщение-нажатие «Главная» должно удаляться")
	}
}

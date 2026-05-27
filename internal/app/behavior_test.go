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
)

// fakeMsg перехватывает исходящие сообщения вместо реального Telegram.
type fakeMsg struct {
	mu    sync.Mutex
	texts []string
}

func (f *fakeMsg) Send(_ context.Context, _ int64, text string) { f.add(text) }
func (f *fakeMsg) SendKB(_ context.Context, _ int64, text string, _ [][]models.InlineKeyboardButton) {
	f.add(text)
}
func (f *fakeMsg) AnswerCallback(_ context.Context, _ string) {}
func (f *fakeMsg) SendPhoto(_ context.Context, _ int64, _, caption string, _ [][]models.InlineKeyboardButton) {
	f.add(caption)
}
func (f *fakeMsg) add(s string) { f.mu.Lock(); f.texts = append(f.texts, s); f.mu.Unlock() }
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

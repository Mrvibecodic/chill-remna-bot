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
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
	"remnabot/internal/yookassa"
)

type fakeMsg struct {
	mu       sync.Mutex
	texts    []string
	seq      int
	live     map[int]string
	deleted  []int
	invoices []string
}

func (f *fakeMsg) Send(_ context.Context, _ int64, text string) int { return f.add(text) }
func (f *fakeMsg) SendKB(_ context.Context, _ int64, text string, _ [][]models.InlineKeyboardButton) int {
	return f.add(text)
}
func (f *fakeMsg) AnswerCallback(_ context.Context, _ string) {}
func (f *fakeMsg) SendPhoto(_ context.Context, _ int64, _, caption string, _ [][]models.InlineKeyboardButton) int {
	return f.add(caption)
}
func (f *fakeMsg) SendPhotoCacheable(_ context.Context, _ int64, _ string, _ []byte, _, caption string, _ [][]models.InlineKeyboardButton) (int, string) {
	return f.add(caption), ""
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

type fakeStore struct {
	cfg     *model.BotConfig
	users   map[int64]*model.User
	reqs    map[int64]*model.P2PRequest
	pays    map[int64]*model.Payment
	media   map[string]string
	pending map[int64]*model.PendingInvoice
	seq     int64
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
		return nil
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

	if p.ExtID != "" {
		for _, x := range s.pays {
			if x.Method == p.Method && x.ExtID == p.ExtID {
				return storage.ErrDuplicateExtID
			}
		}
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
func (s *fakeStore) PaidPayments(_ context.Context) ([]model.Payment, error) {
	var out []model.Payment
	for _, p := range s.pays {
		if p.Status == model.PaymentPaid {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (s *fakeStore) MostPopularPlan(_ context.Context) (int, int, error) {
	counts := map[int]int{}
	total := 0
	for _, p := range s.pays {
		if p.Status == model.PaymentPaid {
			counts[p.Months]++
			total++
		}
	}
	best := 0
	bestN := 0
	for mo, n := range counts {
		if n > bestN || (n == bestN && (best == 0 || mo < best)) {
			best = mo
			bestN = n
		}
	}
	return best, total, nil
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

func (s *fakeStore) DeletePaymentsByUser(_ context.Context, id int64) error {
	for k, p := range s.pays {
		if p.TelegramID == id {
			delete(s.pays, k)
		}
	}
	return nil
}

func (s *fakeStore) DeleteP2PRequestsByUser(_ context.Context, id int64) error {
	for k, r := range s.reqs {
		if r.TelegramID == id {
			delete(s.reqs, k)
		}
	}
	return nil
}

func (s *fakeStore) SetTermsAccepted(_ context.Context, telegramID int64, ts string) error {
	if u, ok := s.users[telegramID]; ok {
		u.TermsAcceptedAt = ts
	}
	return nil
}

func (s *fakeStore) SetTrialUsed(_ context.Context, telegramID int64, ts string) error {
	if u, ok := s.users[telegramID]; ok {
		u.TrialUsedAt = ts
	}
	return nil
}
func (s *fakeStore) SetSubExpiry(_ context.Context, telegramID int64, expireAt, kind string) error {
	if s.users == nil {
		s.users = map[int64]*model.User{}
	}
	if s.users[telegramID] == nil {
		s.users[telegramID] = &model.User{TelegramID: telegramID}
	}
	s.users[telegramID].SubExpireAt = expireAt
	s.users[telegramID].NotifyKind = kind
	s.users[telegramID].NotifySent = ""
	return nil
}
func (s *fakeStore) MarkNotified(_ context.Context, telegramID int64, sentCSV string) error {
	if u, ok := s.users[telegramID]; ok {
		u.NotifySent = sentCSV
	}
	return nil
}
func (s *fakeStore) UsersForNotify(_ context.Context) ([]model.User, error) {
	var out []model.User
	for _, u := range s.users {
		if u.SubExpireAt != "" {
			out = append(out, *u)
		}
	}
	return out, nil
}
func (s *fakeStore) AddBalance(_ context.Context, id int64, kopecks int64) error {
	if s.users == nil {
		s.users = map[int64]*model.User{}
	}
	if s.users[id] == nil {
		s.users[id] = &model.User{TelegramID: id}
	}
	s.users[id].Balance += kopecks
	return nil
}
func (s *fakeStore) DeductBalance(_ context.Context, id int64, kopecks int64) (bool, error) {
	u := s.users[id]
	if u == nil || u.Balance < kopecks || kopecks <= 0 {
		return false, nil
	}
	u.Balance -= kopecks
	return true, nil
}
func (s *fakeStore) SetReferredBy(_ context.Context, id, ref int64) error {
	if s.users == nil {
		s.users = map[int64]*model.User{}
	}
	if s.users[id] == nil {
		s.users[id] = &model.User{TelegramID: id}
	}
	if s.users[id].ReferredBy == 0 {
		s.users[id].ReferredBy = ref
	}
	return nil
}
func (s *fakeStore) SetRefBonusPaid(_ context.Context, id int64) error {
	if s.users != nil && s.users[id] != nil {
		s.users[id].RefBonusPaid = true
	}
	return nil
}
func (s *fakeStore) CountReferrals(_ context.Context, ref int64) (int, error) {
	n := 0
	for _, u := range s.users {
		if u.ReferredBy == ref {
			n++
		}
	}
	return n, nil
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

func (s *fakeStore) DeleteMediaFileID(_ context.Context, section string) error {
	if s.media != nil {
		delete(s.media, section)
	}
	return nil
}

func (s *fakeStore) AddPendingInvoice(_ context.Context, p *model.PendingInvoice) error {
	if s.pending == nil {
		s.pending = map[int64]*model.PendingInvoice{}
	}
	if p.ID == 0 {
		s.seq++
		p.ID = s.seq
	}
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	cp := *p
	s.pending[p.ID] = &cp
	return nil
}
func (s *fakeStore) ListUnresolvedPending(_ context.Context, createdBefore string, limit int) ([]model.PendingInvoice, error) {
	var out []model.PendingInvoice
	for _, p := range s.pending {
		if !p.Resolved && p.CreatedAt <= createdBefore {
			out = append(out, *p)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}
func (s *fakeStore) ResolvePending(_ context.Context, id int64) error {
	if p, ok := s.pending[id]; ok {
		p.Resolved = true
	}
	return nil
}
func (s *fakeStore) PendingByExtID(_ context.Context, extID string) (*model.PendingInvoice, error) {
	if extID == "" {
		return nil, nil
	}
	for _, p := range s.pending {
		if p.ExtID == extID {
			cp := *p
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *fakeStore) Export(context.Context) (*storage.Snapshot, error) {
	return &storage.Snapshot{}, nil
}
func (s *fakeStore) Import(context.Context, *storage.Snapshot) error { return nil }

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

func panelStub(users int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/system/stats") {
			_, _ = w.Write([]byte(`{"response":{"users":{"totalUsers":` + itoa(users) + `}}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
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

func TestWizard_Local_SkipsInstallAndCookie(t *testing.T) {
	srv := panelStub(3)
	defer srv.Close()
	a, fm, fs := newTestApp(t)
	ctx := context.Background()

	a.handleMessage(ctx, msgText(100, "/start"))
	a.handleCallback(ctx, cb(100, "lang:en"))
	a.handleCallback(ctx, cb(100, "db:sqlite"))
	a.handleCallback(ctx, cb(100, "loc:local"))

	a.handleMessage(ctx, msgText(100, "tok"))

	w := a.wiz[100]
	if w == nil {
		t.Fatalf("состояние мастера пропало; лог:\n%s", fm.joined())
	}
	if w.cfg.Panel.Mode != model.ModeLocal || w.cfg.Panel.BaseURL == "" {
		t.Fatalf("локальный режим не выставлен: %+v", w.cfg.Panel)
	}

	if a.installed() {
		t.Fatal("в локальном тесте установка не должна завершиться (панель недостижима)")
	}
	_ = fs
}

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

	created := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			if created {
				_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/abc"}}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			created = true
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

	a.handleCallback(ctx, cb(user, "buy:1"))
	a.handleCallback(ctx, cb(user, "method:p2p"))
	if u, _ := fs.GetUser(ctx, user); u != nil && u.P2PApproved {
		t.Fatal("на этом шаге юзер не должен быть одобрен")
	}

	a.handleCallback(ctx, cb(100, "adm:uok:555"))
	if u, _ := fs.GetUser(ctx, user); u == nil || !u.P2PApproved {
		t.Fatal("админ должен был одобрить доступ")
	}

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

	a.handleCallback(ctx, cb(user, "p2p:paid:"+strconv.FormatInt(reqID, 10)))
	a.handlePhoto(ctx, photoMsg(user, "file_123"))
	if r, _ := fs.GetP2PRequest(ctx, reqID); r == nil || r.Status != model.P2PSubmitted || r.Screenshot != "file_123" {
		t.Fatalf("скриншот не сохранён: %+v", r)
	}

	a.handleCallback(ctx, cb(100, "adm:pok:"+strconv.FormatInt(reqID, 10)))
	if r, _ := fs.GetP2PRequest(ctx, reqID); r == nil || r.Status != model.P2PApproved {
		t.Fatalf("оплата не подтверждена: %+v", r)
	}
	if !strings.Contains(fm.joined(), "sub/abc") {
		t.Fatalf("юзеру не отправлена ссылка:\n%s", fm.joined())
	}
}

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

	_ = fs.UpsertUser(ctx, user)

	a.handleCallback(ctx, cb(100, "usr:block:555"))
	if u, _ := fs.GetUser(ctx, user); u == nil || !u.Blocked {
		t.Fatalf("юзер должен быть заблокирован: %+v", u)
	}

	fm.texts = nil
	a.handleMessage(ctx, msgText(user, "/start"))
	if !strings.Contains(fm.joined(), "ограничен") {
		t.Fatalf("заблокированному должен прийти отказ, got:\n%s", fm.joined())
	}

	fm.texts = nil
	a.handleCallback(ctx, cb(user, "menu:buy"))
	if !strings.Contains(fm.joined(), "ограничен") {
		t.Fatalf("callback заблокированного должен быть отклонён, got:\n%s", fm.joined())
	}

	a.handleCallback(ctx, cb(100, "usr:unblock:555"))
	if u, _ := fs.GetUser(ctx, user); u == nil || u.Blocked {
		t.Fatalf("юзер должен быть разблокирован: %+v", u)
	}

	a.handleCallback(ctx, cb(100, "usr:delbot:555"))
	if u, _ := fs.GetUser(ctx, user); u != nil {
		t.Fatal("после удаления записи быть не должно")
	}
}

func TestUsersAdmin_DeleteDisablesInPanel(t *testing.T) {
	var blockHits, deleteHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/users/by-telegram-id/"):

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u-555","tag":"CHILLBOT","username":"tg_555","subscriptionUrl":"https://x/sub/y"}]}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/users/"):
			deleteHits++
			w.WriteHeader(http.StatusOK)
		default:
			blockHits++
			w.WriteHeader(http.StatusOK)
		}
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
	if blockHits != 0 {
		t.Fatalf("блокировка не должна обращаться к панели, hits=%d", blockHits)
	}

	a.handleCallback(ctx, cb(100, "usr:delfull:555"))
	if deleteHits != 1 {
		t.Fatalf("delfull должен дёрнуть DELETE /api/users ровно 1 раз, hits=%d", deleteHits)
	}

	if u, _ := fs.GetUser(ctx, 555); u != nil {
		t.Fatal("после удаления users не должно остаться")
	}
}

func TestSingleMessageUI(t *testing.T) {
	srv := panelStub(5)
	defer srv.Close()
	a, fm, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()

	a.handleMessage(ctx, msgText(100, "/start"))
	afterStart := fm.liveCount()
	if afterStart == 0 {
		t.Fatalf("после /start должно быть видимое сообщение, live=%d", afterStart)
	}

	a.handleCallback(ctx, cb(100, "menu:manage"))

	a.handleCallback(ctx, cb(100, "menu:home"))
	if got := fm.liveCount(); got != afterStart {
		t.Fatalf("на экране должно оставаться только текущее (%d), а живых=%d; deleted=%v", afterStart, got, fm.deleted)
	}
	if len(fm.deleted) == 0 {
		t.Fatal("предыдущие экраны должны были удаляться")
	}
}

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

	a.handleCallback(ctx, cb(user, "buy:1"))
	a.handleCallback(ctx, cb(user, "method:p2p"))
	if !strings.Contains(fm.joined(), "просит доступ") {
		t.Fatalf("админу не пришло уведомление о запросе:\n%s", fm.joined())
	}
	notifDeleted := len(fm.deleted)

	a.handleCallback(ctx, cb(user, "buy:1"))
	a.handleCallback(ctx, cb(user, "buy:1"))
	if len(fm.deleted) > notifDeleted {

	}

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

	hasSub := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			if hasSub {
				_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/abc"}}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	if row := a.navRow(ctx, user); btnData(row) != "menu:buy|menu:balance|" {
		t.Fatalf("юзер без подписки (без Главной в строке действий): %s", btnData(row))
	}

	hasSub = true
	a.invalidateSubCache(user)
	if row := a.navRow(ctx, user); btnData(row) != "menu:mysubs|menu:balance|" {
		t.Fatalf("юзер с подпиской: %s", btnData(row))
	}
}

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
	if !strings.Contains(fm.joined(), "оплат") || !strings.Contains(fm.joined(), "перевод") {
		t.Fatalf("пользователь должен получить уведомление об открытом P2P:\n%s", fm.joined())
	}

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

func TestStarsFlow(t *testing.T) {

	created := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			if created {
				_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/abc"}}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			created = true
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

	a.handlePreCheckout(ctx, &models.PreCheckoutQuery{ID: "pc1", InvoicePayload: "stars:1"})

	a.handleSuccessfulPayment(ctx, successPayMsg(user, "stars:1", 100))

	if !strings.Contains(fm.joined(), "sub/abc") {
		t.Fatalf("после оплаты не пришла ссылка:\n%s", fm.joined())
	}
	if ok, _ := fs.HasPaidPayment(ctx, user); !ok {
		t.Fatal("оплата не записана в лог")
	}

	if row := a.navRow(ctx, user); btnData(row) != "menu:mysubs|menu:balance|" {
		t.Fatalf("после Stars-оплаты ожидались Мои подписки: %s", btnData(row))
	}
}

func TestModerationNotificationDeleted(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru", P2P: model.P2PConfig{Enabled: true, Prices: map[int]string{1: "100"}}}
	ctx := context.Background()

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
		P2P:      map[int]string{1: "140"},
		YooKassa: map[int]string{},
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

func TestYooKassaFlow(t *testing.T) {

	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/yk"}}`))
	}))
	defer panel.Close()

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

	a.handleCallback(ctx, cb(user, "ykc:pay_42"))
	if !strings.Contains(fm.joined(), "sub/yk") {
		t.Fatalf("после успешной оплаты нет ссылки:\n%s", fm.joined())
	}
	if ok, _ := fs.HasPaidPayment(ctx, user); !ok {
		t.Fatal("оплата ЮKassa не записана в лог")
	}

	before := len(fs.pays)
	a.handleCallback(ctx, cb(user, "ykc:pay_42"))
	if len(fs.pays) != before {
		t.Fatalf("повторная проверка не должна создавать новый платёж: было %d стало %d", before, len(fs.pays))
	}
}

func TestHomeReplyButton(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()

	a.handleMessage(ctx, msgText(100, "🏠 Главная"))
	if fm.joined() == "" {
		t.Fatal("по кнопке «Главная» бот ничего не показал")
	}

	if len(fm.deleted) == 0 {
		t.Fatal("сообщение-нажатие «Главная» должно удаляться")
	}
}

func TestReconciler_ResolvesAlreadyPaid(t *testing.T) {
	ctx := context.Background()
	fs := &fakeStore{}

	_ = fs.AddPayment(ctx, &model.Payment{TelegramID: 777, Method: model.PayMethodYooKassa, ExtID: "yk_x", Status: model.PaymentPaid})
	_ = fs.AddPendingInvoice(ctx, &model.PendingInvoice{
		Method: model.PayMethodYooKassa, ExtID: "yk_x", TelegramID: 777, Months: 1,
		CreatedAt: time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
	})
	a := &App{store: fs, log: slog.Default()}
	a.reconcileOnce(ctx)
	left, _ := fs.ListUnresolvedPending(ctx, time.Now().UTC().Format(time.RFC3339), 50)
	if len(left) != 0 {
		t.Fatalf("ожидалось, что pending закроется как уже-оплаченный, осталось: %d", len(left))
	}
}

func TestReconciler_GivesUpStale(t *testing.T) {
	ctx := context.Background()
	fs := &fakeStore{}
	_ = fs.AddPendingInvoice(ctx, &model.PendingInvoice{
		Method: model.PayMethodYooKassa, ExtID: "yk_old", TelegramID: 777, Months: 1,
		CreatedAt: time.Now().UTC().Add(-25 * time.Hour).Format(time.RFC3339),
	})
	a := &App{store: fs, log: slog.Default()}
	a.reconcileOnce(ctx)
	left, _ := fs.ListUnresolvedPending(ctx, time.Now().UTC().Format(time.RFC3339), 50)
	if len(left) != 0 {
		t.Fatalf("протухший инвойс должен сняться с учёта, осталось: %d", len(left))
	}
}

func TestReconciler_SkipsFresh(t *testing.T) {
	ctx := context.Background()
	fs := &fakeStore{}
	_ = fs.AddPendingInvoice(ctx, &model.PendingInvoice{
		Method: model.PayMethodYooKassa, ExtID: "yk_fresh", TelegramID: 777, Months: 1,
	})
	a := &App{store: fs, log: slog.Default()}
	a.reconcileOnce(ctx)

	left, _ := fs.ListUnresolvedPending(ctx, time.Now().UTC().Format(time.RFC3339), 50)
	if len(left) != 1 {
		t.Fatalf("свежий инвойс не должен трогаться реконсилятором, осталось: %d", len(left))
	}
}

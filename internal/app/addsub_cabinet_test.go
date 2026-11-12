package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// cabinetAcc — аккаунт кабинета, заведённый по почте.
const (
	cabinetAcc   int64 = -1000000001
	cabinetName        = "tg_-1000000001"
	cabinetEmail       = "client@example.com"
)

// cabinetPanel — панель, в которой у учёток кабинета нет telegramId. Как и
// настоящая, отрицательный telegramId в запросе она отвергает.
type cabinetPanel struct {
	mu      sync.Mutex
	users   map[string]string // имя → учётка
	v3      bool              // панель 3.x: поиск по почте — фильтром stream
	down    bool              // список пользователей не отдаётся
	posts   []string
	patches int
	negTG   int
	lookups int // поиски учётки аккаунта кабинета: по имени бота и по почте
	squads  int // чтения списка внутренних сквадов
}

func cabinetUser(name, extra string) string {
	return `{"uuid":"u-` + name + `","username":"` + name + `","status":"ACTIVE","expireAt":"2030-01-01T00:00:00Z",` +
		`"hwidDeviceLimit":3,"trafficLimitStrategy":"MONTH"` + extra + `}`
}

// byEmail — учётки с этой почтой.
func (p *cabinetPanel) byEmail(email string) string {
	var found []string
	for _, u := range p.users {
		if strings.Contains(u, `"email":"`+email+`"`) {
			found = append(found, u)
		}
	}
	return strings.Join(found, ",")
}

func (p *cabinetPanel) start(t *testing.T) *remnawave.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case strings.Contains(path, "/by-telegram-id/-") || strings.HasPrefix(r.URL.Query().Get("telegramId"), "-"):
			p.negTG++
			w.WriteHeader(http.StatusBadRequest)
		case strings.Contains(path, "/by-telegram-id/") || r.URL.Query().Get("telegramId") != "":
			p.lookups++
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"User not found"}`))
		case strings.HasSuffix(path, "/internal-squads"):
			p.squads++
			_, _ = w.Write([]byte(`{"response":{"total":1,"internalSquads":[{"uuid":"g1","name":"Общий"}]}}`))
		case strings.HasSuffix(path, "/external-squads"):
			_, _ = w.Write([]byte(`{"response":{"total":0,"externalSquads":[]}}`))
		case strings.HasPrefix(path, "/api/users/by-username/"):
			if strings.HasPrefix(path, "/api/users/by-username/tg_-") && !strings.HasSuffix(path, "_addsub") {
				p.lookups++
			}
			u, ok := p.users[strings.TrimPrefix(path, "/api/users/by-username/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"User not found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"response":` + u + `}`))
		case strings.HasPrefix(path, "/api/users/by-email/"):
			if p.v3 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Cannot GET ` + path + `"}`))
				return
			}
			p.lookups++
			_, _ = w.Write([]byte(`{"response":[` + p.byEmail(strings.TrimPrefix(path, "/api/users/by-email/")) + `]}`))
		case path == "/api/users/stream" && p.v3:
			email := r.URL.Query().Get("email")
			if email == "" {
				_, _ = w.Write([]byte(`{"response":{"users":[]}}`))
				return
			}
			p.lookups++
			_, _ = w.Write([]byte(`{"response":{"users":[` + p.byEmail(email) + `]}}`))
		case path == "/api/users" && r.Method == http.MethodGet && p.down:
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`<html><b>502</b></html>`))
		case path == "/api/users" && r.Method == http.MethodGet:
			var all []string
			for name, u := range p.users {
				if !strings.HasSuffix(name, "_addsub") {
					all = append(all, u)
				}
			}
			_, _ = w.Write([]byte(`{"response":{"total":` + itoa64(int64(len(all))) + `,"users":[` + strings.Join(all, ",") + `]}}`))
		case path == "/api/users" && r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			name, _ := body["username"].(string)
			p.users[name] = cabinetUser(name, `,"tag":"CHILLBOT_ADD"`)
			p.posts = append(p.posts, name)
			_, _ = w.Write([]byte(`{"response":{"uuid":"u-` + name + `"}}`))
		case path == "/api/users" && r.Method == http.MethodPatch:
			p.patches++
			_, _ = w.Write([]byte(`{"response":{"uuid":"u"}}`))
		default:
			_, _ = w.Write([]byte(`{"response":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

func (p *cabinetPanel) state() (posts []string, patches, negTG int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.posts...), p.patches, p.negTG
}

func (p *cabinetPanel) squadReads() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.squads
}

func (p *cabinetPanel) lookupCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lookups
}

func addSubCabinetApp(t *testing.T, users map[string]string) (*App, *fakeStore, *cabinetPanel) {
	t.Helper()
	a, _, fs := planAdminApp(t)
	p := &cabinetPanel{users: users}
	a.panel = p.start(t)
	a.panel.SetEmailResolver(a.panelEmailFor)
	a.botCfg.AddSub = model.AddSubConfig{Enabled: true, Init: true, UsernameSuffix: "_addsub",
		TrafficGB: 10, InternalSquads: []string{"g1"}}
	_ = fs.UpsertUser(context.Background(), cabinetAcc)
	return a, fs, p
}

// Синхронизация доходит до аккаунтов кабинета по почте: учётку каждого находит
// тот же поиск, что и при покупке, — по имени бота или по подтверждённой почте.
func TestAddSubBackfill_CabinetAccounts(t *testing.T) {
	byEmail := `,"tag":"CHILLBOT","email":"` + cabinetEmail + `"`
	byEmailV3 := `,"id":7` + byEmail
	cases := []struct {
		name       string
		users      map[string]string
		verified   bool
		v3         bool
		wantPosts  string
		wantFailed int
	}{
		{"учётка, заведённая ботом", map[string]string{cabinetName: cabinetUser(cabinetName, `,"tag":"CHILLBOT"`)}, false, false, cabinetName + "_addsub", 0},
		{"учётка, привязанная по почте", map[string]string{"client": cabinetUser("client", byEmail)}, true, false, "client_addsub", 0},
		{"привязанная по почте, панель 3.x", map[string]string{"client": strings.Replace(cabinetUser("client", byEmailV3), `"uuid":"u-client",`, "", 1)}, true, true, "client_addsub", 0},
		{"почта не подтверждена", map[string]string{"client": cabinetUser("client", byEmail)}, false, false, "", 0},
		{"учётки в панели нет", map[string]string{}, true, false, "", 0},
		{"привязанная учётка с чужим тегом", map[string]string{cabinetName: cabinetUser(cabinetName, `,"tag":"OTHER"`)}, false, false, cabinetName + "_addsub", 0},
		{"учётка с telegramId пройдена по списку панели", map[string]string{cabinetName: cabinetUser(cabinetName, `,"tag":"CHILLBOT","telegramId":1000000002`)}, false, false, cabinetName + "_addsub", 0},
		{"две учётки с почтой", map[string]string{"client": cabinetUser("client", byEmail), "other": cabinetUser("other", byEmail)}, true, false, "", 1},
		{"своя учётка и ещё одна с той же почтой — один проход", map[string]string{cabinetName: cabinetUser(cabinetName, `,"tag":"CHILLBOT"`), "client": cabinetUser("client", byEmail)}, true, false, cabinetName + "_addsub", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			a, fs, panel := addSubCabinetApp(t, tc.users)
			panel.v3 = tc.v3
			wu := &model.WebUser{TgID: cabinetAcc, Email: cabinetEmail}
			if tc.verified {
				wu.VerifiedAt = "2026-01-01T00:00:00Z"
			}
			_ = fs.CreateWebUser(ctx, wu)

			cli, _, opt := a.addSubOptions(false)
			st := a.addSubBackfill(ctx, cli, opt)
			posts, patches, negTG := panel.state()
			if st.err != nil || st.failed != tc.wantFailed {
				t.Fatalf("синхронизация: %+v", st)
			}
			if got := strings.Join(posts, ","); got != tc.wantPosts || patches != 0 {
				t.Fatalf("создано %q, обновлений %d; ожидалось %q без обновлений", got, patches, tc.wantPosts)
			}
			if negTG != 0 {
				t.Fatalf("в панель ушёл отрицательный telegramId: запросов %d", negTG)
			}
			// Учётка с telegramId пройдена по списку панели — искать её ещё
			// раз как учётку аккаунта кабинета незачем.
			if strings.Contains(tc.users[cabinetName], `"telegramId"`) && panel.lookupCount() != 0 {
				t.Fatalf("лишних поисков в панели: %d", panel.lookupCount())
			}
		})
	}
}

// webEmailStore отвечает на незнакомую почту так же, как настоящая база, —
// sql.ErrNoRows; broken изображает сбой базы.
type webEmailStore struct {
	*fakeStore
	broken bool
}

func (s webEmailStore) GetWebUserByEmail(ctx context.Context, email string) (*model.WebUser, error) {
	if s.broken {
		return nil, errors.New("база недоступна")
	}
	wu, _ := s.fakeStore.GetWebUserByEmail(ctx, email)
	if wu == nil {
		return nil, sql.ErrNoRows
	}
	return wu, nil
}

// В панель за учёткой аккаунта кабинета ходят, только когда в списке панели
// есть живая учётка, которая может быть его: ни за истёкшими, ни за чужими,
// ни за учётками без аккаунта в боте.
func TestAddSubBackfill_CabinetNoNeedlessLookups(t *testing.T) {
	expired := strings.Replace(cabinetUser(cabinetName, `,"tag":"CHILLBOT"`), "2030-01-01", "2020-01-01", 1)
	mine := map[string]string{"client": cabinetUser("client", `,"email":"`+cabinetEmail+`"`)}
	cases := []struct {
		name  string
		users map[string]string
		web   model.WebUser
	}{
		{"учётки в панели нет", map[string]string{}, model.WebUser{TgID: cabinetAcc, VerifiedAt: "2026-01-01T00:00:00Z"}},
		{"подписка истекла", map[string]string{cabinetName: expired}, model.WebUser{TgID: cabinetAcc, VerifiedAt: "2026-01-01T00:00:00Z"}},
		{"учётка с именем бота без аккаунта в боте", map[string]string{"tg_-1000000009": cabinetUser("tg_-1000000009", `,"tag":"CHILLBOT"`)}, model.WebUser{TgID: cabinetAcc, VerifiedAt: "2026-01-01T00:00:00Z"}},
		{"чужая учётка с незнакомой почтой", map[string]string{"stranger": cabinetUser("stranger", `,"email":"stranger@example.com"`)}, model.WebUser{TgID: cabinetAcc, VerifiedAt: "2026-01-01T00:00:00Z"}},
		{"чужая учётка без почты", map[string]string{"stranger": cabinetUser("stranger", "")}, model.WebUser{TgID: cabinetAcc, VerifiedAt: "2026-01-01T00:00:00Z"}},
		{"почта аккаунта не подтверждена", mine, model.WebUser{TgID: cabinetAcc}},
		{"аккаунт переехал на telegram id", mine, model.WebUser{TgID: 1000000002, VerifiedAt: "2026-01-01T00:00:00Z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			a, fs, panel := addSubCabinetApp(t, tc.users)
			wu := tc.web
			wu.Email = cabinetEmail
			_ = fs.CreateWebUser(ctx, &wu)
			a.store = webEmailStore{fakeStore: fs}

			cli, _, opt := a.addSubOptions(false)
			if st := a.addSubBackfill(ctx, cli, opt); st.err != nil || st.ok != 0 || st.failed != 0 {
				t.Fatalf("синхронизация: %+v", st)
			}
			if posts, _, _ := panel.state(); len(posts) != 0 || panel.lookupCount() != 0 {
				t.Fatalf("создано %v, поисков в панели %d", posts, panel.lookupCount())
			}
		})
	}
}

// Сбой базы при разборе аккаунтов кабинета останавливает синхронизацию до
// первой записи в панель, а не выдаётся за «аккаунта нет».
func TestAddSubBackfill_CabinetStoreError(t *testing.T) {
	ctx := context.Background()
	a, fs, panel := addSubCabinetApp(t, map[string]string{"client": cabinetUser("client", `,"email":"`+cabinetEmail+`"`)})
	a.store = webEmailStore{fakeStore: fs, broken: true}

	cli, _, opt := a.addSubOptions(false)
	if st := a.addSubBackfill(ctx, cli, opt); st.err == nil {
		t.Fatalf("сбой базы не остановил синхронизацию: %+v", st)
	}
	if posts, patches, _ := panel.state(); len(posts) != 0 || patches != 0 {
		t.Fatalf("в панель писали при сбое базы: %v, %d", posts, patches)
	}
}

// Номер аккаунта кабинета читается только из имени, которое бот даёт сам.
func TestCabinetIDByName(t *testing.T) {
	cases := []struct {
		name string
		id   int64
		ok   bool
	}{
		{"tg_-1000000001", -1000000001, true},
		{"tg_1000000001", 0, false},
		{"tg_-0", 0, false},
		{"tg_-01", 0, false},
		{"tg_-abc", 0, false},
		{"tg_-1000000001_addsub", 0, false},
		{"client", 0, false},
	}
	for _, tc := range cases {
		if id, ok := cabinetIDByName(tc.name); id != tc.id || ok != tc.ok {
			t.Fatalf("%q: %d %v, ожидалось %d %v", tc.name, id, ok, tc.id, tc.ok)
		}
	}
}

// Текст ошибки панели в итоговом сообщении экранируется: иначе Telegram
// отверг бы сообщение, и администратор не узнал бы, чем кончилось.
func TestAddSubSync_ErrorTextEscaped(t *testing.T) {
	a, _, panel := addSubCabinetApp(t, map[string]string{})
	fm := a.msg.(*fakeMsg)
	panel.mu.Lock()
	panel.down = true
	panel.mu.Unlock()

	planTap(t, a, "addsub:sync:all")
	deadline := time.Now().Add(5 * time.Second)
	for a.addSubSyncing.Load() || !strings.Contains(fm.last(), "прервана") {
		if time.Now().After(deadline) {
			t.Fatalf("итог не пришёл: %q", fm.last())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := fm.last(); strings.Contains(got, "<html>") || !strings.Contains(got, "&lt;") {
		t.Fatalf("текст ошибки панели не экранирован: %q", got)
	}
}

// Аккаунту кабинета, купившему тариф при выключенной доп-подписке, «выдать
// всем» её выдаёт и отмечает в его снимке; «обновить» до этого его не трогает.
func TestAddSubBackfill_CabinetBoughtWhileOff(t *testing.T) {
	ctx := context.Background()
	a, fs, panel := addSubCabinetApp(t, map[string]string{cabinetName: cabinetUser(cabinetName, `,"tag":"CHILLBOT"`)})
	p := vipPlan(t, fs, model.PlanAvailAll)
	no := false
	_ = fs.SetUserSnapshot(ctx, cabinetAcc, &model.PlanSnapshot{Code: p.Code, Months: 1, AddSub: &no})

	cli, _, opt := a.addSubOptions(false)
	upd := opt
	upd.OnlyExisting = true
	if st := a.addSubBackfill(ctx, cli, upd); st.err != nil || st.ok != 0 || st.failed != 0 {
		t.Fatalf("«обновить»: %+v", st)
	}
	if posts, patches, _ := panel.state(); len(posts) != 0 || patches != 0 || a.addSubSoldTo(ctx, cabinetAcc) {
		t.Fatal("«обновить» выдало доп-подписку аккаунту кабинета")
	}

	if st := a.addSubBackfill(ctx, cli, opt); st.err != nil || st.ok != 1 || st.failed != 0 {
		t.Fatalf("«выдать всем»: %+v", st)
	}
	if posts, _, _ := panel.state(); len(posts) != 1 || !a.addSubSoldTo(ctx, cabinetAcc) {
		t.Fatalf("доп-подписка аккаунту кабинета не выдана: %v", posts)
	}

	if st := a.addSubBackfill(ctx, cli, upd); st.err != nil || st.ok != 1 {
		t.Fatalf("повторное «обновить»: %+v", st)
	}
	if posts, patches, negTG := panel.state(); len(posts) != 1 || patches != 1 || negTG != 0 {
		t.Fatalf("«обновить» после выдачи: создано %d, обновлений %d, отрицательных telegramId %d", len(posts), patches, negTG)
	}
}

// Учётка аккаунта кабинета, у которой в панели есть telegramId, уже пройдена
// по списку панели: второй раз, как учётка аккаунта, она не обрабатывается.
func TestAddSubBackfill_CabinetAccountWithTelegramOnce(t *testing.T) {
	ctx := context.Background()
	a, fs, panel := addSubCabinetApp(t, map[string]string{
		cabinetName: cabinetUser(cabinetName, `,"tag":"CHILLBOT","telegramId":1000000002`),
		"client":    cabinetUser("client", `,"email":"`+cabinetEmail+`"`),
	})
	_ = fs.CreateWebUser(ctx, &model.WebUser{TgID: cabinetAcc, Email: cabinetEmail, VerifiedAt: "2026-01-01T00:00:00Z"})

	cli, _, opt := a.addSubOptions(false)
	st := a.addSubBackfill(ctx, cli, opt)
	posts, patches, _ := panel.state()
	if st.err != nil || st.failed != 0 || st.ok != 1 {
		t.Fatalf("синхронизация: %+v", st)
	}
	if got := strings.Join(posts, ","); got != cabinetName+"_addsub" || patches != 0 {
		t.Fatalf("создано %q, обновлений %d; ожидалась одна доп-подписка без повторной записи", got, patches)
	}
}

// Список сквадов панели нужен каждому, чья сделка несёт свои сквады, но за
// обход он читается один раз, а не на каждого пользователя.
func TestAddSubBackfill_SquadsReadOnce(t *testing.T) {
	ctx := context.Background()
	users := map[string]string{}
	ids := []int64{1000000002, 1000000003, 1000000004}
	for _, id := range ids {
		name := "tg_" + itoa64(id)
		users[name] = cabinetUser(name, `,"tag":"CHILLBOT","telegramId":`+itoa64(id))
	}
	a, fs, panel := addSubCabinetApp(t, users)
	yes := true
	for _, id := range ids {
		_ = fs.UpsertUser(ctx, id)
		_ = fs.SetUserSnapshot(ctx, id, &model.PlanSnapshot{Code: "vipvipvipvip", Months: 1, AddSub: &yes, AddSubSquads: []string{"g1"}})
	}

	cli, _, opt := a.addSubOptions(false)
	if st := a.addSubBackfill(ctx, cli, opt); st.err != nil || st.failed != 0 || st.ok != len(ids) {
		t.Fatalf("синхронизация: %+v", st)
	}
	if got := panel.squadReads(); got != 1 {
		t.Fatalf("список сквадов прочитан %d раз за обход, ожидался один", got)
	}
}

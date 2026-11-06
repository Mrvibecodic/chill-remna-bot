package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"remnabot/internal/model"
)

// emailPanel — панель 3.x в памяти. Повторяет валидацию контракта там, где
// бот может ошибиться: отрицательный telegramId в фильтре — 400, почта в
// фильтре проверяется на формат, сравнение почты строгое.
type emailPanel struct {
	mu      sync.Mutex
	users   map[int64]map[string]any
	patches []map[string]any
	writes  []string // POST и DELETE: их в этих сценариях быть не должно
	srv     *httptest.Server
}

func newEmailPanel(t *testing.T) *emailPanel {
	t.Helper()
	p := &emailPanel{users: map[int64]map[string]any{}}
	exp := time.Now().UTC().Add(720 * time.Hour).Format(time.RFC3339)
	notFound := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"User not found","errorCode":"A063"}`))
	}
	one := func(w http.ResponseWriter, u map[string]any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"response": u})
	}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && path == "/api/users/stream":
			q := r.URL.Query()
			if tg := q.Get("telegramId"); tg != "" {
				n, err := strconv.ParseInt(tg, 10, 64)
				if err != nil || n < 0 {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			}
			if em := q.Get("email"); em != "" && !strings.Contains(em, "@") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var out []map[string]any
			for _, u := range p.users {
				if tg := q.Get("telegramId"); tg != "" && strconv.FormatInt(toInt(u["telegramId"]), 10) != tg {
					continue
				}
				if em := q.Get("email"); em != "" && u["email"] != em {
					continue
				}
				out = append(out, u)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"users": out}})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/users/by-username/"):
			name := strings.TrimPrefix(path, "/api/users/by-username/")
			for _, u := range p.users {
				if u["username"] == name {
					one(w, u)
					return
				}
			}
			notFound(w)
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/users/by-"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Cannot GET ` + path + `"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/users/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/api/users/"), 10, 64)
			if u, ok := p.users[id]; ok {
				one(w, u)
				return
			}
			notFound(w)
		case r.Method == http.MethodPatch && path == "/api/users":
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			p.patches = append(p.patches, body)
			u, ok := p.users[toInt(body["id"])]
			if !ok {
				notFound(w)
				return
			}
			for k, v := range body {
				if k != "id" {
					u[k] = v
				}
			}
			one(w, u)
		case r.Method == http.MethodPost && path == "/api/users":
			p.writes = append(p.writes, r.Method+" "+path)
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			id := int64(100 + len(p.users))
			body["id"] = float64(id)
			body["subscriptionUrl"] = "https://example.test/s/" + strconv.FormatInt(id, 10)
			p.users[id] = body
			one(w, body)
		default:
			p.writes = append(p.writes, r.Method+" "+path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(p.srv.Close)
	p.add(1, map[string]any{"username": "client1", "expireAt": exp})
	return p
}

func toInt(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func (p *emailPanel) add(id int64, fields map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	u := map[string]any{"id": float64(id), "uuid": "", "status": "ACTIVE", "subscriptionUrl": "https://example.test/s/" + strconv.FormatInt(id, 10)}
	for k, v := range fields {
		u[k] = v
	}
	p.users[id] = u
}

func (p *emailPanel) field(id int64, k string) any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.users[id][k]
}

func (p *emailPanel) patchCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.patches)
}

func emailLinkApp(t *testing.T, verified bool) (*App, *fakeStore, *fakeMsg, *emailPanel, int64) {
	t.Helper()
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.ui = map[int64]*uiState{}
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.NormalizeCabinet()
	a.botCfg.Cabinet.Enabled = true
	p := newEmailPanel(t)
	a.panel = a.newPanel(model.PanelConfig{BaseURL: p.srv.URL, APIToken: "t"})
	ctx := context.Background()
	id, err := a.CabinetEmailRegister(ctx, "client@example.com", "password1", "")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	if verified {
		if err := fs.SetWebUserVerified(ctx, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	return a, fs, fm, p, id
}

func adminLink(a *App, uid int64, input string) {
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "usr:link:"+strconv.FormatInt(uid, 10)))
	a.handleMessage(ctx, msgText(100, input))
}

// Привязка аккаунта кабинета пишет в учётку его почту, а не синтетический id,
// и после неё подписку видят и кабинет, и бот.
func TestAdminLinkEmailAccountWritesEmail(t *testing.T) {
	for _, verified := range []bool{true, false} {
		t.Run("verified="+strconv.FormatBool(verified), func(t *testing.T) {
			a, fs, fm, p, id := emailLinkApp(t, verified)
			ctx := context.Background()
			adminLink(a, id, "client1")

			if got := p.field(1, "email"); got != "client@example.com" {
				t.Fatalf("почта в учётке: %v\n%s", got, fm.joined())
			}
			if got := p.field(1, "telegramId"); got != nil {
				t.Fatalf("синтетический id ушёл в панель: %v", got)
			}
			if got := p.field(1, "tag"); got != "CHILLBOT" {
				t.Fatalf("тег бота: %v", got)
			}
			url, _, _, found, err := a.panel.SubscriptionState(ctx, id)
			if err != nil || !found || !strings.Contains(url, "/s/1") {
				t.Fatalf("кабинет не видит привязанную подписку: url=%q found=%v err=%v", url, found, err)
			}
			if u, _ := fs.GetUser(ctx, id); u == nil || u.SubExpireAt == "" {
				t.Fatal("срок подписки не записан в бота")
			}
			if wu, _ := fs.GetWebUserByTgID(ctx, id); wu == nil || wu.VerifiedAt == "" {
				t.Fatal("после ручной привязки почта обязана считаться подтверждённой")
			}
			if len(p.writes) > 0 {
				t.Fatalf("привязка не должна ничего создавать и удалять: %v", p.writes)
			}
		})
	}
}

// Мусорный отрицательный telegramId от прежней привязки затирается, а не
// мешает связке.
func TestAdminLinkEmailClearsGarbageTelegramID(t *testing.T) {
	a, _, fm, p, id := emailLinkApp(t, true)
	p.add(1, map[string]any{"username": "client1", "telegramId": float64(id) + 1000, "expireAt": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
	adminLink(a, id, "client1")
	if got := p.field(1, "telegramId"); got != nil {
		t.Fatalf("мусорный telegramId остался: %v\n%s", got, fm.joined())
	}
	if got := p.field(1, "email"); got != "client@example.com" {
		t.Fatalf("почта не записана: %v", got)
	}
}

// Отказы: ни один не пишет в панель.
func TestAdminLinkEmailRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(p *emailPanel, id int64)
		want  string
	}{
		{"учётка живого Telegram", func(p *emailPanel, id int64) {
			p.add(1, map[string]any{"username": "client1", "telegramId": float64(1000000001)})
		}, "другому Telegram ID"},
		{"учётка с другой почтой", func(p *emailPanel, id int64) {
			p.add(1, map[string]any{"username": "client1", "email": "other@example.com"})
		}, "другой почте"},
		{"почта уже в другой учётке", func(p *emailPanel, id int64) {
			p.add(2, map[string]any{"username": "client2", "email": "client@example.com", "telegramId": float64(1000000003)})
		}, "другая учётка"},
		{"почта уже в учётке без Telegram", func(p *emailPanel, id int64) {
			p.add(2, map[string]any{"username": "client2", "email": "client@example.com"})
		}, "своя учётка"},
		{"своя учётка бота", func(p *emailPanel, id int64) {
			p.add(3, map[string]any{"username": "tg_" + strconv.FormatInt(id, 10), "tag": "CHILLBOT"})
		}, "своя учётка"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _, fm, p, id := emailLinkApp(t, true)
			c.setup(p, id)
			adminLink(a, id, "client1")
			if p.patchCount() != 0 {
				t.Fatalf("отказ записал в панель: %v", p.patches)
			}
			if !strings.Contains(fm.joined(), c.want) {
				t.Fatalf("нет объяснения %q:\n%s", c.want, fm.joined())
			}
		})
	}
}

// Почту вписали в учётку прямо в панели: подтверждённый аккаунт кабинета
// подхватывает её сам, неподтверждённый — нет. В панель автопривязка не пишет.
func TestEmailAutoLinkOnlyVerified(t *testing.T) {
	for _, verified := range []bool{true, false} {
		t.Run("verified="+strconv.FormatBool(verified), func(t *testing.T) {
			a, fs, _, p, id := emailLinkApp(t, verified)
			ctx := context.Background()
			p.add(1, map[string]any{"username": "client1", "email": "client@example.com", "expireAt": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
			linked := a.syncPanelAccount(ctx, id)
			if linked != verified {
				t.Fatalf("автопривязка=%v, ждали %v", linked, verified)
			}
			if p.patchCount() != 0 || len(p.writes) != 0 {
				t.Fatalf("автопривязка писала в панель: %v %v", p.patches, p.writes)
			}
			u, _ := fs.GetUser(ctx, id)
			if verified && (u == nil || u.SubExpireAt == "") {
				t.Fatal("срок не записан")
			}
			if !verified && u != nil && u.SubExpireAt != "" {
				t.Fatal("неподтверждённая почта не должна давать чужую подписку")
			}
		})
	}
}

// Отвязка снимает только привязку: учётка и срок в панели остаются.
func TestAdminUnlinkEmailAccount(t *testing.T) {
	a, fs, fm, p, id := emailLinkApp(t, true)
	ctx := context.Background()
	adminLink(a, id, "client1")
	sid := strconv.FormatInt(id, 10)
	a.handleCallback(ctx, cb(100, "usr:unlink:"+sid))
	if !strings.Contains(fm.joined(), "client1") {
		t.Fatalf("в подтверждении нет учётки:\n%s", fm.joined())
	}
	expBefore := p.field(1, "expireAt")
	a.handleCallback(ctx, cb(100, "usr:unlinkok:"+sid))

	if got := p.field(1, "email"); got != nil {
		t.Fatalf("почта не снята: %v\n%s", got, fm.joined())
	}
	if got := p.field(1, "tag"); got != nil {
		t.Fatalf("тег бота не снят: %v", got)
	}
	if p.field(1, "username") != "client1" || p.field(1, "expireAt") != expBefore {
		t.Fatal("отвязка тронула саму учётку")
	}
	if len(p.writes) > 0 {
		t.Fatalf("отвязка не должна ничего удалять: %v", p.writes)
	}
	u, _ := fs.GetUser(ctx, id)
	if u == nil || u.SubExpireAt != "" || u.TrialUsedAt == "" {
		t.Fatalf("в боте: срок обязан стереться, отметка о триале — остаться: %+v", u)
	}
	if _, _, _, found, err := a.panel.SubscriptionState(ctx, id); found || err != nil {
		t.Fatalf("после отвязки подписка всё ещё находится: found=%v err=%v", found, err)
	}
}

// Учётку, которую завёл сам бот, отвязать нельзя — и в панель ничего не уходит.
func TestAdminUnlinkRefusesBotAccount(t *testing.T) {
	a, _, fm, p, id := emailLinkApp(t, true)
	ctx := context.Background()
	p.add(3, map[string]any{"username": "tg_" + strconv.FormatInt(id, 10), "tag": "CHILLBOT"})
	a.handleCallback(ctx, cb(100, "usr:unlinkok:"+strconv.FormatInt(id, 10)))
	if p.patchCount() != 0 || len(p.writes) != 0 {
		t.Fatalf("учётку бота тронули: %v %v", p.patches, p.writes)
	}
	if !strings.Contains(fm.joined(), "создал сам бот") {
		t.Fatalf("нет объяснения:\n%s", fm.joined())
	}
}

// Telegram-пользователь: снимается telegramId, учётка остаётся.
func TestAdminUnlinkTelegramUser(t *testing.T) {
	a, fs, _, p, _ := emailLinkApp(t, true)
	ctx := context.Background()
	const tg int64 = 1000000002
	_ = fs.UpsertUser(ctx, tg)
	p.add(5, map[string]any{"username": "ivan", "telegramId": float64(tg), "tag": "OTHER"})
	a.handleCallback(ctx, cb(100, "usr:unlink:"+strconv.FormatInt(tg, 10)))
	a.handleCallback(ctx, cb(100, "usr:unlinkok:"+strconv.FormatInt(tg, 10)))
	if got := p.field(5, "telegramId"); got != nil {
		t.Fatalf("telegramId не снят: %v", got)
	}
	if got := p.field(5, "tag"); got != "OTHER" {
		t.Fatalf("чужой тег трогать нельзя: %v", got)
	}
	if len(p.writes) > 0 {
		t.Fatalf("отвязка не должна ничего удалять: %v", p.writes)
	}
}

// Аккаунт кабинета в админке подписан почтой, а не отрицательным id.
func TestAdminShowsEmailInsteadOfSyntheticID(t *testing.T) {
	a, _, fm, _, id := emailLinkApp(t, true)
	ctx := context.Background()
	sid := strconv.FormatInt(id, 10)
	a.handleCallback(ctx, cb(100, "usr:view:"+sid))
	a.handleCallback(ctx, cb(100, "usr:link:"+sid))
	a.handleCallback(ctx, cb(100, "usr:del:"+sid))
	out := fm.joined()
	if !strings.Contains(out, "client@example.com") {
		t.Fatalf("почты нет:\n%s", out)
	}
	if strings.Contains(out, sid) {
		t.Fatalf("в тексте виден синтетический id:\n%s", out)
	}
}

func enableTrial(a *App) {
	a.botCfg.Trial.Enabled = true
	a.botCfg.Trial.Days = 3
	a.botCfg.Trial.TrafficGB = 1
}

// Первый вход до подтверждения почты, потом подтверждение и «Триал»: живая
// учётка с этой почтой не должна получить триальные лимиты.
func TestTrialDoesNotOverwriteLiveEmailAccount(t *testing.T) {
	a, fs, _, p, id := emailLinkApp(t, false)
	ctx := context.Background()
	enableTrial(a)
	a.syncPanelAccount(ctx, id)
	_ = fs.SetWebUserVerified(ctx, id, "")
	p.add(1, map[string]any{"username": "client1", "email": "client@example.com", "trafficLimitBytes": float64(0),
		"expireAt": time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)})
	a.MiniTrial(ctx, id)
	if p.patchCount() != 0 || len(p.writes) != 0 {
		t.Fatalf("триал тронул живую учётку: %v %v", p.patches, p.writes)
	}
	if u, _ := fs.GetUser(ctx, id); u == nil || u.SubExpireAt == "" || u.TrialUsedAt == "" {
		t.Fatalf("учётка должна была привязаться вместо триала: %+v", u)
	}
}

// Та же защита для Telegram: учётку с его id вписали в панели уже после
// первого входа, когда автопривязка в этом запуске своё отработала.
func TestTrialRefusedOverLiveForeignTelegramAccount(t *testing.T) {
	a, fs, _, p, _ := emailLinkApp(t, true)
	ctx := context.Background()
	enableTrial(a)
	const tg int64 = 1000000004
	_ = fs.UpsertUser(ctx, tg)
	a.claimPanelSync(tg)
	p.add(7, map[string]any{"username": "ivan", "telegramId": float64(tg), "trafficLimitBytes": float64(0),
		"expireAt": time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)})
	a.MiniTrial(ctx, tg)
	if p.patchCount() != 0 || len(p.writes) != 0 {
		t.Fatalf("триал тронул живую учётку: %v %v", p.patches, p.writes)
	}
}

// Без учётки в панели триал выдаётся как раньше.
func TestTrialStillGrantedWithoutPanelAccount(t *testing.T) {
	a, fs, _, p, id := emailLinkApp(t, true)
	ctx := context.Background()
	enableTrial(a)
	a.MiniTrial(ctx, id)
	if len(p.writes) != 1 || p.writes[0] != "POST /api/users" {
		t.Fatalf("ждали создание триальной учётки: %v", p.writes)
	}
	if u, _ := fs.GetUser(ctx, id); u == nil || u.TrialUsedAt == "" {
		t.Fatal("триал не отмечен")
	}
}

// Telegram-привязка не забирает учётку, привязанную по почте к другому
// пользователю. Неподтверждённая почта учётку не держит — привязка проходит.
func TestAdminLinkTelegramRefusesEmailBoundAccount(t *testing.T) {
	t.Run("почта не подтверждена", func(t *testing.T) {
		a, fs, _, p, _ := emailLinkApp(t, false)
		ctx := context.Background()
		p.add(1, map[string]any{"username": "client1", "email": "client@example.com"})
		const tg int64 = 1000000008
		_ = fs.UpsertUser(ctx, tg)
		adminLink(a, tg, "client1")
		if toInt(p.field(1, "telegramId")) != tg {
			t.Fatalf("привязка не прошла: %v", p.patches)
		}
	})
	a, fs, fm, p, _ := emailLinkApp(t, true)
	ctx := context.Background()
	p.add(1, map[string]any{"username": "client1", "email": "client@example.com"})
	const tg int64 = 1000000005
	_ = fs.UpsertUser(ctx, tg)
	adminLink(a, tg, "client1")
	if p.patchCount() != 0 {
		t.Fatalf("учётку забрали: %v", p.patches)
	}
	if !strings.Contains(fm.joined(), "привязана по почте") || !strings.Contains(fm.joined(), "client@example.com") {
		t.Fatalf("нет объяснения:\n%s", fm.joined())
	}
}

// Кнопка подтверждения из старого сообщения: отвязываемая учётка сменилась —
// бот показывает новую и ждёт подтверждения, а не отвязывает молча.
func TestAdminUnlinkStaleConfirm(t *testing.T) {
	a, _, fm, p, id := emailLinkApp(t, true)
	ctx := context.Background()
	sid := strconv.FormatInt(id, 10)
	p.add(1, map[string]any{"username": "client1", "email": "client@example.com"})
	a.handleCallback(ctx, cb(100, "usr:unlink:"+sid))
	p.add(1, map[string]any{"username": "client1"})
	p.add(2, map[string]any{"username": "client2", "email": "client@example.com"})
	a.handleCallback(ctx, cb(100, "usr:unlinkok:"+sid))
	if p.patchCount() != 0 {
		t.Fatalf("отвязано без подтверждения: %v", p.patches)
	}
	if !strings.Contains(fm.joined(), "client2") {
		t.Fatalf("новая учётка не показана:\n%s", fm.joined())
	}
	a.handleCallback(ctx, cb(100, "usr:unlinkok:"+sid))
	if p.patchCount() != 1 || p.field(2, "email") != nil {
		t.Fatalf("после подтверждения не отвязано: %v", p.patches)
	}
}

// Отметка о триале при отвязке ставится, только если иначе автопривязка
// нашла бы учётку снова (id в имени); остальным — нет.
func TestAdminUnlinkTrialMark(t *testing.T) {
	cases := []struct {
		name     string
		tg       int64
		username string
		want     bool
	}{
		{"id в имени", 1000000006, "ivan_1000000006", true},
		{"имя без id", 1000000007, "ivan", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, fs, _, p, _ := emailLinkApp(t, true)
			ctx := context.Background()
			_ = fs.UpsertUser(ctx, c.tg)
			p.add(9, map[string]any{"username": c.username, "telegramId": float64(c.tg)})
			sid := strconv.FormatInt(c.tg, 10)
			a.handleCallback(ctx, cb(100, "usr:unlink:"+sid))
			a.handleCallback(ctx, cb(100, "usr:unlinkok:"+sid))
			if p.field(9, "telegramId") != nil {
				t.Fatal("не отвязано")
			}
			u, _ := fs.GetUser(ctx, c.tg)
			if got := u != nil && u.TrialUsedAt != ""; got != c.want {
				t.Fatalf("отметка о триале=%v, ждали %v", got, c.want)
			}
		})
	}
}

// Вход до подтверждения почты не запирает автопривязку до перезапуска: после
// подтверждения следующий вход находит учётку.
func TestEmailAutoLinkAfterLateVerify(t *testing.T) {
	a, fs, _, p, id := emailLinkApp(t, false)
	ctx := context.Background()
	p.add(1, map[string]any{"username": "client1", "email": "client@example.com",
		"expireAt": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
	if a.syncPanelAccount(ctx, id) {
		t.Fatal("неподтверждённая почта не должна находить учётку")
	}
	_ = fs.SetWebUserVerified(ctx, id, "")
	if !a.syncPanelAccount(ctx, id) {
		t.Fatal("после подтверждения учётка не нашлась")
	}
	if u, _ := fs.GetUser(ctx, id); u == nil || u.SubExpireAt == "" {
		t.Fatal("срок не записан")
	}
}

// Telegram-пользователь, который пришёл из кабинета по почте: в учётке обе
// привязки, и отвязка снимает обе — иначе учётку не привязать никому другому.
func TestAdminUnlinkBoundAccountClearsOwnEmail(t *testing.T) {
	a, _, _, p, id := emailLinkApp(t, true)
	ctx := context.Background()
	p.add(1, map[string]any{"username": "client1", "email": "client@example.com"})
	const tg int64 = 1000000009
	if _, err := a.CabinetBindTelegram(ctx, id, tg); err != nil {
		t.Fatalf("привязка Telegram: %v", err)
	}
	if toInt(p.field(1, "telegramId")) != tg {
		t.Fatal("telegramId не проставлен")
	}
	sid := strconv.FormatInt(tg, 10)
	a.handleCallback(ctx, cb(100, "usr:unlink:"+sid))
	a.handleCallback(ctx, cb(100, "usr:unlinkok:"+sid))
	if p.field(1, "telegramId") != nil || p.field(1, "email") != nil {
		t.Fatalf("сняты не обе привязки: tg=%v email=%v", p.field(1, "telegramId"), p.field(1, "email"))
	}
	if p.field(1, "username") != "client1" || len(p.writes) != 0 {
		t.Fatal("отвязка тронула саму учётку")
	}
}

// Чужую почту в учётке Telegram-пользователя отвязка не трогает.
func TestAdminUnlinkKeepsForeignEmail(t *testing.T) {
	a, fs, _, p, _ := emailLinkApp(t, true)
	ctx := context.Background()
	const tg int64 = 1000000010
	_ = fs.UpsertUser(ctx, tg)
	p.add(5, map[string]any{"username": "ivan", "telegramId": float64(tg), "email": "ivan@example.com"})
	sid := strconv.FormatInt(tg, 10)
	a.handleCallback(ctx, cb(100, "usr:unlink:"+sid))
	a.handleCallback(ctx, cb(100, "usr:unlinkok:"+sid))
	if p.field(5, "telegramId") != nil {
		t.Fatal("telegramId не снят")
	}
	if p.field(5, "email") != "ivan@example.com" {
		t.Fatalf("чужая почта стёрта: %v", p.field(5, "email"))
	}
}

// Вместо триала нашлась живая учётка — человеку говорят, что подписка нашлась.
func TestTrialFoundSubIsReported(t *testing.T) {
	a, _, _, p, id := emailLinkApp(t, true)
	ctx := context.Background()
	enableTrial(a)
	a.claimPanelSync(id)
	p.add(1, map[string]any{"username": "client1", "email": "client@example.com",
		"expireAt": time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)})
	got := a.MiniTrial(ctx, id)
	if got.OK || !strings.Contains(got.Error, "нашли вашу подписку") {
		t.Fatalf("ответ: %+v", got)
	}
}

// Подпись кнопки в списке — как есть, а в тексте сообщения — экранированная.
func TestUserButtonLabelNotEscaped(t *testing.T) {
	a, _, _, _, _ := emailLinkApp(t, true)
	ctx := context.Background()
	u := &model.User{TelegramID: 1000000011, FirstName: "Tom & Jerry"}
	if got := a.userButtonLabel(ctx, u); got != "Tom & Jerry (1000000011)" {
		t.Fatalf("кнопка: %q", got)
	}
	if got := a.userLabelFor(ctx, u); got != "Tom &amp; Jerry (1000000011)" {
		t.Fatalf("текст: %q", got)
	}
}

// То же в чате: вместо «триал недоступен» — «нашли вашу подписку».
func TestChatTrialFoundSubIsReported(t *testing.T) {
	a, fs, fm, p, _ := emailLinkApp(t, true)
	ctx := context.Background()
	enableTrial(a)
	const tg int64 = 1000000012
	_ = fs.UpsertUser(ctx, tg)
	a.claimPanelSync(tg)
	p.add(7, map[string]any{"username": "ivan", "telegramId": float64(tg),
		"expireAt": time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)})
	a.handleCallback(ctx, cb(tg, "menu:trial"))
	if !strings.Contains(fm.joined(), "Нашли вашу подписку") {
		t.Fatalf("нет сообщения о найденной подписке:\n%s", fm.joined())
	}
	if p.patchCount() != 0 || len(p.writes) != 0 {
		t.Fatalf("учётку тронули: %v %v", p.patches, p.writes)
	}
}

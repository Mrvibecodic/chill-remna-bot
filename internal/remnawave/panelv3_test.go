package remnawave

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"remnabot/internal/model"
)

// v3Panel fakes Remnawave 3.0.0: no user uuid at all (numeric id everywhere),
// no /api/users/by-telegram-id, lookups go through /api/users/stream.
type v3Panel struct {
	mu sync.Mutex

	expire string

	legacyHits int // requests to the removed by-telegram-id route
	streamHits int
	patches    []map[string]any
	posts      []map[string]any
	deleted    []string
	resets     []string
	revokes    []string
	hwidBodies []map[string]any
	hwidPaths  []string
}

func newV3Panel() *v3Panel {
	return &v3Panel{expire: time.Now().UTC().Add(720 * time.Hour).Format(time.RFC3339)}
}

func (p *v3Panel) user() string {
	return `{"id":77,"shortUuid":"sh","username":"tg_42","telegramId":42,"tag":"` + BotTag +
		`","status":"ACTIVE","expireAt":"` + p.expire +
		`","hwidDeviceLimit":3,"trafficLimitStrategy":"MONTH","subscriptionUrl":"https://x/y","userTraffic":{"usedTrafficBytes":5}}`
}

func (p *v3Panel) start(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.legacyHits++
		p.mu.Unlock()
		// Роут удалён в 3.0.0 — фреймворк отвечает «нет такого маршрута».
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"Cannot GET ` + r.URL.Path + `","error":"Not Found","statusCode":404}`))
	})
	mux.HandleFunc("/api/users/stream", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.streamHits++
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if tg := r.URL.Query().Get("telegramId"); tg != "" && tg != "42" {
			w.Write([]byte(`{"response":{"users":[],"nextCursor":null,"hasMore":false}}`))
			return
		}
		w.Write([]byte(`{"response":{"users":[` + p.user() + `],"nextCursor":null,"hasMore":false}}`))
	})
	mux.HandleFunc("/api/users/by-username/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		p.mu.Lock()
		switch r.Method {
		case http.MethodPost:
			p.posts = append(p.posts, body)
		case http.MethodPatch:
			p.patches = append(p.patches, body)
		}
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated) // 3.0.0 answers 201 on create
		w.Write([]byte(`{"response":` + p.user() + `}`))
	})
	mux.HandleFunc("/api/users/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/users/")
		p.mu.Lock()
		switch {
		case r.Method == http.MethodDelete:
			p.deleted = append(p.deleted, rest)
			p.mu.Unlock()
			w.WriteHeader(http.StatusNoContent) // 3.0.0 answers 204
			return
		case strings.HasSuffix(rest, "/actions/reset-traffic"):
			p.resets = append(p.resets, strings.TrimSuffix(rest, "/actions/reset-traffic"))
		case strings.HasSuffix(rest, "/actions/revoke"):
			p.revokes = append(p.revokes, strings.TrimSuffix(rest, "/actions/revoke"))
		}
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":{}}`))
	})
	mux.HandleFunc("/api/hwid/devices/delete-all", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		p.mu.Lock()
		p.hwidBodies = append(p.hwidBodies, body)
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":{"total":0,"devices":[]}}`))
	})
	mux.HandleFunc("/api/hwid/devices/", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.hwidPaths = append(p.hwidPaths, strings.TrimPrefix(r.URL.Path, "/api/hwid/devices/"))
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":{"total":2,"devices":[]}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

func TestV3LookupFallsBackToStream(t *testing.T) {
	p := newV3Panel()
	c := p.start(t)
	u, _, ok := c.Subscription(context.Background(), 42)
	if !ok || u == "" {
		t.Fatalf("подписка не найдена на панели 3.x: ok=%v", ok)
	}
	if c.generation() != genV3 {
		t.Fatalf("диалект не определён: %v", c.generation())
	}
	// Второй запрос уже идёт сразу в stream, без стука в удалённый роут.
	before := p.legacyHits
	if _, _, ok := c.Subscription(context.Background(), 42); !ok {
		t.Fatal("повторный поиск не сработал")
	}
	if p.legacyHits != before {
		t.Fatalf("после определения диалекта в удалённый роут ходить нельзя: %d → %d", before, p.legacyHits)
	}
}

func TestV3UnknownUserIsNotFound(t *testing.T) {
	p := newV3Panel()
	c := p.start(t)
	if _, _, ok := c.Subscription(context.Background(), 999); ok {
		t.Fatal("несуществующий пользователь не должен находиться")
	}
}

func TestV3PatchUsesNumericID(t *testing.T) {
	p := newV3Panel()
	c := p.start(t)
	if _, _, err := c.CreateOrUpdateUser(context.Background(), 42, 1, 0, UserLimits{TrafficBytes: 10}); err != nil {
		t.Fatal(err)
	}
	if len(p.patches) != 1 {
		t.Fatalf("patches = %v", p.patches)
	}
	if _, ok := p.patches[0]["uuid"]; ok {
		t.Fatalf("на панели 3.x uuid не существует: %v", p.patches[0])
	}
	if id, ok := p.patches[0]["id"].(float64); !ok || int64(id) != 77 {
		t.Fatalf("пользователь адресован не числовым id: %v", p.patches[0])
	}
	if len(p.resets) != 1 || p.resets[0] != "77" {
		t.Fatalf("сброс трафика ушёл не по id: %v", p.resets)
	}
}

func TestV3DeleteAndDevicesUseNumericID(t *testing.T) {
	p := newV3Panel()
	c := p.start(t)

	res, found, err := c.ResetDevicesByTelegramID(context.Background(), 42)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if res.Ref.Key() != "77" {
		t.Fatalf("ref = %q", res.Ref.Key())
	}
	if len(p.revokes) != 1 || p.revokes[0] != "77" {
		t.Fatalf("revoke ушёл не по id: %v", p.revokes)
	}
	if len(p.hwidPaths) != 1 || p.hwidPaths[0] != "77" {
		t.Fatalf("список устройств запрошен не по id: %v", p.hwidPaths)
	}
	if len(p.hwidBodies) != 1 {
		t.Fatalf("delete-all не вызван: %v", p.hwidBodies)
	}
	if _, ok := p.hwidBodies[0]["userUuid"]; ok {
		t.Fatalf("тело delete-all должно быть с userId: %v", p.hwidBodies[0])
	}
	if id, ok := p.hwidBodies[0]["userId"].(float64); !ok || int64(id) != 77 {
		t.Fatalf("тело delete-all = %v", p.hwidBodies[0])
	}

	if ok, err := c.DeleteByTelegramID(context.Background(), 42); err != nil || !ok {
		t.Fatalf("удаление: ok=%v err=%v", ok, err)
	}
	if len(p.deleted) != 1 || p.deleted[0] != "77" {
		t.Fatalf("DELETE ушёл не по id: %v", p.deleted)
	}
}

func TestV3AddSubCreatedAndPatchedByID(t *testing.T) {
	p := newV3Panel()
	c := p.start(t)
	// B ещё нет (by-username отвечает 404) — создаём.
	res, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{TrafficBytes: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Done || len(p.posts) != 1 {
		t.Fatalf("B не создан: res=%+v posts=%v", res, p.posts)
	}
	if got, _ := p.posts[0]["username"].(string); got != "tg_42_addsub" {
		t.Fatalf("username B = %v", p.posts[0])
	}
	if _, ok := p.posts[0]["uuid"]; ok {
		t.Fatalf("в теле создания не должно быть uuid: %v", p.posts[0])
	}
}

func TestProbeIgnoresCatchAllOK(t *testing.T) {
	// Прокси/заглушка, отвечающая 200 на что угодно, не должна убеждать бота,
	// что перед ним панель 3.x — иначе все запросы уйдут в несуществующий роут.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	if g := c.probeGen(context.Background()); g == genV3 {
		t.Fatalf("диалект определён по пустому 200: %v", g)
	}
}

func TestProbeRateLimited(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	if g := c.probeGen(context.Background()); g != genLegacy {
		t.Fatalf("404 на /users/stream = панель до 3.0.0, получили %v", g)
	}
	for i := 0; i < 5; i++ {
		c.probeGen(context.Background())
	}
	if hits != 1 {
		t.Fatalf("пробник должен быть закэширован, запросов: %d", hits)
	}
}

// nestNotFound — ответ фреймворка на несуществующий маршрут.
func nestNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"message":"Cannot GET ` + r.URL.Path + `","error":"Not Found","statusCode":404}`))
}

func TestV3StreamIgnoringFilterMustNotLeakAnotherUser(t *testing.T) {
	// Панель (или что-то перед ней) не поддержала фильтр telegramId и отдала
	// первую страницу всех пользователей. Чужую подписку показывать нельзя.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/by-telegram-id/", nestNotFound)
	mux.HandleFunc("/api/users/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":{"users":[{"id":9,"username":"someone_else","telegramId":777,"status":"ACTIVE","subscriptionUrl":"https://x/CHUZHAYA"}],"nextCursor":null,"hasMore":false}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	if url, _, ok := c.Subscription(context.Background(), 42); ok {
		t.Fatalf("отдана чужая подписка: %q", url)
	}
}

func TestV3RouteGoneWithUndecidedProbeIsAnError(t *testing.T) {
	// Старый роут удалён, а пробник упирается в 429 — версия неизвестна.
	// Ответить «подписки нет» тут нельзя: продление создаст второй аккаунт.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/by-telegram-id/", nestNotFound)
	mux.HandleFunc("/api/users/stream", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	if _, err := c.findByTelegram(context.Background(), 42); err == nil {
		t.Fatal("неопределённая версия должна быть ошибкой, а не «пользователя нет»")
	}
}

func TestProbeRateLimitedWhileUndecided(t *testing.T) {
	var probes int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"User not found","errorCode":"A001"}`))
	})
	mux.HandleFunc("/api/users/stream", func(w http.ResponseWriter, r *http.Request) {
		probes++
		w.WriteHeader(http.StatusInternalServerError) // ничего не проясняет
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	for i := 0; i < 5; i++ {
		if u, err := c.findByTelegram(context.Background(), 42); u != nil || err != nil {
			t.Fatalf("ожидали «пользователя нет»: u=%v err=%v", u, err)
		}
	}
	if probes != 1 {
		t.Fatalf("неудачный пробник тоже должен кэшироваться, запросов: %d", probes)
	}
}

func TestPanelRollbackToLegacyIsPickedUp(t *testing.T) {
	// Панель откатили с 3.x на 2.x под работающим ботом.
	v3 := true
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
		if v3 {
			nestNotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":[{"uuid":"u-1","username":"tg_42","telegramId":42,"status":"ACTIVE","subscriptionUrl":"https://x/old"}]}`))
	})
	mux.HandleFunc("/api/users/stream", func(w http.ResponseWriter, r *http.Request) {
		if !v3 {
			nestNotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":{"users":[{"id":77,"username":"tg_42","telegramId":42,"status":"ACTIVE","subscriptionUrl":"https://x/new"}],"nextCursor":null,"hasMore":false}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	if url, _, ok := c.Subscription(context.Background(), 42); !ok || url != "https://x/new" {
		t.Fatalf("3.x: url=%q ok=%v", url, ok)
	}
	v3 = false
	if url, _, ok := c.Subscription(context.Background(), 42); !ok || url != "https://x/old" {
		t.Fatalf("после отката бот должен вернуться на старый роут: url=%q ok=%v", url, ok)
	}
}

func TestV3DevicesUseNumericID(t *testing.T) {
	p := newV3Panel()
	c := p.start(t)
	info, ok := c.DevicesByTelegramID(context.Background(), 42)
	if !ok || info.Used != 2 || info.Limit != 3 {
		t.Fatalf("устройства не прочитались на 3.x: %+v ok=%v", info, ok)
	}
	if len(p.hwidPaths) != 1 || p.hwidPaths[0] != "77" {
		t.Fatalf("список устройств запрошен не по id: %v", p.hwidPaths)
	}
}

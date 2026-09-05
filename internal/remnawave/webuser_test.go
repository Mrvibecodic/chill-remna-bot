package remnawave

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

// webPanel повторяет панель 3.x там, где она строже фейка из panelv3_test:
// фильтр stream описан как неотрицательное число и на отрицательном отвечает
// 400 (глобальная валидация), а не пустым списком. На таких id держатся
// аккаунты веб-кабинета, заведённые по почте.
type webPanel struct {
	mu sync.Mutex

	users map[string]string // username -> JSON пользователя

	streamTG  []string
	nameHits  []string
	posts     []map[string]any
	patches   []map[string]any
	badFilter int
}

func newWebPanel() *webPanel {
	return &webPanel{users: map[string]string{}}
}

func (p *webPanel) add(username string, telegramID int64) {
	exp := time.Now().UTC().Add(720 * time.Hour).Format(time.RFC3339)
	p.users[username] = `{"id":77,"shortUuid":"sh","username":"` + username +
		`","telegramId":` + strconv.FormatInt(telegramID, 10) + `,"tag":"` + BotTag +
		`","status":"ACTIVE","expireAt":"` + exp + `","subscriptionUrl":"https://example.test/s"}`
}

func (p *webPanel) start(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Cannot GET ` + r.URL.Path + `","error":"Not Found","statusCode":404}`))
	})
	mux.HandleFunc("/api/users/stream", func(w http.ResponseWriter, r *http.Request) {
		tg := r.URL.Query().Get("telegramId")
		p.mu.Lock()
		p.streamTG = append(p.streamTG, tg)
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n, err := strconv.ParseInt(tg, 10, 64); tg != "" && (err != nil || n < 0) {
			p.mu.Lock()
			p.badFilter++
			p.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"statusCode":400,"message":"Validation failed",` +
				`"errors":[{"path":["telegramId"],"message":"Too small: expected number to be >=0"}]}`))
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, u := range p.users {
			if strings.Contains(u, `"telegramId":`+tg+`,`) {
				_, _ = w.Write([]byte(`{"response":{"users":[` + u + `],"nextCursor":null,"hasMore":false}}`))
				return
			}
		}
		_, _ = w.Write([]byte(`{"response":{"users":[],"nextCursor":null,"hasMore":false}}`))
	})
	mux.HandleFunc("/api/users/by-username/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/users/by-username/")
		p.mu.Lock()
		p.nameHits = append(p.nameHits, name)
		u, ok := p.users[name]
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"User not found","statusCode":404}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":` + u + `}`))
	})
	mux.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		p.mu.Lock()
		switch r.Method {
		case http.MethodPost:
			p.posts = append(p.posts, body)
			if name, _ := body["username"].(string); name != "" {
				exp := time.Now().UTC().Add(720 * time.Hour).Format(time.RFC3339)
				p.users[name] = `{"id":78,"shortUuid":"sh2","username":"` + name +
					`","telegramId":0,"tag":"` + BotTag + `","status":"ACTIVE","expireAt":"` + exp +
					`","subscriptionUrl":"https://example.test/new"}`
			}
		case http.MethodPatch:
			p.patches = append(p.patches, body)
		}
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"response":{"id":78,"shortUuid":"sh2","username":"x","status":"ACTIVE",` +
			`"expireAt":"` + time.Now().UTC().Add(720*time.Hour).Format(time.RFC3339) +
			`","subscriptionUrl":"https://example.test/new"}}`))
	})
	mux.HandleFunc("/api/users/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

const webTgID int64 = -1757000000000000000

// Аккаунт кабинета уже есть в панели: продление обязано его найти и продлить,
// а не упереться в отказ фильтра и не завести второй аккаунт.
func TestWebAccountRenewalFindsExisting(t *testing.T) {
	p := newWebPanel()
	p.add(botUsername(webTgID), webTgID)
	c := p.start(t)

	link, expire, err := c.CreateOrUpdateUser(context.Background(), webTgID, 1, 0, UserLimits{})
	if err != nil {
		t.Fatalf("продление аккаунта кабинета не прошло: %v", err)
	}
	if link == "" || expire == "" {
		t.Fatalf("пустая выдача: link=%q expire=%q", link, expire)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.badFilter != 0 {
		t.Fatalf("отрицательный id всё ещё уходит в фильтр панели: %v", p.streamTG)
	}
	if len(p.posts) != 0 {
		t.Fatalf("создан второй аккаунт вместо продления: %v", p.posts)
	}
	if len(p.patches) != 1 {
		t.Fatalf("ожидали один PATCH продления, получили %d", len(p.patches))
	}
}

// Аккаунта в панели ещё нет: первая покупка обязана его создать под тем же
// именем, по которому его потом ищут.
func TestWebAccountFirstPurchaseCreates(t *testing.T) {
	p := newWebPanel()
	c := p.start(t)

	if _, _, err := c.CreateOrUpdateUser(context.Background(), webTgID, 1, 0, UserLimits{}); err != nil {
		t.Fatalf("первая покупка из кабинета не прошла: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.badFilter != 0 {
		t.Fatalf("отрицательный id ушёл в фильтр панели: %v", p.streamTG)
	}
	if len(p.posts) != 1 {
		t.Fatalf("ожидали одно создание, получили %d", len(p.posts))
	}
	if got, _ := p.posts[0]["username"].(string); got != botUsername(webTgID) {
		t.Fatalf("имя созданного аккаунта %q != %q", got, botUsername(webTgID))
	}
	// Отрицательный идентификатор в панель не уходит: панель разбирает JSON
	// числом с плавающей точкой и сохранила бы округлённое значение.
	if _, ok := p.posts[0]["telegramId"]; ok {
		t.Fatalf("синтетический id ушёл в панель: %v", p.posts[0]["telegramId"])
	}
}

// У обычного telegram-аккаунта идентификатор в панель уходит по-прежнему.
func TestTelegramAccountKeepsID(t *testing.T) {
	p := newWebPanel()
	c := p.start(t)

	if _, _, err := c.CreateOrUpdateUser(context.Background(), 42, 1, 0, UserLimits{}); err != nil {
		t.Fatalf("покупка обычного пользователя: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.posts) != 1 {
		t.Fatalf("ожидали одно создание, получили %d", len(p.posts))
	}
	if got, _ := p.posts[0]["telegramId"].(float64); int64(got) != 42 {
		t.Fatalf("telegramId не ушёл в панель: %v", p.posts[0]["telegramId"])
	}
}

// «Нет такого маршрута» (отвечает не панель, а что-то перед ней) не должно
// выглядеть как «нет такого пользователя»: иначе продление заведёт второй
// аккаунт и оплаченная подписка потеряется.
func TestWebAccountRouteGoneIsNotMissingUser(t *testing.T) {
	mux := http.NewServeMux()
	var posts int
	mux.HandleFunc("/api/users/by-username/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Cannot GET ` + r.URL.Path + `","error":"Not Found","statusCode":404}`))
	})
	mux.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"response":{"id":78,"username":"x","status":"ACTIVE","subscriptionUrl":"https://example.test/n"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	if _, _, err := c.CreateOrUpdateUser(context.Background(), webTgID, 1, 0, UserLimits{}); err == nil {
		t.Fatal("пропавший маршрут принят за отсутствие пользователя")
	}
	if posts != 0 {
		t.Fatalf("на пропавшем маршруте создан аккаунт (%d раз)", posts)
	}
}

// Остальные операции такого аккаунта тоже идут через поиск по telegram id —
// проверяем, что и они больше не упираются в отказ фильтра.
func TestWebAccountSubscriptionAndDevices(t *testing.T) {
	p := newWebPanel()
	p.add(botUsername(webTgID), webTgID)
	c := p.start(t)

	if _, _, ok := c.Subscription(context.Background(), webTgID); !ok {
		t.Fatal("подписка аккаунта кабинета не найдена")
	}
	if _, err := c.DisableByTelegramID(context.Background(), webTgID); err != nil {
		t.Fatalf("отключение аккаунта кабинета: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.badFilter != 0 {
		t.Fatalf("отрицательный id ушёл в фильтр панели: %v", p.streamTG)
	}
}

// Обычные telegram-аккаунты обязаны ходить прежним путём — через фильтр.
func TestPositiveIDStillUsesStream(t *testing.T) {
	p := newWebPanel()
	p.add("tg_42", 42)
	c := p.start(t)

	if _, _, ok := c.Subscription(context.Background(), 42); !ok {
		t.Fatal("подписка обычного пользователя не найдена")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.streamTG) == 0 {
		t.Fatal("поиск по telegramId больше не используется для обычных аккаунтов")
	}
	for _, n := range p.nameHits {
		if n == "tg_42" {
			t.Fatal("обычный аккаунт ищется по имени вместо фильтра")
		}
	}
}

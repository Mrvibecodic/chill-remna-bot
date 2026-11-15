package remnawave

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"remnabot/internal/model"
)

// mailPanel — учётки по имени и запись тел POST/PATCH. Поле email проверяется
// так же, как в контракте панели: адрес не по z.email() — 400.
type mailPanel struct {
	mu     sync.Mutex
	user   string            // тело ответа by-username для основной учётки ("" — учётки нет)
	others map[string]string // прочие учётки по имени
	bodies []map[string]any
}

const mailMain = "tg_-1152921504606846983"

func (p *mailPanel) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/users/by-username/"+mailMain && p.user != "":
			_, _ = w.Write([]byte(`{"response":` + p.user + `}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/users/by-username/") &&
			p.others[strings.TrimPrefix(r.URL.Path, "/api/users/by-username/")] != "":
			_, _ = w.Write([]byte(`{"response":` + p.others[strings.TrimPrefix(r.URL.Path, "/api/users/by-username/")] + `}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/users/stream":
			_, _ = w.Write([]byte(`{"response":{"users":[]}}`))
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"User not found","errorCode":"A063"}`))
		default:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			if e, ok := body["email"]; ok && e != nil {
				if s, _ := e.(string); !PanelEmailOK(s) {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"message":"Validation failed","errorCode":"A000"}`))
					return
				}
			}
			p.bodies = append(p.bodies, body)
			_, _ = w.Write([]byte(`{"response":{"uuid":"u-1","id":9,"username":"tg_x","subscriptionUrl":"https://example.test/s"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (p *mailPanel) client(t *testing.T, id int64, email string) *Client {
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: p.start(t).URL, APIToken: "t"})
	c.SetAccountEmail(func(_ context.Context, got int64) string {
		if got == id {
			return email
		}
		return ""
	})
	return c
}

func (p *mailPanel) emails() []any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []any
	for _, b := range p.bodies {
		out = append(out, b["email"])
	}
	return out
}

func TestNewUserGetsAccountEmail(t *testing.T) {
	for _, id := range []int64{emailAcc, 1000000001} {
		p := &mailPanel{}
		if _, _, err := p.client(t, id, "client@example.com").CreateOrUpdateUser(context.Background(), id, 1, 0, UserLimits{}); err != nil {
			t.Fatalf("id %d: создание: %v", id, err)
		}
		if got := p.emails(); len(got) != 1 || got[0] != "client@example.com" {
			t.Fatalf("id %d: почта в теле создания: %v", id, got)
		}
	}
}

// Адрес, который панель не примет, не пишется — и не срывает покупку.
func TestNewUserSkipsEmailPanelRejects(t *testing.T) {
	p := &mailPanel{}
	if _, _, err := p.client(t, emailAcc, "иван@почта.рф").CreateOrUpdateUser(context.Background(), emailAcc, 1, 0, UserLimits{}); err != nil {
		t.Fatalf("создание: %v", err)
	}
	if got := p.emails(); len(got) != 1 || got[0] != nil {
		t.Fatalf("непринимаемый адрес ушёл в панель: %v", got)
	}
}

// Продление дописывает почту в пустое поле и не трогает заполненное.
func TestExtendFillsOnlyEmptyEmail(t *testing.T) {
	for _, tc := range []struct {
		have, want any
	}{{"", "client@example.com"}, {"other@example.com", nil}} {
		p := &mailPanel{user: `{"uuid":"u-1","id":9,"username":"tg_-1152921504606846983","tag":"` + BotTag +
			`","email":"` + tc.have.(string) + `","expireAt":"2030-01-01T00:00:00Z"}`}
		if _, _, err := p.client(t, emailAcc, "client@example.com").CreateOrUpdateUser(context.Background(), emailAcc, 1, 0, UserLimits{}); err != nil {
			t.Fatalf("продление: %v", err)
		}
		if got := p.emails(); len(got) == 0 || got[0] != tc.want {
			t.Fatalf("было %q: почта в продлении %v, ждали %v", tc.have, got, tc.want)
		}
	}
}

func TestFillEmail(t *testing.T) {
	const own = `"username":"tg_-1152921504606846983","tag":"` + BotTag + `"`
	for _, tc := range []struct {
		name, user, email string
		writes            int
	}{
		{"пустое поле", `{"uuid":"u-1",` + own + `,"email":""}`, "client@example.com", 1},
		{"своя почта уже есть", `{"uuid":"u-1",` + own + `,"email":"client@example.com"}`, "client@example.com", 0},
		{"чужая почта", `{"uuid":"u-1",` + own + `,"email":"other@example.com"}`, "client@example.com", 0},
		{"учётка не бота", `{"uuid":"u-1","username":"client","email":""}`, "client@example.com", 0},
		{"учётки нет", "", "client@example.com", 0},
		{"почты нет", `{"uuid":"u-1",` + own + `,"email":""}`, "", 0},
	} {
		p := &mailPanel{user: tc.user}
		if err := p.client(t, emailAcc, tc.email).FillEmail(context.Background(), emailAcc, ""); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := p.emails()
		if len(got) != tc.writes || (tc.writes == 1 && got[0] != tc.email) {
			t.Fatalf("%s: записи %v", tc.name, got)
		}
	}
}

// Доп-подписка получает почту основной учётки: и когда основная её только что
// получила, и когда почта там уже была. Заполненную почту доп-подписки бот не
// меняет.
func TestFillEmailAddSub(t *testing.T) {
	const own = `"username":"` + mailMain + `","tag":"` + BotTag + `"`
	addSub := func(email string) map[string]string {
		return map[string]string{mailMain + "_addsub": `{"uuid":"b-1","username":"` + mailMain +
			`_addsub","tag":"` + BotTagAdd + `","email":"` + email + `"}`}
	}
	for _, tc := range []struct {
		name, mainEmail, addEmail string
		want                      []any
	}{
		{"обе пустые", "", "", []any{"client@example.com", "client@example.com"}},
		{"у основной своя", "client@example.com", "", []any{"client@example.com"}},
		{"у основной другая", "other@example.com", "", []any{"other@example.com"}},
		{"у доп-подписки уже есть", "", "other@example.com", []any{"client@example.com"}},
	} {
		p := &mailPanel{user: `{"uuid":"u-1",` + own + `,"email":"` + tc.mainEmail + `"}`, others: addSub(tc.addEmail)}
		if err := p.client(t, emailAcc, "client@example.com").FillEmail(context.Background(), emailAcc, "_addsub"); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := p.emails(); !slices.Equal(got, tc.want) {
			t.Fatalf("%s: записи %v, ждали %v", tc.name, got, tc.want)
		}
	}
}

// Доп-подписка при создании и продлении берёт почту основной учётки и не
// трогает свою, если она уже есть.
func TestUpsertAddSubMirrorsMainEmail(t *testing.T) {
	for _, tc := range []struct {
		name, mainEmail string
		addSub          string // "" — доп-подписки нет, иначе её почта ("-" — пустая)
		want            any
	}{
		{"создание", "client@example.com", "", "client@example.com"},
		{"создание без почты", "", "", nil},
		{"продление, поле пустое", "client@example.com", "-", "client@example.com"},
		{"продление, своя почта", "client@example.com", "other@example.com", nil},
	} {
		p := newAddSubPanel("tg_42")
		p.mainEmail = tc.mainEmail
		if tc.addSub != "" {
			e := strings.TrimPrefix(tc.addSub, "-")
			p.users["tg_42_addsub"] = `{"uuid":"b-uuid","username":"tg_42_addsub","tag":"` + BotTagAdd +
				`","email":"` + e + `","status":"ACTIVE","expireAt":"` + p.mainExpire + `"}`
		}
		if _, err := addSubClient(t, p).UpsertAddSub(context.Background(), 42, AddSubOptions{}); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		writes := append(p.posts, p.patches...)
		if len(writes) != 1 || writes[0]["email"] != tc.want {
			t.Fatalf("%s: записи %v, ждали почту %v", tc.name, writes, tc.want)
		}
	}
}

// Проверка повторяет z.email() из контракта панели (zod 4.6.7).
func TestPanelEmailOK(t *testing.T) {
	for e, want := range map[string]bool{
		"ivan@example.com": true, "Ivan.P+tag@sub.example.co": true, "o'neil@example.com": true,
		"иван@почта.рф": false, "a..b@x.yz": false, "a@b.c": false, "user!x@example.com": false,
		".a@example.com": false, "a.@example.com": false, "a@x.com.": false, "": false,
	} {
		if PanelEmailOK(e) != want {
			t.Errorf("%q: ждали %v", e, want)
		}
	}
}

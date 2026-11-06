package remnawave

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"remnabot/internal/model"
)

const emailAcc int64 = -(1<<60 + 7)

// emailFake отвечает на поиск по имени «нет такого», а на поиск по почте — тем,
// что задаст тест. Всё, что пишет, считается.
type emailFake struct {
	mu        sync.Mutex
	legacy    bool   // панель до 3.0.0: есть /by-email, нет фильтра email в stream
	answer    string // тело ответа на поиск по почте
	status    int
	emailHits int
	writes    []string
}

func (f *emailFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case r.Method != http.MethodGet:
			f.writes = append(f.writes, r.Method+" "+p)
			_, _ = w.Write([]byte(`{"response":{"uuid":"u-new","id":9,"username":"tg_x","subscriptionUrl":"https://example.test/s"}}`))
		case strings.HasPrefix(p, "/api/users/by-username/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"User not found","errorCode":"A063"}`))
		case strings.HasPrefix(p, "/api/users/by-email/"):
			if !f.legacy {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Cannot GET ` + p + `"}`))
				return
			}
			f.emailHits++
			if f.status != 0 {
				w.WriteHeader(f.status)
			}
			_, _ = w.Write([]byte(f.answer))
		case p == "/api/users/stream":
			if r.URL.Query().Get("email") == "" {
				// проба версии
				if f.legacy {
					_, _ = w.Write([]byte(`{"response":{"users":[{"uuid":"u-a","username":"a"}]}}`))
				} else {
					_, _ = w.Write([]byte(`{"response":{"users":[]}}`))
				}
				return
			}
			f.emailHits++
			if f.status != 0 {
				w.WriteHeader(f.status)
			}
			_, _ = w.Write([]byte(`{"response":{"users":` + f.answer + `}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Cannot GET ` + p + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func emailClient(t *testing.T, f *emailFake, email string) *Client {
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: f.server(t).URL, APIToken: "t"})
	c.SetEmailResolver(func(_ context.Context, id int64) string {
		if id == emailAcc {
			return email
		}
		return ""
	})
	return c
}

func TestFindByAccountEmail(t *testing.T) {
	const mine = `{"uuid":"","id":5,"username":"client","email":"client@example.com","subscriptionUrl":"https://example.test/s/5"}`
	cases := []struct {
		name    string
		legacy  bool
		answer  string
		status  int
		email   string
		wantID  int64
		found   bool
		wantErr bool
		hits    int
	}{
		{name: "3.x нашли", answer: `[` + mine + `]`, email: "client@example.com", wantID: 5, found: true, hits: 1},
		{name: "2.x нашли", legacy: true, answer: `{"response":[{"uuid":"u-5","username":"client","email":"Client@Example.com"}]}`, email: "client@example.com", found: true, hits: 1},
		{name: "нет учётки", answer: `[]`, email: "client@example.com", hits: 1},
		{name: "почта не подтверждена — панель не спрашиваем", answer: `[` + mine + `]`, email: "", hits: 0},
		{name: "панель не приняла адрес", answer: `{}`, status: http.StatusBadRequest, email: "client@example.com", hits: 1},
		{name: "две учётки с почтой", answer: `[` + mine + `,` + strings.Replace(mine, `"id":5`, `"id":6`, 1) + `]`, email: "client@example.com", wantErr: true, hits: 1},
		{name: "учётка живого Telegram не наша", answer: `[` + strings.Replace(mine, `"id":5`, `"id":5,"telegramId":1000000001`, 1) + `]`, email: "client@example.com", hits: 1},
		{name: "доп-подписка не наша", answer: `[` + strings.Replace(mine, `"id":5`, `"id":5,"tag":"CHILLBOT_ADD"`, 1) + `]`, email: "client@example.com", hits: 1},
		{name: "фильтр не применён", answer: `[{"id":8,"username":"x","email":"x@example.com"}]`, email: "client@example.com", wantErr: true, hits: 1},
		{name: "панель упала", answer: `{}`, status: http.StatusInternalServerError, email: "client@example.com", wantErr: true, hits: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &emailFake{legacy: c.legacy, answer: c.answer, status: c.status}
			cl := emailClient(t, f, c.email)
			u, err := cl.FindByTelegramID(context.Background(), emailAcc)
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v, ждали ошибку=%v", err, c.wantErr)
			}
			found := u != nil
			wantFound := c.found
			if found != wantFound {
				t.Fatalf("найдено=%v (%+v), ждали %v", found, u, wantFound)
			}
			if c.wantID != 0 && u.ID != c.wantID {
				t.Fatalf("не та учётка: %+v", u)
			}
			if f.emailHits != c.hits {
				t.Fatalf("запросов по почте %d, ждали %d", f.emailHits, c.hits)
			}
			if len(f.writes) > 0 {
				t.Fatalf("поиск писал в панель: %v", f.writes)
			}
		})
	}
}

// Новичок с подтверждённой почтой, у которого в панели ничего нет, покупает
// как раньше — создаётся его учётка.
func TestEmailAccountWithoutPanelUserStillBuys(t *testing.T) {
	f := &emailFake{answer: `[]`}
	cl := emailClient(t, f, "client@example.com")
	if _, _, err := cl.CreateOrUpdateUser(context.Background(), emailAcc, 1, 0, UserLimits{}); err != nil {
		t.Fatalf("покупка: %v", err)
	}
	if len(f.writes) != 1 || !strings.HasPrefix(f.writes[0], "POST") {
		t.Fatalf("ждали одно создание: %v", f.writes)
	}
}

// Найденная по почте учётка продлевается, а не заводится вторая.
func TestEmailLinkedAccountIsExtended(t *testing.T) {
	f := &emailFake{answer: `[{"uuid":"","id":5,"username":"client","email":"client@example.com","expireAt":"2030-01-01T00:00:00Z"}]`}
	cl := emailClient(t, f, "client@example.com")
	if _, _, err := cl.CreateOrUpdateUser(context.Background(), emailAcc, 1, 0, UserLimits{}); err != nil {
		t.Fatalf("продление: %v", err)
	}
	if len(f.writes) == 0 || f.writes[0] != "PATCH /api/users" {
		t.Fatalf("ждали продление: %v", f.writes)
	}
	for _, w := range f.writes {
		if w == "POST /api/users" || strings.HasPrefix(w, "DELETE") {
			t.Fatalf("при продлении создана или удалена учётка: %v", f.writes)
		}
	}
}

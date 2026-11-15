package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// mailGrantPanel хранит учётки целиком: созданное POST и дописанное PATCH
// видно следующему поиску, как в настоящей панели. Почту проверяет по
// контракту, отрицательный telegramId отвергает.
type mailGrantPanel struct {
	mu    sync.Mutex
	users map[string]map[string]any
}

func (p *mailGrantPanel) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p.mu.Lock()
		defer p.mu.Unlock()
		path := r.URL.Path
		reply := func(v any) {
			raw, _ := json.Marshal(map[string]any{"response": v})
			_, _ = w.Write(raw)
		}
		switch {
		case strings.Contains(path, "/by-telegram-id/-") || strings.HasPrefix(r.URL.Query().Get("telegramId"), "-"):
			w.WriteHeader(http.StatusBadRequest)
		case strings.HasSuffix(path, "/internal-squads"):
			reply(map[string]any{"internalSquads": []any{map[string]any{"uuid": "sq-vip", "name": "VIP"}, map[string]any{"uuid": "g1", "name": "Доп"}}})
		case strings.HasSuffix(path, "/external-squads"):
			reply(map[string]any{"externalSquads": []any{}})
		case strings.HasPrefix(path, "/api/users/by-username/"):
			u, ok := p.users[strings.TrimPrefix(path, "/api/users/by-username/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"User not found","errorCode":"A063"}`))
				return
			}
			reply(u)
		case strings.HasPrefix(path, "/api/users/by-email/"):
			email := strings.TrimPrefix(path, "/api/users/by-email/")
			found := []any{}
			for _, u := range p.users {
				if u["email"] == email {
					found = append(found, u)
				}
			}
			reply(found)
		case path == "/api/users" && (r.Method == http.MethodPost || r.Method == http.MethodPatch):
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			if e, ok := body["email"]; ok && e != nil {
				if s, _ := e.(string); !remnawave.PanelEmailOK(s) {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"message":"Validation failed","errorCode":"A000"}`))
					return
				}
			}
			if tg, ok := body["telegramId"].(float64); ok && tg < 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var u map[string]any
			if r.Method == http.MethodPost {
				name, _ := body["username"].(string)
				u = map[string]any{"uuid": "u-" + name, "status": "ACTIVE", "subscriptionUrl": "https://sub.example.com/" + name}
				p.users[name] = u
			} else {
				for _, cand := range p.users {
					if cand["uuid"] == body["uuid"] {
						u = cand
					}
				}
				if u == nil {
					w.WriteHeader(http.StatusNotFound)
					return
				}
			}
			for k, v := range body {
				u[k] = v
			}
			reply(u)
		default:
			reply(map[string]any{})
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (p *mailGrantPanel) email(name string) any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if u := p.users[name]; u != nil {
		return u["email"]
	}
	return "нет учётки"
}

// Выдача почтовому аккаунту кабинета через экраны админа: учётка и
// доп-подписка заводятся в панели с почтой аккаунта, а если почта ещё не
// подтверждена — получают её при подтверждении.
func TestGrant_EmailAccountPanelEmail(t *testing.T) {
	const (
		uid   int64 = -1000000041
		id          = "-1000000041"
		name        = "tg_-1000000041"
		email       = "client@example.com"
	)
	for _, verified := range []bool{true, false} {
		p := &mailGrantPanel{users: map[string]map[string]any{}}
		a, fm, fs := grantApp(t, "")
		a.panel = a.newPanel(model.PanelConfig{Mode: model.ModeRemote, BaseURL: p.start(t), APIToken: "t"})
		a.botCfg.AddSub = model.AddSubConfig{Enabled: true, Init: true, UsernameSuffix: "_addsub",
			TrafficGB: 10, InternalSquads: []string{"g1"}}
		ctx := context.Background()
		_ = fs.UpsertUser(ctx, uid)
		wu := &model.WebUser{TgID: uid, Email: email}
		if verified {
			wu.VerifiedAt = "2026-01-01T00:00:00Z"
		}
		_ = fs.CreateWebUser(ctx, wu)

		a.handleCallback(ctx, cb(100, "usr:gr:"+id))
		a.handleCallback(ctx, cb(100, "usr:grp:"+id+":"+planEditHash("vip")))
		a.handleCallback(ctx, cb(100, "usr:grt:"+id+":1"))
		a.handleCallback(ctx, cb(100, "usr:grok:"+id+":"+grantNonceFor(t, fm, id)))
		if !strings.Contains(fm.joined(), "Подписка выдана") {
			t.Fatalf("подтверждена=%v: выдача не прошла:\n%s", verified, fm.joined())
		}
		want := any(email)
		if !verified {
			want = nil
		}
		if got := p.email(name); got != want {
			t.Fatalf("подтверждена=%v: почта учётки %v, ждали %v", verified, got, want)
		}
		if got := p.email(name + "_addsub"); got != want {
			t.Fatalf("подтверждена=%v: почта доп-подписки %v, ждали %v", verified, got, want)
		}
		if verified {
			continue
		}
		tok, err := a.newEmailToken(ctx, uid, email, model.EmailPurposeVerify, verifyTTL)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.CabinetVerifyEmail(ctx, tok); err != nil {
			t.Fatalf("подтверждение: %v", err)
		}
		if p.email(name) != email || p.email(name+"_addsub") != email {
			t.Fatalf("после подтверждения: учётка %v, доп-подписка %v", p.email(name), p.email(name+"_addsub"))
		}
	}
}

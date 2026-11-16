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
		case strings.Contains(path, "/by-telegram-id/"):
			found := []any{}
			for _, u := range p.users {
				if tg, _ := u["telegramId"].(float64); tg != 0 && strconv.FormatInt(int64(tg), 10) == path[strings.LastIndex(path, "/")+1:] {
					found = append(found, u)
				}
			}
			if len(found) == 0 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"User not found","errorCode":"A063"}`))
				return
			}
			reply(found)
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
// доп-подписка заводятся в панели с почтой аккаунта. Неподтверждённая почта
// при включённых письмах впишется при подтверждении, а при выключенных —
// сразу: подтвердить её нечем.
func TestGrant_EmailAccountPanelEmail(t *testing.T) {
	const (
		uid   int64 = -1000000041
		id          = "-1000000041"
		name        = "tg_-1000000041"
		email       = "client@example.com"
	)
	for _, tc := range []struct{ verified, mail bool }{{true, true}, {false, true}, {false, false}} {
		verified := tc.verified
		p := &mailGrantPanel{users: map[string]map[string]any{}}
		a, fm, fs := grantApp(t, "")
		if tc.mail {
			grantMailOn(a)
		}
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
		if !verified && tc.mail {
			want = nil
		}
		if got := p.email(name); got != want {
			t.Fatalf("подтверждена=%v: почта учётки %v, ждали %v", verified, got, want)
		}
		if got := p.email(name + "_addsub"); got != want {
			t.Fatalf("подтверждена=%v: почта доп-подписки %v, ждали %v", verified, got, want)
		}
		if want != nil {
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

// Экран подтверждения и итог выдачи аккаунту по почте называют то, что
// помешает человеку воспользоваться подпиской: кабинет и неподтверждённую
// почту. Проверка модерации при этом не шлёт админу заявку на вход.
func TestGrant_EmailAccountWarnings(t *testing.T) {
	const (
		uid int64 = -1000000051
		id        = "-1000000051"
	)
	listNoMail := "письма выключены: подтвердить её нечем"
	cabOff := "Веб-кабинет выключен"
	pending := "ещё не одобрен"
	denied := "отклонён: подписку"
	unverified := "Почта аккаунта не подтверждена"
	renew := "Продлевать подписку человек сможет тоже только после подтверждения"
	cases := []struct {
		name            string
		cabinet, mail   bool
		approval        string
		approved, deny  bool
		verified, list  bool
		want, notWanted []string
	}{
		{"кабинет выключен", false, false, model.CabinetApprovalOff, false, false, true, false,
			[]string{cabOff}, []string{pending, denied, unverified}},
		{"всё в порядке", true, false, model.CabinetApprovalOff, false, false, true, false,
			nil, []string{cabOff, pending, denied, unverified}},
		{"ждёт одобрения", true, false, model.CabinetApprovalEmail, false, false, true, false,
			[]string{pending}, []string{cabOff, denied}},
		{"одобрен", true, false, model.CabinetApprovalEmail, true, false, true, false,
			nil, []string{cabOff, pending, denied}},
		{"отклонён", true, false, model.CabinetApprovalEmail, false, true, true, false,
			[]string{denied}, []string{cabOff, pending}},
		{"почта не подтверждена, письма выключены", true, false, model.CabinetApprovalOff, false, false, false, false,
			nil, []string{unverified, renew, listNoMail}},
		{"почта не подтверждена, письма выключены, тариф по списку", true, false, model.CabinetApprovalOff, false, false, false, true,
			[]string{listNoMail}, []string{unverified, renew}},
		{"почта не подтверждена, письма включены", true, true, model.CabinetApprovalOff, false, false, false, false,
			[]string{unverified, renew}, []string{listNoMail}},
		{"почта не подтверждена, письма включены, тариф по списку", true, true, model.CabinetApprovalOff, false, false, false, true,
			[]string{unverified, renew}, []string{listNoMail}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &mailGrantPanel{users: map[string]map[string]any{}}
			a, fm, fs := grantApp(t, "")
			a.panel = a.newPanel(model.PanelConfig{Mode: model.ModeRemote, BaseURL: p.start(t), APIToken: "t"})
			a.botCfg.NormalizeCabinet()
			a.botCfg.Cabinet.Enabled = tc.cabinet
			a.botCfg.Cabinet.Approval = tc.approval
			if tc.mail {
				grantMailOn(a)
			}
			ctx := context.Background()
			if tc.list {
				pl, _ := fs.GetPlan(ctx, "vip")
				pl.Availability = model.PlanAvailList
				_ = fs.SavePlan(ctx, pl)
			}
			_ = fs.UpsertUser(ctx, uid)
			_ = fs.SetWebApproved(ctx, uid, tc.approved)
			_ = fs.SetWebDenied(ctx, uid, tc.deny)
			wu := &model.WebUser{TgID: uid, Email: "client@example.com"}
			if tc.verified {
				wu.VerifiedAt = "2026-01-01T00:00:00Z"
			}
			_ = fs.CreateWebUser(ctx, wu)

			a.handleCallback(ctx, cb(100, "usr:gr:"+id))
			a.handleCallback(ctx, cb(100, "usr:grp:"+id+":"+planEditHash("vip")))
			a.handleCallback(ctx, cb(100, "usr:grt:"+id+":1"))
			confirm := fm.last()
			a.handleCallback(ctx, cb(100, "usr:grok:"+id+":"+grantNonceFor(t, fm, id)))
			all := fm.joined()
			report := all[strings.LastIndex(all, "Подписка выдана"):]
			for _, screen := range []struct{ name, text string }{{"подтверждение", confirm}, {"итог", report}} {
				for _, w := range tc.want {
					if !strings.Contains(screen.text, w) {
						t.Errorf("%s: нет «%s»:\n%s", screen.name, w, screen.text)
					}
				}
				for _, w := range tc.notWanted {
					if strings.Contains(screen.text, w) {
						t.Errorf("%s: лишнее «%s»:\n%s", screen.name, w, screen.text)
					}
				}
			}
			for _, d := range fm.allCallbackData() {
				if strings.HasPrefix(d, "adm:wok:") {
					t.Fatal("проверка модерации отправила админу заявку на вход")
				}
			}
		})
	}
}

// grantMailOn включает письма кабинета: без них почту подтвердить нечем.
func grantMailOn(a *App) {
	a.botCfg.Webhook.PublicBaseURL = "https://example.com"
	a.botCfg.Mail = model.MailConfig{Enabled: true, Mode: model.MailModeSMTP,
		From: "noreply@example.com", Host: "127.0.0.1", Port: 1}
	a.botCfg.NormalizeMail()
}

// Подписка, взятая на почтовый аккаунт, переживает привязку Telegram: в
// панели остаётся та же учётка — то же имя, та же ссылка, — ей лишь
// проставляется telegramId, и следующее продление идёт в неё же.
func TestEmailAccountKeepsPanelUserAfterTelegramBind(t *testing.T) {
	const (
		uid  int64 = -1000000061
		tg   int64 = 1000000062
		name       = "tg_-1000000061"
	)
	p := &mailGrantPanel{users: map[string]map[string]any{}}
	a, fm, fs := grantApp(t, "")
	a.panel = a.newPanel(model.PanelConfig{Mode: model.ModeRemote, BaseURL: p.start(t), APIToken: "t"})
	a.botCfg.NormalizeCabinet()
	a.botCfg.Cabinet.Enabled = true
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, uid)
	_ = fs.CreateWebUser(ctx, &model.WebUser{TgID: uid, Email: "client@example.com", VerifiedAt: "2026-01-01T00:00:00Z"})

	grant := func(id int64) {
		s := strconv.FormatInt(id, 10)
		a.handleCallback(ctx, cb(100, "usr:gr:"+s))
		a.handleCallback(ctx, cb(100, "usr:grp:"+s+":"+planEditHash("vip")))
		a.handleCallback(ctx, cb(100, "usr:grt:"+s+":1"))
		a.handleCallback(ctx, cb(100, "usr:grok:"+s+":"+grantNonceFor(t, fm, s)))
	}
	snapshot := func() (n int, uuid, url, exp string, tgID any) {
		p.mu.Lock()
		defer p.mu.Unlock()
		u := p.users[name]
		if u == nil {
			return len(p.users), "", "", "", nil
		}
		uuid, _ = u["uuid"].(string)
		url, _ = u["subscriptionUrl"].(string)
		exp, _ = u["expireAt"].(string)
		return len(p.users), uuid, url, exp, u["telegramId"]
	}

	grant(uid)
	n0, uuid0, url0, exp0, _ := snapshot()
	if n0 != 1 || uuid0 == "" {
		t.Fatalf("после покупки по почте в панели %d учёток, учётки %s нет", n0, name)
	}
	if got, err := a.CabinetBindTelegram(ctx, uid, tg); err != nil || got != tg {
		t.Fatalf("привязка Telegram: %d, %v", got, err)
	}
	n1, uuid1, url1, _, tg1 := snapshot()
	if n1 != 1 || uuid1 != uuid0 || url1 != url0 || tg1 != float64(tg) {
		t.Fatalf("после привязки: учёток %d, uuid %s→%s, ссылка %s→%s, telegramId %v", n1, uuid0, uuid1, url0, url1, tg1)
	}
	grant(tg)
	n2, uuid2, url2, exp2, _ := snapshot()
	if n2 != 1 || uuid2 != uuid0 || url2 != url0 {
		t.Fatalf("продление после привязки завело другую учётку: учёток %d, uuid %s→%s, ссылка %s→%s", n2, uuid0, uuid2, url0, url2)
	}
	if exp2 <= exp0 {
		t.Fatalf("продление не легло в ту же учётку: срок %s → %s", exp0, exp2)
	}
	if p.email(name) != "client@example.com" {
		t.Fatalf("почта учётки после привязки: %v", p.email(name))
	}
}

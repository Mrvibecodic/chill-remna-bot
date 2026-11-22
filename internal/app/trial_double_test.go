package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"remnabot/internal/model"
)

// emptyPanelCountingGrants — панель, которая не знает ни одного пользователя и
// считает создания/продления учётки. Любой поиск отвечает пустым списком.
func emptyPanelCountingGrants(t *testing.T, grants *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_, _ = w.Write([]byte(`{"response":[]}`))
			return
		}
		if (r.Method == http.MethodPatch || r.Method == http.MethodPost) && strings.HasSuffix(r.URL.Path, "/api/users") {
			*grants++
			_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/x","expireAt":"2030-01-01T00:00:00Z"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// noRowCreateStore — SetSubExpiry, как в настоящей базе, не заводит строку.
type noRowCreateStore struct{ *fakeStore }

func (s noRowCreateStore) SetSubExpiry(ctx context.Context, id int64, expireAt, kind string) error {
	if u, _ := s.fakeStore.GetUser(ctx, id); u == nil {
		return nil
	}
	return s.fakeStore.SetSubExpiry(ctx, id, expireAt, kind)
}

// Гость мини-аппа без строки в users не получает триал второй раз.
func TestMiniTrial_NoDoubleGrantWithoutUserRow(t *testing.T) {
	ctx := context.Background()
	grants := 0
	srv := emptyPanelCountingGrants(t, &grants)
	a, fs := snapApp(t, srv.URL)
	a.store = noRowCreateStore{fs}
	const u int64 = 556
	a.botCfg.Trial = model.TrialConfig{Enabled: true, Days: 3}

	if r := a.MiniTrial(ctx, u); r.Error != "" || !r.OK {
		t.Fatalf("первый триал не выдан: %+v", r)
	}
	if r := a.MiniTrial(ctx, u); r.Error == "" {
		t.Fatalf("второй триал выдан повторно: %+v", r)
	}
	if grants != 1 {
		t.Fatalf("панель выдала триал %d раз(а), ожидался один", grants)
	}
	if u2, _ := fs.GetUser(ctx, u); u2 == nil || u2.TrialUsedAt == "" {
		t.Fatalf("триал не отмечен в базе: %+v", u2)
	}
}

// heldPanel — в панели у человека уже лежит учётка username. Пока down, панель
// не отвечает. Считает выдачи.
type heldPanel struct {
	mu       sync.Mutex
	down     bool
	id       int64
	username string
	expireAt string
	grants   int
}

func (p *heldPanel) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case p.down:
			w.WriteHeader(http.StatusBadGateway)
		case strings.Contains(r.URL.Path, "/by-telegram-id/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"response": []map[string]any{{
				"uuid": "u1", "telegramId": p.id, "username": p.username,
				"subscriptionUrl": "https://sub/x", "expireAt": p.expireAt,
			}}})
		case (r.Method == http.MethodPatch || r.Method == http.MethodPost) && strings.HasSuffix(r.URL.Path, "/api/users"):
			p.grants++
			_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/x","expireAt":"2030-01-01T00:00:00Z"}}`))
		default:
			_, _ = w.Write([]byte(`{"response":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// База не помнит учётку из панели, а поиск при входе пришёлся на сбой панели.
// Триал выдаётся только поверх истёкшей чужой учётки или истёкшей своей после
// возврата триала; остальные привязываются.
func TestMiniTrial_HeldPanelAccount(t *testing.T) {
	const u int64 = 556
	own := fmt.Sprintf("tg_%d", u)
	alive := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	for _, c := range []struct {
		name     string
		username string
		expireAt string
		returned bool
		grants   int
	}{
		{"учётка бота, подписка жива", own, alive, false, 0},
		{"учётка бота, срок истёк", own, past, false, 0},
		{"учётка бота, триал возвращён", own, past, true, 1},
		{"учётка бота, триал возвращён, подписка жива", own, alive, true, 0},
		{"чужая учётка, подписка жива", "old_user", alive, false, 0},
		{"чужая учётка, срок истёк", "old_user", past, false, 1},
	} {
		ctx := context.Background()
		panel := &heldPanel{down: true, id: u, username: c.username, expireAt: c.expireAt}
		a, fs := snapApp(t, panel.serve(t).URL)
		a.store = noRowCreateStore{fs}
		a.botCfg.Trial = model.TrialConfig{Enabled: true, Days: 3}
		if c.returned {
			_ = fs.UpsertUser(ctx, u)
			fs.trialResets = map[int64]int{u: 1}
		}

		if r := a.MiniTrial(ctx, u); r.OK {
			t.Fatalf("%s: триал выдан при неответившей панели", c.name)
		}
		panel.mu.Lock()
		panel.down = false
		panel.mu.Unlock()
		r := a.MiniTrial(ctx, u)
		if r.OK != (c.grants == 1) || panel.grants != c.grants {
			t.Fatalf("%s: ok=%v, выдач в панели %d: %+v", c.name, r.OK, panel.grants, r)
		}
		if c.grants == 1 {
			continue
		}
		row, _ := fs.GetUser(ctx, u)
		if row == nil || row.TrialUsedAt == "" || row.SubExpireAt != c.expireAt {
			t.Fatalf("%s: учётка не привязана: %+v", c.name, row)
		}
		if found := strings.Contains(r.Error, "нашли вашу подписку"); found != (c.expireAt == alive) {
			t.Fatalf("%s: ответ покупателю: %q", c.name, r.Error)
		}
	}
}

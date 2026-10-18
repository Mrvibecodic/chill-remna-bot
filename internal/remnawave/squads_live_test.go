package remnawave

import (
	"context"
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
)

// squadPanel — панель с настраиваемыми списками сквадов. Пользователь ищется
// по имени (аккаунт кабинета), поэтому диалект панели тут не важен.
type squadPanel struct {
	mu sync.Mutex

	intBody, extBody     string
	intStatus, extStatus int
	listHits             int

	existing bool
	posts    []map[string]any
	patches  []map[string]any
}

const squadTgID int64 = -1000000001

func newSquadPanel() *squadPanel {
	return &squadPanel{
		intBody:   `{"response":{"total":1,"internalSquads":[{"uuid":"s-live","name":"Живой"}]}}`,
		extBody:   `{"response":{"total":1,"externalSquads":[{"uuid":"e-live","name":"Внешний"}]}}`,
		intStatus: http.StatusOK,
		extStatus: http.StatusOK,
	}
}

func (p *squadPanel) start(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	list := func(body *string, status *int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			p.mu.Lock()
			p.listHits++
			b, st := *body, *status
			p.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(st)
			_, _ = w.Write([]byte(b))
		}
	}
	mux.HandleFunc("/api/internal-squads", list(&p.intBody, &p.intStatus))
	mux.HandleFunc("/api/external-squads", list(&p.extBody, &p.extStatus))
	mux.HandleFunc("/api/users/by-username/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p.mu.Lock()
		ex := p.existing
		p.mu.Unlock()
		if !ex {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"User not found","errorCode":"A063"}`))
			return
		}
		exp := time.Now().UTC().Add(720 * time.Hour).Format(time.RFC3339)
		_, _ = w.Write([]byte(`{"response":{"uuid":"u-1","username":"` + botUsername(squadTgID) +
			`","tag":"` + BotTag + `","status":"ACTIVE","expireAt":"` + exp + `","subscriptionUrl":"https://example.test/s"}}`))
	})
	mux.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		p.mu.Lock()
		if r.Method == http.MethodPost {
			p.posts = append(p.posts, body)
		} else {
			p.patches = append(p.patches, body)
		}
		p.mu.Unlock()
		exp := time.Now().UTC().Add(720 * time.Hour).Format(time.RFC3339)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"uuid":"u-1","subscriptionUrl":"https://example.test/s","expireAt":"` + exp + `"}}`))
	})
	mux.HandleFunc("/api/users/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

func squadsOf(body map[string]any) []string {
	raw, ok := body["activeInternalSquads"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

// Список считается известным только при однозначном ответе панели: всё
// остальное не должно выдавать живые сквады за удалённые.
func TestLiveSquadsStrictParse(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		known  bool
		size   int
	}{
		{"обычный ответ", `{"response":{"total":2,"internalSquads":[{"uuid":"a","name":"A"},{"uuid":"b","name":"B"}]}}`, 200, true, 2},
		{"пустой список", `{"response":{"total":0,"internalSquads":[]}}`, 200, true, 0},
		{"без total", `{"response":{"internalSquads":[{"uuid":"a"}]}}`, 200, true, 1},
		{"массивом", `{"response":[{"uuid":"a"}]}`, 200, true, 1},
		{"нет ключа", `{"response":{"total":0}}`, 200, false, 0},
		{"список null", `{"response":{"internalSquads":null}}`, 200, false, 0},
		{"отдано не всё", `{"response":{"total":5,"internalSquads":[{"uuid":"a"}]}}`, 200, false, 0},
		{"сквад без uuid", `{"response":{"internalSquads":[{"name":"x"}]}}`, 200, false, 0},
		{"нет response", `{}`, 200, false, 0},
		{"ошибка панели", `{"message":"boom"}`, 500, false, 0},
		{"не JSON", `<html></html>`, 200, false, 0},
	}
	for _, c := range cases {
		p := newSquadPanel()
		p.intBody, p.intStatus = c.body, c.status
		live := p.start(t).LiveSquads(context.Background())
		if live.IntKnown != c.known || len(live.Int) != c.size {
			t.Errorf("%s: known=%v size=%d, ожидалось known=%v size=%d", c.name, live.IntKnown, len(live.Int), c.known, c.size)
		}
		if !live.ExtKnown {
			t.Errorf("%s: внешний список не разобран", c.name)
		}
		if !c.known && live.IntGone("a") {
			t.Errorf("%s: при неизвестном списке сквад объявлен удалённым", c.name)
		}
	}
}

// Новый пользователь создаётся без удалённых сквадов, живые остаются.
func TestCreateSkipsGoneSquads(t *testing.T) {
	p := newSquadPanel()
	c := p.start(t)
	_, _, err := c.CreateOrUpdateUserDays(context.Background(), squadTgID, 3, UserLimits{
		InternalSquads: []string{"s-live", "s-gone"},
		ExternalSquad:  "e-gone",
	})
	if err != nil {
		t.Fatalf("выдача не прошла: %v", err)
	}
	if len(p.posts) != 1 {
		t.Fatalf("ожидали одно создание, получили %d", len(p.posts))
	}
	if got := squadsOf(p.posts[0]); len(got) != 1 || got[0] != "s-live" {
		t.Fatalf("сквады в панель: %v, ожидался только s-live", got)
	}
	if _, ok := p.posts[0]["externalSquadUuid"]; ok {
		t.Fatalf("удалённый внешний сквад ушёл в панель: %v", p.posts[0]["externalSquadUuid"])
	}
}

// Живой внешний сквад уходит как есть.
func TestCreateKeepsLiveExternalSquad(t *testing.T) {
	p := newSquadPanel()
	c := p.start(t)
	if _, _, err := c.CreateOrUpdateUser(context.Background(), squadTgID, 1, 0, UserLimits{
		InternalSquads: []string{"s-live"},
		ExternalSquad:  "e-live",
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.posts[0]["externalSquadUuid"].(string); got != "e-live" {
		t.Fatalf("живой внешний сквад потерян: %q", got)
	}
}

// Все сквады удалены: создавать пользователя без серверов нельзя — понятная
// ошибка вместо 500 от панели.
func TestCreateAllSquadsGoneFails(t *testing.T) {
	p := newSquadPanel()
	c := p.start(t)
	_, _, err := c.CreateOrUpdateUserDays(context.Background(), squadTgID, 3, UserLimits{
		InternalSquads: []string{"s-gone"},
	})
	if !errors.Is(err, ErrSquadsGone) {
		t.Fatalf("ожидали ErrSquadsGone, получили %v", err)
	}
	if len(p.posts) != 0 {
		t.Fatalf("пользователь создан без сквадов: %v", p.posts)
	}
}

// У существующего пользователя пустой набор не отправляется: его текущие
// сквады остаются, продление проходит.
func TestRenewalAllSquadsGoneKeepsCurrent(t *testing.T) {
	p := newSquadPanel()
	p.existing = true
	c := p.start(t)
	_, _, err := c.ExtendUserDays(context.Background(), squadTgID, 7, 0, UserLimits{
		InternalSquads: []string{"s-gone"},
		ExternalSquad:  "e-gone",
	}, false)
	if err != nil {
		t.Fatalf("продление не прошло: %v", err)
	}
	if len(p.patches) != 1 {
		t.Fatalf("ожидали один PATCH, получили %d", len(p.patches))
	}
	if _, ok := p.patches[0]["activeInternalSquads"]; ok {
		t.Fatalf("в PATCH ушли сквады: %v", p.patches[0]["activeInternalSquads"])
	}
	if _, ok := p.patches[0]["externalSquadUuid"]; ok {
		t.Fatal("в PATCH ушёл удалённый внешний сквад")
	}
}

// Список сквадов недоступен — сквады уходят как были, решает панель.
func TestUnknownSquadListSendsAsIs(t *testing.T) {
	p := newSquadPanel()
	p.intStatus, p.extStatus = http.StatusInternalServerError, http.StatusInternalServerError
	c := p.start(t)
	if _, _, err := c.CreateOrUpdateUserDays(context.Background(), squadTgID, 3, UserLimits{
		InternalSquads: []string{"s-live", "s-other"},
		ExternalSquad:  "e-other",
	}); err != nil {
		t.Fatal(err)
	}
	if got := squadsOf(p.posts[0]); strings.Join(got, ",") != "s-live,s-other" {
		t.Fatalf("сквады изменены без списка панели: %v", got)
	}
	if got, _ := p.posts[0]["externalSquadUuid"].(string); got != "e-other" {
		t.Fatalf("внешний сквад потерян без списка панели: %q", got)
	}
}

// Без сквадов в лимитах список не запрашивается; повторная выдача в пределах
// кэша не ходит за ним снова.
func TestSquadListCached(t *testing.T) {
	p := newSquadPanel()
	c := p.start(t)
	ctx := context.Background()
	if _, _, err := c.CreateOrUpdateUserDays(ctx, squadTgID, 3, UserLimits{}); err != nil {
		t.Fatal(err)
	}
	if p.listHits != 0 {
		t.Fatalf("список сквадов запрошен без сквадов в выдаче: %d", p.listHits)
	}
	lim := UserLimits{InternalSquads: []string{"s-live"}}
	for i := 0; i < 3; i++ {
		if _, _, err := c.CreateOrUpdateUserDays(ctx, squadTgID, 3, lim); err != nil {
			t.Fatal(err)
		}
	}
	if p.listHits != 2 {
		t.Fatalf("ожидали один заход за списками (2 запроса), получили %d", p.listHits)
	}
}

// Доп-подписка: удалённые сквады не уходят ни при создании, ни при обновлении.
func TestAddSubSkipsGoneSquads(t *testing.T) {
	ap := newAddSubPanel("tg_42")
	srv := ap.start(t)
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	c.squads.live = LiveSquads{Int: map[string]string{"s-live": "Живой"}, IntKnown: true}
	c.squads.at = time.Now()

	opt := AddSubOptions{InternalSquads: []string{"s-gone", "s-live"}}
	if _, err := c.UpsertAddSub(context.Background(), 42, opt); err != nil {
		t.Fatal(err)
	}
	if len(ap.posts) != 1 || strings.Join(squadsOf(ap.posts[0]), ",") != "s-live" {
		t.Fatalf("создание доп-подписки: %v", ap.posts)
	}

	ap.addUser("tg_42_addsub", "b-uuid", BotTagAdd)
	if _, err := c.UpsertAddSub(context.Background(), 42, opt); err != nil {
		t.Fatal(err)
	}
	if len(ap.patches) != 1 || strings.Join(squadsOf(ap.patches[0]), ",") != "s-live" {
		t.Fatalf("обновление доп-подписки: %v", ap.patches)
	}

	// Все удалены: B не создаётся пустым.
	ap2 := newAddSubPanel("tg_42")
	srv2 := ap2.start(t)
	c2 := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv2.URL, APIToken: "t"})
	c2.squads.live = LiveSquads{Int: map[string]string{"s-live": ""}, IntKnown: true}
	c2.squads.at = time.Now()
	if _, err := c2.UpsertAddSub(context.Background(), 42, AddSubOptions{InternalSquads: []string{"s-gone"}}); !errors.Is(err, ErrSquadsGone) {
		t.Fatalf("ожидали ErrSquadsGone, получили %v", err)
	}
	if len(ap2.posts) != 0 {
		t.Fatalf("доп-подписка создана без сквадов: %v", ap2.posts)
	}
}

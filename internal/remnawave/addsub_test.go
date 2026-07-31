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

// addSubPanel is a fake panel that serves one main user A (any username) plus
// whatever add-on users the test pre-creates, and records every write.
type addSubPanel struct {
	mu sync.Mutex

	mainUsername string
	mainStatus   string
	mainExpire   string

	// users by username -> raw JSON body of the panel user
	users map[string]string

	posts   []map[string]any
	patches []map[string]any
	deleted []string
	resets  []string
	revokes []string
	hwidDel []string
}

func newAddSubPanel(username string) *addSubPanel {
	return &addSubPanel{
		mainUsername: username,
		mainStatus:   "ACTIVE",
		mainExpire:   time.Now().UTC().Add(720 * time.Hour).Format(time.RFC3339),
		users:        map[string]string{},
	}
}

func (p *addSubPanel) addUser(username, uuid, tag string) {
	p.users[username] = `{"uuid":"` + uuid + `","username":"` + username +
		`","tag":"` + tag + `","status":"ACTIVE","expireAt":"` + p.mainExpire + `","trafficLimitBytes":100,"userTraffic":{"usedTrafficBytes":40}}`
}

func (p *addSubPanel) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":[{"uuid":"main-uuid","username":"` + p.mainUsername +
			`","telegramId":42,"tag":"` + BotTag + `","status":"` + p.mainStatus +
			`","expireAt":"` + p.mainExpire + `","hwidDeviceLimit":3,"trafficLimitStrategy":"MONTH","subscriptionUrl":"https://x/y"}]}`))
	})
	mux.HandleFunc("/api/users/by-username/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/users/by-username/")
		p.mu.Lock()
		body, ok := p.users[name]
		p.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":` + body + `}`))
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
		w.Write([]byte(`{"response":{"uuid":"b-uuid","subscriptionUrl":"https://x/b","expireAt":"` + p.mainExpire + `"}}`))
	})
	mux.HandleFunc("/api/users/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/users/")
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case r.Method == http.MethodDelete:
			p.deleted = append(p.deleted, rest)
		case strings.HasSuffix(rest, "/actions/reset-traffic"):
			p.resets = append(p.resets, strings.TrimSuffix(rest, "/actions/reset-traffic"))
		case strings.HasSuffix(rest, "/actions/revoke"):
			p.revokes = append(p.revokes, strings.TrimSuffix(rest, "/actions/revoke"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":{}}`))
	})
	mux.HandleFunc("/api/hwid/devices/delete-all", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		p.mu.Lock()
		p.hwidDel = append(p.hwidDel, str(body["userUuid"]))
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":{"total":0,"devices":[]}}`))
	})
	mux.HandleFunc("/api/hwid/devices/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":{"total":2,"devices":[]}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func addSubClient(t *testing.T, p *addSubPanel) *Client {
	t.Helper()
	srv := p.start(t)
	return New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

func TestAddSubNamesDerivesFromMainUsername(t *testing.T) {
	// Bot-created account: nothing changes.
	if got := addSubNames("tg_42", 42, ""); len(got) != 1 || got[0] != "tg_42_addsub" {
		t.Fatalf("bot-created: %v", got)
	}
	// Adopted account: the middleware looks up <A>_addsub, the legacy name is
	// kept as a migration source.
	got := addSubNames("vasya", 42, "")
	if len(got) != 2 || got[0] != "vasya_addsub" || got[1] != "tg_42_addsub" {
		t.Fatalf("adopted: %v", got)
	}
	// A unknown (already deleted): only the legacy name is knowable.
	if got := addSubNames("", 42, "_x"); len(got) != 1 || got[0] != "tg_42_x" {
		t.Fatalf("no main: %v", got)
	}
}

func TestUpsertAddSubUsesMainUsername(t *testing.T) {
	p := newAddSubPanel("vasya")
	c := addSubClient(t, p)
	if _, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(p.posts) != 1 {
		t.Fatalf("posts = %v", p.posts)
	}
	if got := str(p.posts[0]["username"]); got != "vasya_addsub" {
		t.Fatalf("username = %q, ожидалось vasya_addsub (иначе прослойка не найдёт B)", got)
	}
	if got := str(p.posts[0]["tag"]); got != BotTagAdd {
		t.Fatalf("tag = %q", got)
	}
	if _, ok := p.posts[0]["telegramId"]; ok {
		t.Fatal("у B не должно быть telegramId")
	}
}

func TestUpsertAddSubKeepsManagingLegacyUserByDefault(t *testing.T) {
	p := newAddSubPanel("vasya")
	p.addUser("tg_42_addsub", "legacy-uuid", BotTagAdd)
	c := addSubClient(t, p)
	res, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Старый B может быть заведён в прослойке ручной привязкой. Автоматические
	// пути не должны ни удалять его, ни бросать (иначе он протухнет по сроку) —
	// он ведётся ровно как раньше.
	if len(p.deleted) != 0 {
		t.Fatalf("на автопути ничего удалять нельзя: %v", p.deleted)
	}
	if len(p.posts) != 0 {
		t.Fatalf("дубль создавать нельзя: %v", p.posts)
	}
	if len(p.patches) != 1 || str(p.patches[0]["uuid"]) != "legacy-uuid" {
		t.Fatalf("старый B должен продолжать обновляться: %v", p.patches)
	}
	if res.Legacy != "tg_42_addsub" || res.Migrated {
		t.Fatalf("res = %+v", res)
	}
}

func TestUpsertAddSubMigratesOnlyWhenAsked(t *testing.T) {
	p := newAddSubPanel("vasya")
	p.addUser("tg_42_addsub", "legacy-uuid", BotTagAdd)
	c := addSubClient(t, p)
	res, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{MigrateLegacyName: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.posts) != 1 || str(p.posts[0]["username"]) != "vasya_addsub" {
		t.Fatalf("новый B не создан: %v", p.posts)
	}
	if len(p.deleted) != 1 || p.deleted[0] != "legacy-uuid" {
		t.Fatalf("старый B не удалён: %v", p.deleted)
	}
	if !res.Migrated || res.Legacy != "tg_42_addsub" {
		t.Fatalf("res = %+v", res)
	}
}

func TestAddSubNamesFallBackWhenNameTooLongForPanel(t *testing.T) {
	long := strings.Repeat("u", 34) // 34 + len("_addsub") = 41 > 36
	got := addSubNames(long, 42, "")
	if len(got) != 1 || got[0] != "tg_42_addsub" {
		t.Fatalf("панель отвергла бы имя, нужен откат на короткое: %v", got)
	}
}

func TestUpsertAddSubAlwaysWritesTrafficLimit(t *testing.T) {
	p := newAddSubPanel("tg_42")
	p.addUser("tg_42_addsub", "b-uuid", BotTagAdd)
	c := addSubClient(t, p)
	// 0 = безлимит: значение задаётся в админке бота, поэтому пишется явно.
	if _, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(p.patches) != 1 {
		t.Fatalf("patches = %v", p.patches)
	}
	if v, ok := p.patches[0]["trafficLimitBytes"]; !ok || v.(float64) != 0 {
		t.Fatalf("лимит трафика B не записан: %v", p.patches[0])
	}
}

func TestUpsertAddSubResetsTrafficOnRenewal(t *testing.T) {
	p := newAddSubPanel("tg_42")
	p.addUser("tg_42_addsub", "b-uuid", BotTagAdd)
	c := addSubClient(t, p)
	if _, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{TrafficBytes: 5, ResetTraffic: true}); err != nil {
		t.Fatal(err)
	}
	if len(p.patches) != 1 {
		t.Fatalf("patches = %v", p.patches)
	}
	if len(p.resets) != 1 || p.resets[0] != "b-uuid" {
		t.Fatalf("трафик B не сброшен: %v", p.resets)
	}
	if len(p.deleted) != 0 {
		t.Fatalf("ничего не должно удаляться: %v", p.deleted)
	}
}

func TestUpsertAddSubNoResetWithoutRenewal(t *testing.T) {
	p := newAddSubPanel("tg_42")
	p.addUser("tg_42_addsub", "b-uuid", BotTagAdd)
	c := addSubClient(t, p)
	if _, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{TrafficBytes: 5}); err != nil {
		t.Fatal(err)
	}
	if len(p.resets) != 0 {
		t.Fatalf("сброс трафика без продления: %v", p.resets)
	}
}

func TestUpsertAddSubMirrorsDisabledStatus(t *testing.T) {
	p := newAddSubPanel("tg_42")
	p.mainStatus = StatusDisabled
	c := addSubClient(t, p)
	if _, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(p.posts) != 1 || str(p.posts[0]["status"]) != StatusDisabled {
		t.Fatalf("статус B не зеркалит A: %v", p.posts)
	}
}

func TestUpsertAddSubSkipsExpired(t *testing.T) {
	p := newAddSubPanel("tg_42")
	p.mainExpire = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	c := addSubClient(t, p)
	if _, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(p.posts) != 0 || len(p.patches) != 0 {
		t.Fatalf("для истёкшей A не должно быть записей: posts=%v patches=%v", p.posts, p.patches)
	}
}

func TestUpsertAddSubRejectsForeignUser(t *testing.T) {
	p := newAddSubPanel("vasya")
	p.addUser("vasya_addsub", "foreign", "SOMEONE_ELSE")
	c := addSubClient(t, p)
	_, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{})
	if err == nil {
		t.Fatal("чужой пользователь на имени B должен быть ошибкой")
	}
	if len(p.posts) != 0 || len(p.deleted) != 0 {
		t.Fatalf("чужого трогать нельзя: posts=%v deleted=%v", p.posts, p.deleted)
	}
}

func TestSetAddSubEnabledFindsByMainUsername(t *testing.T) {
	p := newAddSubPanel("vasya")
	p.addUser("vasya_addsub", "b-uuid", BotTagAdd)
	c := addSubClient(t, p)
	if err := c.SetAddSubEnabled(context.Background(), 42, "", true); err != nil {
		t.Fatal(err)
	}
	if len(p.patches) != 1 || str(p.patches[0]["uuid"]) != "b-uuid" || str(p.patches[0]["status"]) != "ACTIVE" {
		t.Fatalf("patches = %v", p.patches)
	}
}

func TestDeleteAddSubResolvesThroughMainUser(t *testing.T) {
	p := newAddSubPanel("vasya")
	p.addUser("vasya_addsub", "b-uuid", BotTagAdd)
	c := addSubClient(t, p)
	if err := c.DeleteAddSub(context.Background(), 42, ""); err != nil {
		t.Fatal(err)
	}
	if len(p.deleted) != 1 || p.deleted[0] != "b-uuid" {
		t.Fatalf("deleted = %v", p.deleted)
	}
}

func TestAddSubStatusReportsTraffic(t *testing.T) {
	p := newAddSubPanel("tg_42")
	p.addUser("tg_42_addsub", "b-uuid", BotTagAdd)
	c := addSubClient(t, p)
	info, ok := c.AddSubStatus(context.Background(), 42, "")
	if !ok || info.UUID != "b-uuid" || info.Limit != 100 || info.Used != 40 {
		t.Fatalf("info = %+v ok=%v", info, ok)
	}
	if info.Exhausted {
		t.Fatal("40 из 100 — не исчерпан")
	}
}

func TestAddSubStatusMissing(t *testing.T) {
	p := newAddSubPanel("tg_42")
	c := addSubClient(t, p)
	if _, ok := c.AddSubStatus(context.Background(), 42, ""); ok {
		t.Fatal("без B должно быть ok=false")
	}
}

func TestResetAddSubDevices(t *testing.T) {
	p := newAddSubPanel("tg_42")
	p.addUser("tg_42_addsub", "b-uuid", BotTagAdd)
	c := addSubClient(t, p)
	res, found, err := c.ResetAddSubDevices(context.Background(), 42, "")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !res.KeysRotated || !res.HwidCleared || res.Removed != 2 {
		t.Fatalf("res = %+v", res)
	}
	if len(p.revokes) != 1 || p.revokes[0] != "b-uuid" {
		t.Fatalf("revokes = %v", p.revokes)
	}
	if len(p.hwidDel) != 1 || p.hwidDel[0] != "b-uuid" {
		t.Fatalf("hwid delete-all = %v", p.hwidDel)
	}
}

func TestResetAddSubDevicesNoAddon(t *testing.T) {
	p := newAddSubPanel("tg_42")
	c := addSubClient(t, p)
	_, found, err := c.ResetAddSubDevices(context.Background(), 42, "")
	if err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if len(p.revokes) != 0 {
		t.Fatalf("ничего не должно ротироваться: %v", p.revokes)
	}
}

func TestUpsertAddSubKeepsStatusUntouchedWhenInStep(t *testing.T) {
	p := newAddSubPanel("tg_42")
	p.addUser("tg_42_addsub", "b-uuid", BotTagAdd)
	c := addSubClient(t, p)
	if _, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{TrafficBytes: 5}); err != nil {
		t.Fatal(err)
	}
	if len(p.patches) != 1 {
		t.Fatalf("patches = %v", p.patches)
	}
	// Writing ACTIVE unconditionally would un-limit a B the panel had capped.
	if _, ok := p.patches[0]["status"]; ok {
		t.Fatalf("статус трогать не нужно: %v", p.patches[0])
	}
}

func TestUpsertAddSubLiftsLeftoverBlock(t *testing.T) {
	p := newAddSubPanel("tg_42")
	p.users["tg_42_addsub"] = `{"uuid":"b-uuid","username":"tg_42_addsub","tag":"` + BotTagAdd +
		`","status":"` + StatusDisabled + `","expireAt":"` + p.mainExpire + `"}`
	c := addSubClient(t, p)
	if _, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{TrafficBytes: 5, ResetTraffic: true}); err != nil {
		t.Fatal(err)
	}
	if len(p.patches) != 1 || str(p.patches[0]["status"]) != "ACTIVE" {
		t.Fatalf("забытая блокировка B не снята: %v", p.patches)
	}
}

func TestMainUsernameErrorIsNotSwallowed(t *testing.T) {
	// Панель отвечает 500 на by-telegram-id: нельзя молча сузить поиск до
	// старого имени — иначе B под новым именем останется активным.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	var lookedUp int
	mux.HandleFunc("/api/users/by-username/", func(w http.ResponseWriter, r *http.Request) {
		lookedUp++
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	if err := c.SetAddSubEnabled(context.Background(), 42, "", false); err == nil {
		t.Fatal("ошибка панели должна пробрасываться наверх")
	}
	if err := c.DeleteAddSub(context.Background(), 42, ""); err == nil {
		t.Fatal("ошибка панели должна пробрасываться наверх")
	}
	if lookedUp != 0 {
		t.Fatalf("поиск по имени не должен запускаться после ошибки: %d", lookedUp)
	}
}

func TestBadSuffixFallsBackToDefault(t *testing.T) {
	// Суффикс с символами, которых панель не примет в username, обнулил бы
	// авто-дискавери у всех — откатываемся на дефолт.
	if got := normalizeAddSubSuffix("_доп"); got != DefaultAddSubSuffix {
		t.Fatalf("suffix = %q", got)
	}
	if got := normalizeAddSubSuffix("-extra"); got != "-extra" {
		t.Fatalf("валидный суффикс не должен подменяться: %q", got)
	}
}

func TestMigrationCleansUpLeftoverAfterPartialRun(t *testing.T) {
	// Прошлый переезд создал нового B, но не смог удалить старого.
	p := newAddSubPanel("vasya")
	p.addUser("vasya_addsub", "new-uuid", BotTagAdd)
	p.addUser("tg_42_addsub", "legacy-uuid", BotTagAdd)
	c := addSubClient(t, p)
	res, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{MigrateLegacyName: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.deleted) != 1 || p.deleted[0] != "legacy-uuid" {
		t.Fatalf("забытый старый B не убран: %v", p.deleted)
	}
	if !res.Migrated || res.Legacy != "tg_42_addsub" {
		t.Fatalf("res = %+v", res)
	}
	if len(p.patches) != 1 || str(p.patches[0]["uuid"]) != "new-uuid" {
		t.Fatalf("новый B должен обновляться как обычно: %v", p.patches)
	}
}

func TestNoLeftoverProbeOnAutomaticPath(t *testing.T) {
	p := newAddSubPanel("vasya")
	p.addUser("vasya_addsub", "new-uuid", BotTagAdd)
	p.addUser("tg_42_addsub", "legacy-uuid", BotTagAdd)
	c := addSubClient(t, p)
	res, err := c.UpsertAddSub(context.Background(), 42, AddSubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.deleted) != 0 || res.Migrated {
		t.Fatalf("автопуть ничего не удаляет: deleted=%v res=%+v", p.deleted, res)
	}
}

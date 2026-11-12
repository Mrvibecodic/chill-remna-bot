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

// Блокировка и разблокировка «в боте и подписку»: флаг в боте, статус основной
// учётки в панели, доп-подписка следом за ней и сообщение человеку — в обе
// стороны.
func TestBlockAndUnblockBothSides(t *testing.T) {
	const uid int64 = 555
	var mu sync.Mutex
	var patches []string
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/by-telegram-id/"):
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u-a","tag":"CHILLBOT","username":"tg_555","telegramId":555,"status":"ACTIVE",` +
				`"subscriptionUrl":"https://sub.example.com/x","expireAt":"2030-01-01T00:00:00.000Z"}]}`))
		case strings.HasSuffix(r.URL.Path, "/by-username/tg_555_addsub"):
			_, _ = w.Write([]byte(`{"response":{"uuid":"u-b","username":"tg_555_addsub","tag":"CHILLBOT_ADD","status":"ACTIVE",` +
				`"subscriptionUrl":"https://sub.example.com/b","expireAt":"2030-01-01T00:00:00.000Z"}}`))
		case r.URL.Path == "/api/users" && r.Method == http.MethodPatch:
			raw, _ := io.ReadAll(r.Body)
			var b struct{ UUID, Status string }
			_ = json.Unmarshal(raw, &b)
			mu.Lock()
			patches = append(patches, b.UUID+"="+b.Status)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"response":{"uuid":"` + b.UUID + `"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(panel.Close)

	a, fm, st := bkTestApp(t)
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panel.URL, APIToken: "t"})
	a.botCfg.AddSub = model.AddSubConfig{Enabled: true}
	ctx := context.Background()
	_ = st.UpsertUser(ctx, uid)
	admin := a.cfg.AdminID
	took := func() string {
		mu.Lock()
		defer mu.Unlock()
		out := strings.Join(patches, " ")
		patches = nil
		return out
	}

	a.applyBlock(ctx, admin, uid, "blockboth", 0)
	if u, _ := st.GetUser(ctx, uid); u == nil || !u.Blocked {
		t.Fatal("после блокировки человек не отмечен заблокированным в боте")
	}
	if got := took(); got != "u-a=DISABLED u-b=DISABLED" {
		t.Fatalf("блокировка в панели: %q", got)
	}
	if !strings.Contains(fm.joined(), "Доступ заблокирован") {
		t.Fatalf("человеку не сказано о блокировке: %s", fm.joined())
	}

	a.applyUnblock(ctx, admin, uid, "unblockboth", 0)
	if u, _ := st.GetUser(ctx, uid); u == nil || u.Blocked {
		t.Fatal("после разблокировки человек остался заблокированным в боте")
	}
	if got := took(); got != "u-a=ACTIVE u-b=ACTIVE" {
		t.Fatalf("разблокировка в панели: %q", got)
	}
	if !strings.Contains(fm.joined(), "Доступ восстановлен") {
		t.Fatalf("человеку не сказано о разблокировке: %s", fm.joined())
	}
}

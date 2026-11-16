package remnawave

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"remnabot/internal/model"
)

// Выдача срока в днях (триал, бонусные дни): действующей подписке дни
// прибавляются к её концу, трафик не сбрасывается; новому человеку срок
// считается от «сейчас».
func TestCreateOrUpdateUserDaysTerm(t *testing.T) {
	const until = "2030-01-01T00:00:00Z"
	var exists atomic.Bool
	var resets atomic.Int32
	var sent atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/users/by-telegram-id/"):
			if exists.Load() {
				_, _ = w.Write([]byte(`{"response":[{"uuid":"u-1","username":"tg_555","tag":"CHILLBOT","telegramId":555,` +
					`"subscriptionUrl":"https://sub.example.com/x","expireAt":"` + until + `"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"response":[]}`))
		case strings.Contains(r.URL.Path, "reset-traffic"):
			resets.Add(1)
			_, _ = w.Write([]byte(`{"response":{}}`))
		case r.URL.Path == "/api/users" && (r.Method == http.MethodPatch || r.Method == http.MethodPost):
			raw, _ := io.ReadAll(r.Body)
			var b struct {
				ExpireAt string `json:"expireAt"`
			}
			_ = json.Unmarshal(raw, &b)
			sent.Store(r.Method + " " + b.ExpireAt)
			_, _ = w.Write([]byte(`{"response":{"uuid":"u-1","subscriptionUrl":"https://sub.example.com/x","expireAt":"` + b.ExpireAt + `"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	ctx := context.Background()

	exists.Store(true)
	if _, _, err := c.CreateOrUpdateUserDays(ctx, 555, 3, UserLimits{}); err != nil {
		t.Fatal(err)
	}
	if got := sent.Load(); got != "PATCH 2030-01-04T00:00:00Z" {
		t.Fatalf("действующей подписке три дня прибавляются к её концу, отправлено %v", got)
	}
	if n := resets.Load(); n != 0 {
		t.Fatalf("выдача дней не сбрасывает трафик, сбросов %d", n)
	}

	exists.Store(false)
	before := time.Now().UTC()
	if _, _, err := c.CreateOrUpdateUserDays(ctx, 555, 3, UserLimits{}); err != nil {
		t.Fatal(err)
	}
	method, at, _ := strings.Cut(sent.Load().(string), " ")
	got, err := time.Parse(time.RFC3339, at)
	if method != http.MethodPost || err != nil {
		t.Fatalf("нового человека панель должна создать: %v", sent.Load())
	}
	if d := got.Sub(before.AddDate(0, 0, 3)); d < -2*time.Second || d > time.Minute {
		t.Fatalf("новому человеку срок считается от «сейчас» + 3 дня, расхождение %v", d)
	}
}

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

func TestIsEternal(t *testing.T) {
	for raw, want := range map[string]bool{
		EternalExpire:          true,
		"2099-12-31T23:00:00Z": true,
		"2100-01-01T00:00:00Z": true,
		"2098-12-31T23:59:59Z": false,
		"":                     false,
		"мусор":                false,
	} {
		if IsEternal(raw) != want {
			t.Errorf("IsEternal(%q) != %v", raw, want)
		}
	}
	// Год берётся панелью в её часовом поясе: срок обязан остаться в 2099 при
	// любом смещении от -12 до +14 часов.
	e, _ := time.Parse(time.RFC3339, EternalExpire)
	for _, off := range []int{-12, 0, 3, 14} {
		if y := e.In(time.FixedZone("", off*3600)).Year(); y != 2099 {
			t.Errorf("при смещении %d ч год %d", off, y)
		}
	}
}

// После трафик-тарифа (срок 2099) срочный тариф считается от сегодня, а не
// прибавляется к бессрочному сроку.
func TestNextExpireAfterEternal(t *testing.T) {
	now := time.Now().UTC()
	u := &panelUser{ExpireAt: EternalExpire}
	for name, raw := range map[string]string{
		"месяцы": nextExpire(termBase(u, true), 1, 0),
		"дни":    nextExpireDays(termBase(u, true), 30, 0),
	} {
		got, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			t.Fatal(err)
		}
		if d := got.Sub(now).Hours() / 24; d < 27 || d > 32 {
			t.Errorf("%s: срок через %.1f дн., ожидался месяц от сегодня", name, d)
		}
	}
}

// Бессрочный срок, выставленный админом руками (без признака FromNow),
// продлевается как раньше — от его конца.
func TestManualEternalExtends(t *testing.T) {
	u := &panelUser{ExpireAt: "2099-12-31T00:00:00Z"}
	if got := nextExpire(termBase(u, false), 1, 0); got != "2100-01-31T00:00:00Z" {
		t.Fatalf("ручной бессрочный срок: %s", got)
	}
}

// Нулевой срок (сверка лимитов применяет только лимиты) бессрочный срок не
// трогает: отсчёт «от сегодня» выключил бы подписку сразу.
func TestZeroTermKeepsEternal(t *testing.T) {
	u := &panelUser{ExpireAt: EternalExpire}
	if got := nextExpireDays(u, 0, 0); got != EternalExpire {
		t.Fatalf("дни 0: %s", got)
	}
	if got := nextExpire(u, 0, 0); got != EternalExpire {
		t.Fatalf("месяцы 0: %s", got)
	}
	// Поправка без срока бессрочный не трогает, а купленный срок с
	// отрицательной поправкой — всё равно срок от сегодня.
	if got := nextExpireDays(u, 0, 5); !IsEternal(got) {
		t.Fatalf("дни 0 с поправкой: %s", got)
	}
	if got := nextExpire(termBase(u, true), 1, -40); got == EternalExpire {
		t.Fatal("купленный месяц с поправкой оставил бессрочный срок")
	}
}

func TestGrantTraffic(t *testing.T) {
	var exists atomic.Bool
	var resets atomic.Int32
	var sent atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/users/by-telegram-id/"):
			if exists.Load() {
				_, _ = w.Write([]byte(`{"response":[{"uuid":"u-1","username":"tg_555","tag":"CHILLBOT","telegramId":555,` +
					`"subscriptionUrl":"https://sub.example.com/x","expireAt":"2030-01-01T00:00:00Z"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"response":[]}`))
		case strings.Contains(r.URL.Path, "reset-traffic"):
			resets.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/api/users" && (r.Method == http.MethodPatch || r.Method == http.MethodPost):
			raw, _ := io.ReadAll(r.Body)
			var b map[string]any
			_ = json.Unmarshal(raw, &b)
			sent.Store(b)
			_, _ = w.Write([]byte(`{"response":{"uuid":"u-1","subscriptionUrl":"https://sub.example.com/x","expireAt":"` + b["expireAt"].(string) + `"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	ctx := context.Background()
	lim := UserLimits{TrafficBytes: 150 << 30, TrafficSet: true, DeviceLimit: 3, Strategy: "NO_RESET"}

	exists.Store(true)
	r, err := c.GrantTraffic(ctx, 555, lim)
	if err != nil {
		t.Fatal(err)
	}
	b := sent.Load().(map[string]any)
	if b["expireAt"] != EternalExpire || b["trafficLimitStrategy"] != "NO_RESET" || b["trafficLimitBytes"].(float64) != float64(150<<30) {
		t.Fatalf("выдача трафик-тарифа: %v", b)
	}
	if resets.Load() != 1 || r.ResetErr == nil {
		t.Fatalf("сброс трафика обязан выполняться, а его ошибка — возвращаться: сбросов %d, ошибка %v", resets.Load(), r.ResetErr)
	}
	if r.ExpireAt != EternalExpire {
		t.Fatalf("срок: %q", r.ExpireAt)
	}

	exists.Store(false)
	resets.Store(0)
	if r, err := c.GrantTraffic(ctx, 555, lim); err != nil || r.ResetErr != nil || resets.Load() != 0 {
		t.Fatalf("новой учётке сбрасывать нечего: %+v %v", r, err)
	}
}

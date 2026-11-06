package remnawave

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"remnabot/internal/model"
)

func TestNextTrafficReset(t *testing.T) {
	ts := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	created := ts("2026-01-31T12:00:00Z")
	for _, tc := range []struct {
		strategy, now, want string
	}{
		{"DAY", "2026-11-06T10:00:00Z", "2026-11-07T00:05:00Z"},
		{"DAY", "2026-11-06T00:01:00Z", "2026-11-06T00:05:00Z"},
		{"WEEK", "2026-11-06T10:00:00Z", "2026-11-09T00:15:00Z"}, // пятница → понедельник
		{"WEEK", "2026-11-09T00:10:00Z", "2026-11-09T00:15:00Z"},
		{"WEEK", "2026-11-09T01:00:00Z", "2026-11-16T00:15:00Z"},
		{"MONTH", "2026-11-06T10:00:00Z", "2026-12-01T00:20:00Z"},
		{"MONTH", "2026-12-01T00:10:00Z", "2026-12-01T00:20:00Z"},
		// День создания 31-е: в коротком месяце — последний день.
		{"MONTH_ROLLING", "2026-11-06T10:00:00Z", "2026-11-30T00:10:00Z"},
		{"MONTH_ROLLING", "2026-02-10T10:00:00Z", "2026-02-28T00:10:00Z"},
		// Первый сброс — не раньше, чем через месяц после создания.
		{"MONTH_ROLLING", "2026-01-31T13:00:00Z", "2026-02-28T00:10:00Z"},
		{"NO_RESET", "2026-11-06T10:00:00Z", ""},
	} {
		got := NextTrafficReset(tc.strategy, created, ts(tc.now))
		want := time.Time{}
		if tc.want != "" {
			want = ts(tc.want)
		}
		if !got.Equal(want) {
			t.Errorf("%s @ %s: got %v, want %v", tc.strategy, tc.now, got, want)
		}
	}
	if !NextTrafficReset("MONTH_ROLLING", time.Time{}, ts("2026-11-06T10:00:00Z")).IsZero() {
		t.Error("без даты создания сброс MONTH_ROLLING неизвестен")
	}
}

// Сброс после конца срока не показывается: подписки к тому времени не будет.
func TestSnapshotNextReset(t *testing.T) {
	for _, tc := range []struct {
		expire string
		want   bool
	}{{"2099-01-01T00:00:00Z", true}, {"2000-01-01T00:00:00Z", false}} {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"response":[{"uuid":"u-1","subscriptionUrl":"https://x/y","status":"ACTIVE","expireAt":"` + tc.expire +
				`","trafficLimitBytes":1073741824,"trafficLimitStrategy":"DAY","createdAt":"2026-01-01T00:00:00Z"}]}`))
		})
		srv := httptest.NewServer(mux)
		c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
		s, ok, err := c.SubscriptionSnapshot(context.Background(), 42)
		srv.Close()
		if err != nil || !ok || s.NextReset.IsZero() != !tc.want {
			t.Fatalf("expire %s: %+v ok=%v err=%v", tc.expire, s, ok, err)
		}
	}
}

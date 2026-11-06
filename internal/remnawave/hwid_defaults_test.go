package remnawave

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"remnabot/internal/model"
)

func hwidServer(t *testing.T, settings, squads string, settingsCode int) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/subscription-settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(settingsCode)
		w.Write([]byte(settings))
	})
	mux.HandleFunc("/api/external-squads", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(squads))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

// Правило сквада заменяет общее целиком; пустые настройки сквада — общее.
func TestHwidDefaults(t *testing.T) {
	c := hwidServer(t,
		`{"response":{"hwidSettings":{"enabled":true,"fallbackDeviceLimit":3,"maxDevicesAnnounce":null}}}`,
		`{"response":{"total":3,"externalSquads":[
			{"uuid":"e-own","hwidSettings":{"enabled":true,"fallbackDeviceLimit":7,"maxDevicesAnnounce":null}},
			{"uuid":"e-off","hwidSettings":{"enabled":false,"fallbackDeviceLimit":1,"maxDevicesAnnounce":null}},
			{"uuid":"e-none","hwidSettings":null}]}}`, http.StatusOK)
	d, err := c.HwidDefaults(context.Background())
	if err != nil || !d.ExtKnown {
		t.Fatalf("err=%v ext=%v", err, d.ExtKnown)
	}
	for _, tc := range []struct {
		ext  string
		want HwidRule
	}{
		{"", HwidRule{true, 3}},
		{"e-none", HwidRule{true, 3}},
		{"e-own", HwidRule{true, 7}},
		{"e-off", HwidRule{false, 1}},
		{"e-unknown", HwidRule{true, 3}},
	} {
		if got, ok := d.For(tc.ext); !ok || got != tc.want {
			t.Fatalf("%q: got %+v ok=%v, want %+v", tc.ext, got, ok, tc.want)
		}
	}
}

// Без скоупа панель отвечает 403 — это ошибка, а не «лимита нет».
func TestHwidDefaultsForbidden(t *testing.T) {
	c := hwidServer(t, `{"message":"Forbidden"}`, `{}`, http.StatusForbidden)
	if _, err := c.HwidDefaults(context.Background()); err == nil {
		t.Fatal("403 должен быть ошибкой")
	}
	c = hwidServer(t, `{"response":{"hwidSettings":null}}`, `{}`, http.StatusOK)
	if _, err := c.HwidDefaults(context.Background()); err == nil {
		t.Fatal("без hwidSettings правило неизвестно")
	}
}

// Список сквадов не прочитан — правило подписки со сквадом неизвестно, без
// сквада — общее.
func TestHwidDefaultsSquadsUnknown(t *testing.T) {
	c := hwidServer(t,
		`{"response":{"hwidSettings":{"enabled":true,"fallbackDeviceLimit":3,"maxDevicesAnnounce":null}}}`,
		`{"response":{"total":2,"externalSquads":[{"uuid":"e-own","hwidSettings":null}]}}`, http.StatusOK)
	d, err := c.HwidDefaults(context.Background())
	if err != nil || d.ExtKnown {
		t.Fatalf("неполный список не должен считаться прочитанным: err=%v ext=%v", err, d.ExtKnown)
	}
	if _, ok := d.For("e-own"); ok {
		t.Fatal("правило сквада при непрочитанном списке должно быть неизвестно")
	}
	if r, ok := d.For(""); !ok || r.Fallback != 3 {
		t.Fatalf("без сквада — общее правило: %+v %v", r, ok)
	}
}

// null — лимит по умолчанию, 0 — свой «без ограничений»: их нельзя путать.
func TestDevicesOwnLimit(t *testing.T) {
	for _, tc := range []struct {
		raw string
		own bool
		lim int
		has bool
	}{{"null", false, 0, false}, {"0", true, 0, false}, {"4", true, 4, true}} {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"response":[{"uuid":"u-1","status":"ACTIVE","externalSquadUuid":"e-1","hwidDeviceLimit":` + tc.raw + `}]}`))
		})
		mux.HandleFunc("/api/hwid/devices/u-1", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"response":{"total":1,"devices":[]}}`))
		})
		srv := httptest.NewServer(mux)
		c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
		info, ok := c.DevicesByTelegramID(context.Background(), 42)
		srv.Close()
		if !ok || info.OwnLimit != tc.own || info.Limit != tc.lim || info.HasLimit != tc.has || info.ExtSquad != "e-1" {
			t.Fatalf("%s: %+v ok=%v", tc.raw, info, ok)
		}
	}
}

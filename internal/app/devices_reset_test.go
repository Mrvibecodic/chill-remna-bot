package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// miniResetServer is a fake panel whose device-reset endpoints all succeed
// (unless ok=false, in which case the user is unknown).
func miniResetServer(t *testing.T, ok bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.Write([]byte(`{"response":[]}`))
			return
		}
		w.Write([]byte(`{"response":[{"uuid":"u-1","telegramId":42,"status":"ACTIVE","hwidDeviceLimit":3}]}`))
	})
	mux.HandleFunc("/api/hwid/devices/u-1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":{"total":1,"devices":[]}}`))
	})
	mux.HandleFunc("/api/users/u-1/actions/revoke", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/hwid/devices/delete-all", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux)
}

func miniResetApp(base string) *App {
	return &App{
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		panel: remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: base, APIToken: "t"}),
	}
}

func TestMiniResetDevices_OK(t *testing.T) {
	srv := miniResetServer(t, true)
	defer srv.Close()
	dto := miniResetApp(srv.URL).MiniResetDevices(context.Background(), 42)
	if !dto.OK || dto.Error != "" {
		t.Fatalf("dto=%+v, want OK", dto)
	}
}

func TestMiniResetDevices_NotFound(t *testing.T) {
	srv := miniResetServer(t, false)
	defer srv.Close()
	dto := miniResetApp(srv.URL).MiniResetDevices(context.Background(), 42)
	if dto.OK || dto.Error == "" {
		t.Fatalf("dto=%+v, want not-found error", dto)
	}
}

func TestMiniResetDevices_NoPanel(t *testing.T) {
	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	dto := a.MiniResetDevices(context.Background(), 42)
	if dto.OK || dto.Error == "" {
		t.Fatalf("dto=%+v, want error when panel is nil", dto)
	}
}

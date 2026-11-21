package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// deleteDeviceServer is a fake panel with one HWID device. The delete endpoint
// accepts only the exact user/hwid pair the list reported.
func deleteDeviceServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[{"uuid":"u-1","telegramId":42,"status":"ACTIVE","hwidDeviceLimit":3}]}`))
	})
	mux.HandleFunc("/api/hwid/devices/u-1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"total":1,"devices":[{"hwid":"hw-1","userUuid":"u-1"}]}}`))
	})
	mux.HandleFunc("/api/hwid/devices/delete", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"hwid":"hw-1"`) || !strings.Contains(string(body), `"userUuid":"u-1"`) {
			http.Error(w, "bad delete body: "+string(body), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux)
}

func deleteDeviceApp(base string) *App {
	return &App{
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		panel: remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: base, APIToken: "t"}),
	}
}

func TestMiniDeleteDevice_OK(t *testing.T) {
	srv := deleteDeviceServer(t)
	defer srv.Close()
	dto := deleteDeviceApp(srv.URL).MiniDeleteDevice(context.Background(), 42, "hw-1")
	if !dto.OK || dto.Error != "" {
		t.Fatalf("dto=%+v, want OK", dto)
	}
}

func TestMiniDeleteDevice_UnknownHWID(t *testing.T) {
	srv := deleteDeviceServer(t)
	defer srv.Close()
	dto := deleteDeviceApp(srv.URL).MiniDeleteDevice(context.Background(), 42, "nope")
	if dto.OK || dto.Error == "" {
		t.Fatalf("dto=%+v, want not-found error", dto)
	}
}

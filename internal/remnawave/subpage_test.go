package remnawave

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remnabot/internal/model"
)

func subpageClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

func TestSubpageConfigsOrdersByViewPosition(t *testing.T) {
	c := subpageClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/subscription-page-configs" {
			t.Errorf("путь = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"response":{"total":2,"configs":[
			{"uuid":"b","name":"Second","viewPosition":2,"config":{"platforms":{}}},
			{"uuid":"a","name":"Default","viewPosition":1,"config":{"platforms":{}}}
		]}}`)
	})
	list, err := c.SubpageConfigs(context.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(list) != 2 || list[0].UUID != "a" || list[1].UUID != "b" {
		t.Fatalf("порядок = %+v", list)
	}
	if len(list[0].Config) == 0 {
		t.Error("сырой конфиг не сохранён")
	}
}

// Панель до 3.0.0 такого маршрута не знает — это не ошибка, а сигнал идти
// старым путём, на саму страницу подписки.
func TestSubpageConfigsMissingAPI(t *testing.T) {
	c := subpageClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.SubpageConfigs(context.Background())
	if !ErrNoSubpageAPI(err) {
		t.Fatalf("err = %v, want «нет API»", err)
	}
}

func TestSubpageConfigForPicksAssigned(t *testing.T) {
	c := subpageClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/subscription-page-configs":
			_, _ = io.WriteString(w, `{"response":{"total":2,"configs":[
				{"uuid":"a","name":"Default","viewPosition":1,"config":{"platforms":{}}},
				{"uuid":"b","name":"Second","viewPosition":2,"config":{"platforms":{}}}
			]}}`)
		case strings.HasPrefix(r.URL.Path, "/api/subscriptions/subpage-config/"):
			if got := strings.TrimPrefix(r.URL.Path, "/api/subscriptions/subpage-config/"); got != "sh0rt" {
				t.Errorf("shortUuid = %q", got)
			}
			_, _ = io.WriteString(w, `{"response":{"subpageConfigUuid":"b","webpageAllowed":true}}`)
		default:
			t.Errorf("неожиданный путь %q", r.URL.Path)
		}
	})
	cfg, err := c.SubpageConfigFor(context.Background(), "sh0rt")
	if err != nil || cfg == nil {
		t.Fatalf("cfg = %v, err = %v", cfg, err)
	}
	if cfg.UUID != "b" {
		t.Fatalf("выбран %q, want b", cfg.UUID)
	}
}

// Персональный маршрут не обязателен: если панель на него не отвечает, берём
// первый конфиг, он же дефолтный.
func TestSubpageConfigForFallsBackToFirst(t *testing.T) {
	c := subpageClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/subscription-page-configs" {
			_, _ = io.WriteString(w, `{"response":{"total":1,"configs":[
				{"uuid":"a","name":"Default","viewPosition":1,"config":{"platforms":{}}}
			]}}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	cfg, err := c.SubpageConfigFor(context.Background(), "sh0rt")
	if err != nil || cfg == nil || cfg.UUID != "a" {
		t.Fatalf("cfg = %v, err = %v", cfg, err)
	}
}

func TestShortUUIDParsedFromPanelUser(t *testing.T) {
	u := toPanelUser(&panelUser{Uuid: "u-1", ShortUuid: "sh0rt", Username: "tg_1"})
	if u == nil || u.ShortUUID != "sh0rt" {
		t.Fatalf("ShortUUID = %+v", u)
	}
}

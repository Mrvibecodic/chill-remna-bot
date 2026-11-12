package remnawave

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"remnabot/internal/model"
)

// Экран подписки спрашивает о человеке три вещи: подписку, устройства и
// доп-подписку. С памятью показа панель ищет его один раз, без неё — каждый
// вызов сам, как и раньше.
func TestLookupMemoFindsUserOnce(t *testing.T) {
	var lookups atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/users/by-telegram-id/") {
			lookups.Add(1)
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u-1","username":"tg_555","telegramId":555,"status":"ACTIVE",` +
				`"subscriptionUrl":"https://sub.example.com/x","expireAt":"2030-01-01T00:00:00.000Z"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	ask := func(ctx context.Context) {
		t.Helper()
		snap, ok, err := c.SubscriptionSnapshot(ctx, 555)
		if err != nil || !ok || snap.URL == "" {
			t.Fatalf("подписка не прочитана: ok=%v err=%v", ok, err)
		}
		c.DevicesByTelegramID(ctx, 555)
		if name, err := c.mainUsernameFor(ctx, 555); err != nil || name != "tg_555" {
			t.Fatalf("имя основной учётки: %q %v", name, err)
		}
	}

	ask(context.Background())
	if n := lookups.Swap(0); n != 3 {
		t.Fatalf("без памяти показа ожидалось три поиска, было %d", n)
	}
	ctx := WithLookupMemo(context.Background())
	ask(ctx)
	if n := lookups.Swap(0); n != 1 {
		t.Fatalf("с памятью показа ожидался один поиск, было %d", n)
	}
	// Вызов, меняющий пользователя, память не читает: панель спрашивается заново.
	if _, err := c.FindByTelegramID(ctx, 555); err != nil {
		t.Fatal(err)
	}
	if n := lookups.Load(); n != 1 {
		t.Fatalf("прямой поиск обязан идти в панель, запросов %d", n)
	}
}

// «Такого нет» тоже запоминается на показ, а сбой панели — нет.
func TestLookupMemoKeepsAbsenceNotErrors(t *testing.T) {
	var lookups atomic.Int32
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/users/by-telegram-id/") {
			lookups.Add(1)
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(`{"response":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	ctx := WithLookupMemo(context.Background())

	if _, _, err := c.SubscriptionSnapshot(ctx, 555); err == nil {
		t.Fatal("сбой панели должен вернуться ошибкой")
	}
	fail = false
	lookups.Store(0)
	for range 2 {
		if _, ok, err := c.SubscriptionSnapshot(ctx, 555); ok || err != nil {
			t.Fatalf("ожидалось «подписки нет»: ok=%v err=%v", ok, err)
		}
	}
	if n := lookups.Load(); n != 1 {
		t.Fatalf("после сбоя панель спрошена заново один раз, было %d", n)
	}
}

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// Экран «Моя подписка» в чате и в мини-аппе ищет человека в панели один раз,
// хотя показывает подписку, устройства и доп-подписку.
func TestSubscriptionScreenLooksUserUpOnce(t *testing.T) {
	var lookups atomic.Int32
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			lookups.Add(1)
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u1","tag":"CHILLBOT","username":"tg_555","telegramId":555,"status":"ACTIVE",` +
				`"subscriptionUrl":"https://sub.example.com/x","expireAt":"2030-01-01T00:00:00.000Z"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(panel.Close)

	a, fm, st := bkTestApp(t)
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panel.URL, APIToken: "t"})
	ctx := context.Background()
	const uid int64 = 555
	_ = st.UpsertUser(ctx, uid)

	a.showMySubs(ctx, uid)
	if !strings.Contains(fm.joined(), "sub.example.com") {
		t.Fatalf("экран подписки не показан: %s", fm.joined())
	}
	if n := lookups.Swap(0); n != 1 {
		t.Fatalf("чат: человека искали в панели %d раз, ожидался один", n)
	}

	if dto := a.MiniSubscription(ctx, uid); !dto.Active {
		t.Fatalf("мини-апп не увидел подписку: %+v", dto)
	}
	if n := lookups.Swap(0); n != 1 {
		t.Fatalf("мини-апп: человека искали в панели %d раз, ожидался один", n)
	}
}

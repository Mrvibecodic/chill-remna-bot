package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-telegram/bot"
)

// Экран с баннером раздела: сохранённая картинка заменяется встроенной, только
// когда Telegram отверг саму картинку. Разовый сбой отправки её не трогает —
// иначе баннер, загруженный администратором, пропадал бы после первой же
// неудачной отправки.
func TestSendPhotoCacheableKeepsSavedPictureOnTransientFailure(t *testing.T) {
	cases := []struct {
		name    string
		cached  string
		idReply string
		wantNew string
	}{
		{"первая отправка", "", "", "fresh-id"},
		{"картинка отвергнута", "saved-id",
			`{"ok":false,"error_code":400,"description":"Bad Request: wrong file identifier/HTTP URL specified"}`, "fresh-id"},
		{"сбой на стороне Telegram", "saved-id",
			`{"ok":false,"error_code":500,"description":"Internal Server Error"}`, ""},
		{"лимит запросов", "saved-id",
			`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`, ""},
		{"картинка ушла", "saved-id", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.FormValue("photo") == "saved-id" && c.idReply != "" {
					_, _ = io.WriteString(w, c.idReply)
					return
				}
				_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7,"chat":{"id":1},"photo":[{"file_id":"fresh-id","width":1280,"height":640}]}}`)
			}))
			defer srv.Close()
			b, err := bot.New("123:test", bot.WithServerURL(srv.URL), bot.WithSkipGetMe())
			if err != nil {
				t.Fatal(err)
			}
			m := botMessenger{b: b, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			id, newID := m.SendPhotoCacheable(context.Background(), 1, c.cached, []byte("jpeg"), "экран", nil)
			if id != 7 {
				t.Fatalf("экран не ушёл: id=%d", id)
			}
			if newID != c.wantNew {
				t.Fatalf("новый file_id для кэша: %q, ожидался %q", newID, c.wantNew)
			}
		})
	}
}

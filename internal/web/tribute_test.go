package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// trbOnly — обработчики, у которых есть только вебхук Tribute.
type trbOnly struct {
	Handlers
	err error
}

func (h trbOnly) HandleTributeWebhook(context.Context, string, []byte) (bool, error) {
	return h.err == nil, h.err
}

// Tribute ждёт в ответ {"status":"ok"}; подпись с неверным ключом — 401.
func TestHandleTribute_Response(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := func(h Handlers) *httptest.ResponseRecorder {
		s := newServer(log, h)
		w := httptest.NewRecorder()
		s.handleTribute(w, httptest.NewRequest(http.MethodPost, "/webhook/tribute", strings.NewReader(`{}`)))
		return w
	}
	w := call(trbOnly{})
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("ответ: %d %q", w.Code, w.Body.String())
	}
	if w := call(trbOnly{err: ErrUnauthorized}); w.Code != http.StatusUnauthorized {
		t.Fatalf("неверная подпись: %d", w.Code)
	}
}

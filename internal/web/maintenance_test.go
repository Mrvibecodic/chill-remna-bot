package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type maintHandlers struct {
	Handlers
	on     bool
	active int
}

func (m *maintHandlers) EnterWork() bool {
	if m.on {
		return false
	}
	m.active++
	return true
}
func (m *maintHandlers) LeaveWork() { m.active-- }

// Во время восстановления базы вебхуки и API отвечают 503, остальное живёт;
// начатые запросы отмечаются, чтобы восстановление их дождалось.
func TestWrap_Maintenance(t *testing.T) {
	mh := &maintHandlers{}
	s := newServer(nil, mh)
	inside := -1
	h := s.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inside = mh.active
		w.WriteHeader(http.StatusOK)
	}))
	call := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	paths := []struct{ method, path string }{
		{http.MethodPost, "/webhook/yookassa"},
		{http.MethodPost, "/webhook/remnawave"},
		{http.MethodGet, "/api/miniapp/me"},
		{http.MethodPost, "/api/cabinet/auth/login"},
	}
	for _, p := range paths {
		if got := call(p.method, p.path).Code; got != http.StatusOK {
			t.Fatalf("без техработ %s: %d", p.path, got)
		}
		if inside != 1 || mh.active != 0 {
			t.Fatalf("%s: запрос не отмечен (внутри %d, после %d)", p.path, inside, mh.active)
		}
	}
	mh.on = true
	for _, p := range paths {
		if got := call(p.method, p.path).Code; got != http.StatusServiceUnavailable {
			t.Fatalf("техработы %s: %d, ждали 503", p.path, got)
		}
	}
	if w := call(http.MethodGet, "/webhook/yoomoney/pay/x"); w.Code != http.StatusServiceUnavailable ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("страница оплаты в техработы: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, p := range []string{"/healthz", "/miniapp/"} {
		if got := call(http.MethodGet, p).Code; got != http.StatusOK {
			t.Fatalf("техработы %s: %d, ждали 200", p, got)
		}
	}
}

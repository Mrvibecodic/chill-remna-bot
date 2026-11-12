package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// hookSpy — обработчики вебхуков, которые запоминают, что до них дошло, и
// отвечают заданным исходом.
type hookSpy struct {
	Handlers
	err  error
	skip bool
	got  []string
}

func (h *hookSpy) out(parts ...string) (bool, error) {
	h.got = parts
	return !h.skip && h.err == nil, h.err
}
func (h *hookSpy) HandleYooKassaWebhook(_ context.Context, b []byte) (bool, error) {
	return h.out(string(b))
}
func (h *hookSpy) HandleCryptoBotWebhook(_ context.Context, sig string, b []byte) (bool, error) {
	return h.out(sig, string(b))
}
func (h *hookSpy) HandleRemnawaveWebhook(_ context.Context, sig string, b []byte) (bool, error) {
	return h.out(sig, string(b))
}
func (h *hookSpy) HandlePlategaWebhook(_ context.Context, m, s string, b []byte) (bool, error) {
	return h.out(m, s, string(b))
}
func (h *hookSpy) HandleHeleketWebhook(_ context.Context, b []byte) (bool, error) {
	return h.out(string(b))
}
func (h *hookSpy) HandleTributeWebhook(_ context.Context, sig string, b []byte) (bool, error) {
	return h.out(sig, string(b))
}
func (h *hookSpy) HandleYooMoneyWebhook(_ context.Context, f url.Values) (bool, error) {
	return h.out(f.Get("label"))
}

// Все вебхуки отвечают отправителю по одним правилам: принято — 200, подпись
// не прошла — 401, просьба повторить и сбой — 500, слишком большое тело — 413.
// Заголовки с подписью и тело доходят до обработчика без изменений.
func TestWebhookRoutesAnswerAlike(t *testing.T) {
	routes := []struct {
		path    string
		headers map[string]string
		body    string
		ctype   string
		want    []string
		okBody  string
	}{
		{"/webhook/yookassa", nil, `{"a":1}`, "", []string{`{"a":1}`}, ""},
		{"/webhook/cryptobot", map[string]string{"Crypto-Pay-API-Signature": "sig-cb"}, `{"a":1}`, "", []string{"sig-cb", `{"a":1}`}, ""},
		{"/webhook/remnawave", map[string]string{"X-Remnawave-Signature": "sig-rw"}, `{"a":1}`, "", []string{"sig-rw", `{"a":1}`}, ""},
		{"/webhook/platega", map[string]string{"X-MerchantId": "m-1", "X-Secret": "s-1"}, `{"a":1}`, "", []string{"m-1", "s-1", `{"a":1}`}, ""},
		{"/webhook/heleket", nil, `{"a":1}`, "", []string{`{"a":1}`}, ""},
		{"/webhook/tribute", map[string]string{"trbt-signature": "sig-trb"}, `{"a":1}`, "", []string{"sig-trb", `{"a":1}`}, `{"status":"ok"}`},
		{"/webhook/yoomoney", nil, "label=ym%3A1%3Aabc", "application/x-www-form-urlencoded", []string{"ym:1:abc"}, ""},
	}
	outcomes := []struct {
		name string
		err  error
		skip bool
		code int
	}{
		{"принято", nil, false, http.StatusOK},
		{"не наше событие", nil, true, http.StatusOK},
		{"подпись не прошла", errors.Join(errors.New("x"), ErrUnauthorized), false, http.StatusUnauthorized},
		{"просьба повторить", ErrRetryLater, false, http.StatusInternalServerError},
		{"сбой", errors.New("boom"), false, http.StatusInternalServerError},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, rt := range routes {
		for _, oc := range outcomes {
			spy := &hookSpy{err: oc.err, skip: oc.skip}
			s := newServer(log, spy)
			s.allowPlainHTTP = true
			req := httptest.NewRequest(http.MethodPost, rt.path, strings.NewReader(rt.body))
			for k, v := range rt.headers {
				req.Header.Set(k, v)
			}
			if rt.ctype != "" {
				req.Header.Set("Content-Type", rt.ctype)
			}
			w := httptest.NewRecorder()
			s.wrap(s.mux()).ServeHTTP(w, req)
			if w.Code != oc.code {
				t.Errorf("%s, %s: код %d, ожидался %d", rt.path, oc.name, w.Code, oc.code)
			}
			if strings.Join(spy.got, "|") != strings.Join(rt.want, "|") {
				t.Errorf("%s, %s: до обработчика дошло %q, ожидалось %q", rt.path, oc.name, spy.got, rt.want)
			}
			if oc.code == http.StatusOK && strings.TrimSpace(w.Body.String()) != rt.okBody {
				t.Errorf("%s, %s: тело ответа %q, ожидалось %q", rt.path, oc.name, w.Body.String(), rt.okBody)
			}
		}
		if rt.ctype != "" {
			continue // у формы ЮMoney свой предел и свой отказ
		}
		spy := &hookSpy{}
		s := newServer(log, spy)
		s.allowPlainHTTP = true
		w := httptest.NewRecorder()
		s.wrap(s.mux()).ServeHTTP(w, httptest.NewRequest(http.MethodPost, rt.path, strings.NewReader(strings.Repeat("x", 300*1024))))
		if w.Code != http.StatusRequestEntityTooLarge || spy.got != nil {
			t.Errorf("%s: тело 300 КБ — код %d, обработчик вызван: %v", rt.path, w.Code, spy.got != nil)
		}
	}
}

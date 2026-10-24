package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ymOnly — обработчики, у которых есть только ЮMoney.
type ymOnly struct {
	Handlers
	gotForm url.Values
	err     error
	payOK   bool
}

func (h *ymOnly) HandleYooMoneyWebhook(_ context.Context, form url.Values) (bool, error) {
	h.gotForm = form
	return h.err == nil, h.err
}

func (h *ymOnly) YooMoneyPayForm(_ context.Context, label string) (string, [][2]string, bool) {
	if !h.payOK {
		return "", nil, false
	}
	return "https://yoomoney.ru/quickpay/confirm", [][2]string{{"receiver", "4100"}, {"sum", "150.00"}, {"label", label}, {"x", `a"<b>`}}, true
}

func (h *ymOnly) YooMoneyAuthForm(state string) (string, [][2]string, bool) {
	if state != "st1" {
		return "", nil, false
	}
	return "https://yoomoney.ru/oauth/authorize", [][2]string{{"client_id", "cid"}}, true
}

func (h *ymOnly) HandleYooMoneyOAuth(_ context.Context, state, code, errCode string) string {
	return "state=" + state + " code=" + code + " err=" + errCode
}

func TestYooMoneyWebhookRoute(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &ymOnly{}
	s := newServer(log, h)
	body := "notification_type=card-incoming&operation_id=1&amount=98.00&label=ym%3A1%3Aa&sign=abc"
	req := httptest.NewRequest(http.MethodPost, "/webhook/yoomoney", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleYooMoney(w, req)
	if w.Code != http.StatusOK || h.gotForm.Get("label") != "ym:1:a" || h.gotForm.Get("sign") != "abc" {
		t.Fatalf("форма не дошла: %d %v", w.Code, h.gotForm)
	}
	h.err = ErrUnauthorized
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/webhook/yoomoney", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.handleYooMoney(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("чужая подпись: %d", w.Code)
	}
}

func TestYooMoneyRelayPages(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &ymOnly{payOK: true}
	s := newServer(log, h)
	srv := httptest.NewServer(s.wrap(s.mux()))
	defer srv.Close()
	get := func(path string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	resp, body := get("/webhook/yoomoney/pay/ym:1:abc")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("страница оплаты: %d %s", resp.StatusCode, body)
	}
	for _, want := range []string{
		`action="https://yoomoney.ru/quickpay/confirm"`, `name="label" value="ym:1:abc"`, `name="sum" value="150.00"`,
		`value="a&#34;&lt;b&gt;"`, relaySubmitJS, "<button",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("на странице нет %q:\n%s", want, body)
		}
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "form-action https://yoomoney.ru") || !strings.Contains(csp, relaySubmitHash) || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("CSP страницы: %q", csp)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("страница с суммой кэшируется: %q", resp.Header.Get("Cache-Control"))
	}
	h.payOK = false
	if resp, _ := get("/webhook/yoomoney/pay/ym:1:abc"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("несуществующий счёт: %d", resp.StatusCode)
	}

	resp, body = get("/webhook/yoomoney/auth?s=st1")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `action="https://yoomoney.ru/oauth/authorize"`) || !strings.Contains(body, `name="client_id" value="cid"`) {
		t.Fatalf("страница авторизации: %d %s", resp.StatusCode, body)
	}
	var stateCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == ymStateCookie {
			stateCookie = c
		}
	}
	if stateCookie == nil || stateCookie.Value != "st1" || !stateCookie.HttpOnly || stateCookie.SameSite != http.SameSiteLaxMode || stateCookie.Path != "/webhook/yoomoney/" {
		t.Fatalf("cookie состояния: %+v", stateCookie)
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP без frame-ancestors: %q", csp)
	}
	if resp, _ := get("/webhook/yoomoney/auth?s=other"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("протухшая ссылка: %d", resp.StatusCode)
	}
	// Редирект без cookie — состояние пустое; с cookie — передаётся как есть.
	resp, body = get("/webhook/yoomoney/oauth?code=c1&error=")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "state= code=c1 err=") {
		t.Fatalf("редирект OAuth: %d %s", resp.StatusCode, body)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/webhook/yoomoney/oauth?code=c2", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.AddCookie(&http.Cookie{Name: ymStateCookie, Value: "st1"})
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !strings.Contains(string(b2), "state=st1 code=c2") {
		t.Fatalf("cookie не дошла до обработчика: %s", b2)
	}
	// Текст ответа экранируется: редирект не должен уметь вставить разметку.
	resp, body = get("/webhook/yoomoney/oauth?code=%3Cscript%3E&error=")
	if strings.Contains(body, "<script>") {
		t.Fatalf("разметка из запроса попала на страницу:\n%s", body)
	}
	_ = resp
}

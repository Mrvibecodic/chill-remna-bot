package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Адрес клиента берётся из ПРАВОГО конца цепочки пересылки.
//
// Рекомендованный нами конфиг nginx дописывает реальный адрес в конец строки,
// присланной клиентом, — значит левый элемент пишет сам клиент. Беря его,
// лимитер получал ключ, подконтрольный атакующему, и перебор паролей кабинета
// шёл без ограничения.
func TestClientIP_TakesRightmostUntrusted(t *testing.T) {
	req := func(peer, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = peer
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	// Клиент подставил свой элемент, nginx дописал настоящий в конец.
	if got := clientIP(req("127.0.0.1:1234", "1.2.3.4, 203.0.113.7")); got != "203.0.113.7" {
		t.Fatalf("подделанный левый элемент принят: %q", got)
	}
	// Два прокси: свои адреса частные, пропускаем их и берём клиента.
	if got := clientIP(req("127.0.0.1:1234", "203.0.113.7, 10.0.0.5")); got != "203.0.113.7" {
		t.Fatalf("цепочка из двух прокси: %q", got)
	}
	// Прямой клиент из интернета: заголовкам не верим вовсе.
	if got := clientIP(req("198.51.100.9:1234", "1.2.3.4")); got != "198.51.100.9" {
		t.Fatalf("прямому клиенту поверили: %q", got)
	}
	// Одиночные заголовки читаются только от своего прокси.
	r := req("198.51.100.9:1234", "")
	r.Header.Set("CF-Connecting-IP", "1.2.3.4")
	if got := clientIP(r); got != "198.51.100.9" {
		t.Fatalf("CF-заголовок от прямого клиента принят: %q", got)
	}
}

// Признак «пришло по HTTPS» принимается только от своего прокси. Раньше он
// принимался от кого угодно, и одна строка в запросе снимала и редирект, и
// отказ 426 — клиент отдавал пароль и пропуск в открытый канал.
func TestIsSecure_TrustsOnlyLocalProxy(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.9:1234"
	r.Header.Set("X-Forwarded-Proto", "https")
	if isSecure(r) {
		t.Fatal("заголовок от прямого клиента принят за HTTPS")
	}
	r.RemoteAddr = "127.0.0.1:1234"
	if !isSecure(r) {
		t.Fatal("заголовок от локального прокси обязан приниматься")
	}
	plain := httptest.NewRequest(http.MethodGet, "/", nil)
	plain.RemoteAddr = "127.0.0.1:1234"
	if isSecure(plain) {
		t.Fatal("без заголовка и без TLS соединение не защищено")
	}
}

// Обёртка: HTTPS требуется на ручках данных, вебхуки и healthz не трогает,
// заголовки ставятся всем, мини-апп остаётся фреймируемым.
func TestWrap_HTTPSAndHeaders(t *testing.T) {
	s := newServer(nil, nil)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := s.wrap(inner)

	call := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = "198.51.100.9:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// Ручка данных по открытому HTTP — отказ. Именно она носит пропуск,
	// ссылку на подписку и баланс.
	if got := call("/api/miniapp/subscription").Code; got != http.StatusUpgradeRequired {
		t.Fatalf("ручка данных по HTTP: код %d, ожидался 426", got)
	}
	// Вебхуки и healthz не трогаем: на отказ платёжки уходят в суточные повторы.
	for _, p := range []string{"/webhook/yookassa", "/healthz"} {
		if got := call(p).Code; got != http.StatusOK {
			t.Fatalf("%s: код %d, ожидался 200", p, got)
		}
	}
	// Заголовки — всем.
	w := call("/api/miniapp/subscription")
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("nosniff не поставлен")
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("Referrer-Policy не поставлен: ссылка на подписку утекает при переходе")
	}
	// Мини-апп обязан открываться внутри Telegram — запрет фрейма ему нельзя.
	if got := call("/api/miniapp/me").Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("мини-апп получил запрет фрейма: %q", got)
	}
	if got := call("/api/cabinet/config").Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("кабинет без запрета фрейма: %q", got)
	}
}

// Лимитер покрывает деньги и вход, но не вебхуки.
func TestWrap_RateLimits(t *testing.T) {
	s := newServer(nil, nil)
	s.allowPlainHTTP = true
	h := s.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	hit := func(path string) int {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.RemoteAddr = "198.51.100.9:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	// Перебор промокодов — прямой убыток, лимит обязан сработать.
	got429 := false
	for i := 0; i < moneyLimitHits+3; i++ {
		if hit("/api/miniapp/promo") == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("перебор промокодов не ограничен")
	}
	// Вебхуки лимитировать нельзя ни при каких условиях.
	for i := 0; i < 200; i++ {
		if hit("/webhook/tribute") == http.StatusTooManyRequests {
			t.Fatal("вебхук платёжки попал под лимит — провайдер уйдёт в суточные повторы")
		}
	}
}

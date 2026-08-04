package remnawave

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"remnabot/internal/model"
)

// Панель за аддоном «Caddy with security» отвечает на запрос без ключа не
// ошибкой, а редиректом на портал входа. Клиент не должен ходить по редиректам:
// иначе Health увидит 200 со страницей логина и решит, что связь есть.
func TestCaddyRedirectNotFollowed(t *testing.T) {
	var portalHit bool
	mux := http.NewServeMux()
	mux.HandleFunc("/r", func(w http.ResponseWriter, r *http.Request) {
		portalHit = true
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body>login</body></html>"))
	})
	mux.HandleFunc("/api/system/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/r?redirect_url=%2Fapi%2Fsystem%2Fhealth")
		w.WriteHeader(http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := New(model.PanelConfig{BaseURL: srv.URL, APIToken: "T"}).Health(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка: панель закрыта прокси")
	}
	if portalHit {
		t.Fatal("клиент пошёл по редиректу на портал входа")
	}
	if !strings.Contains(err.Error(), "CADDY_AUTH_API_TOKEN") {
		t.Fatalf("в ошибке нет подсказки про ключ: %v", err)
	}
}

// caddy-security на неверный X-Api-Key отвечает плейн-текстом, панель — JSON.
// Претензия должна адресоваться правильному звену.
func TestCaddyBadKeyVsPanelToken(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string
	}{
		{"ключ прокси", "401 Unauthorized", "CADDY_AUTH_API_TOKEN"},
		{"токен панели", `{"message":"Unauthorized","statusCode":401}`, "API-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			err := New(model.PanelConfig{BaseURL: srv.URL, APIToken: "T"}).Health(context.Background())
			if err == nil {
				t.Fatal("ожидалась ошибка")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("ошибка %q не содержит %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// Прокси установщика eGames в сборке «панель+нода» отдаёт неавторизованным
// запросам сайт-заглушку с кодом 200. Health не должен считать это успехом.
func TestHealthRejectsDecoySite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<!doctype html>\n<html><head><title>nginx</title></head></html>"))
	}))
	defer srv.Close()

	err := New(model.PanelConfig{BaseURL: srv.URL, APIToken: "T"}).Health(context.Background())
	if err == nil {
		t.Fatal("страница-заглушка принята за живую панель")
	}
	if !strings.Contains(err.Error(), "веб-страница") {
		t.Fatalf("непонятная ошибка: %v", err)
	}
}

// Панель, до которой запрос доходит, отвечает JSON — такой Health проходит.
func TestHealthOKOnJSON(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"response":{"isConnected":true}}`))
	}))
	defer srv.Close()

	c := New(model.PanelConfig{BaseURL: srv.URL, APIToken: "T", APIKey: " key-123 "})
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	// Ключ Caddy идёт вдобавок к Bearer панели, а не вместо него, и приходит
	// обрезанным от случайных пробелов.
	if got.Get("X-Api-Key") != "key-123" {
		t.Errorf("X-Api-Key: %q", got.Get("X-Api-Key"))
	}
	if got.Get("Authorization") != "Bearer T" {
		t.Errorf("Authorization: %q", got.Get("Authorization"))
	}
}

func TestLooksHTML(t *testing.T) {
	html := []string{"<!DOCTYPE html><html>", " <html lang=\"en\">", "<!doctype HTML>"}
	notHTML := []string{"", "   ", `{"response":{}}`, "401 Unauthorized", "[1,2]"}
	for _, s := range html {
		if !looksHTML(s) {
			t.Errorf("looksHTML(%q) = false", s)
		}
	}
	for _, s := range notHTML {
		if looksHTML(s) {
			t.Errorf("looksHTML(%q) = true", s)
		}
	}
}

// Клиент не должен ждать вечно — таймаут остаётся на месте вместе с
// CheckRedirect.
func TestClientTimeoutPreserved(t *testing.T) {
	c := New(model.PanelConfig{BaseURL: "https://example.invalid"})
	if c.http.Timeout != 15*time.Second {
		t.Fatalf("таймаут: %v", c.http.Timeout)
	}
	if c.http.CheckRedirect == nil {
		t.Fatal("CheckRedirect не задан — клиент пойдёт по редиректам прокси")
	}
}

// Живые установки часто отвечают редиректом на канонический адрес: http→https
// от Caddy, www→apex. Путь при этом не меняется — по таким ходить надо, иначе
// у работавшего бота разом отвалятся все запросы к панели.
func TestCanonicalRedirectFollowed(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/system/stats") {
			_, _ = w.Write([]byte(`{"response":{"users":{"totalUsers":42}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"isConnected":true}}`))
	}))
	defer backend.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, backend.URL+r.URL.Path, http.StatusPermanentRedirect)
	}))
	defer front.Close()

	c := New(model.PanelConfig{BaseURL: front.URL, APIToken: "T"})
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health через канонизирующий редирект: %v", err)
	}
	n, err := c.SystemStats(context.Background())
	if err != nil || n != 42 {
		t.Fatalf("SystemStats = %d, %v", n, err)
	}
}

func TestSameSite(t *testing.T) {
	same := [][2]string{{"panel.example.com", "panel.example.com"}, {"www.panel.example.com", "panel.example.com"}, {"panel.example.com.", "panel.example.com"}}
	diff := [][2]string{
		{"panel.example.com", "evil.com"},
		{"panel.example.com.evil.com", "panel.example.com"},
		{"evil-panel.example.com", "panel.example.com"},
		// Апекс не роднится с произвольным поддоменом: иначе панель на
		// example.com делилась бы ключом с sub.example.com.
		{"sub.example.com", "example.com"},
		{"", "panel.example.com"},
	}
	for _, p := range same {
		if !sameSite(p[0], p[1]) {
			t.Errorf("sameSite(%q,%q) = false", p[0], p[1])
		}
	}
	for _, p := range diff {
		if sameSite(p[0], p[1]) {
			t.Errorf("sameSite(%q,%q) = true", p[0], p[1])
		}
	}
}

// Пустое тело при 401 — это скорее протухший API-token панели, чем прокси:
// подсказка не должна отправлять админа не туда.
func TestEmpty401BlamesPanelToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	err := New(model.PanelConfig{BaseURL: srv.URL, APIToken: "T"}).Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), "API-token") {
		t.Fatalf("ошибка: %v", err)
	}
}

// Страница-заглушка может начинаться с чего угодно — Content-Type выдаёт её
// вернее, чем первые байты тела.
func TestHealthRejectsHTMLByContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!-- " + strings.Repeat("x", 600) + " --><html></html>"))
	}))
	defer srv.Close()
	if err := New(model.PanelConfig{BaseURL: srv.URL, APIToken: "T"}).Health(context.Background()); err == nil {
		t.Fatal("HTML-страница принята за панель")
	}
}

// Редирект с https на открытый http не принимаем: Go снимает Authorization
// только при смене хоста, так что Bearer панели ушёл бы открытым текстом.
func TestDowngradeRedirectRefused(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"isConnected":true}}`))
	}))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	}))
	defer tls.Close()

	c := New(model.PanelConfig{BaseURL: tls.URL, APIToken: "T"})
	c.http.Transport = tls.Client().Transport
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("понижение до http принято")
	}
}

// 301/302/303 превращают запись в GET и теряют тело — «успех» такого запроса
// был бы молчаливым ничего-не-сделал. Переигрываем только по 307/308.
func TestUnsafeMethodNotReplayedOn302(t *testing.T) {
	var methods []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		_, _ = w.Write([]byte(`{"response":{}}`))
	}))
	defer backend.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		http.Redirect(w, r, backend.URL+r.URL.Path, http.StatusFound)
	}))
	defer front.Close()

	c := New(model.PanelConfig{BaseURL: front.URL, APIToken: "T"})
	resp, err := c.do(context.Background(), http.MethodPatch, "/api/users", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("статус %d — редирект всё-таки проглочен", resp.StatusCode)
	}
	for _, m := range methods {
		if m == http.MethodGet {
			t.Fatalf("PATCH превратился в GET: %v", methods)
		}
	}
}

// 307/308 метод и тело сохраняют — такой переход безопасен и должен работать.
func TestUnsafeMethodReplayedOn308(t *testing.T) {
	var methods []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		_, _ = w.Write([]byte(`{"response":{}}`))
	}))
	defer backend.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		http.Redirect(w, r, backend.URL+r.URL.Path, http.StatusPermanentRedirect)
	}))
	defer front.Close()

	c := New(model.PanelConfig{BaseURL: front.URL, APIToken: "T"})
	resp, err := c.do(context.Background(), http.MethodPatch, "/api/users", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(methods) != 2 || methods[1] != http.MethodPatch {
		t.Fatalf("308 не переиграл PATCH: статус %d, методы %v", resp.StatusCode, methods)
	}
}

// Слэш в конце — тоже канонизация, а не портал: по такому редиректу идём.
func TestTrailingSlashRedirectFollowed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/health/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"isConnected":true}}`))
	})
	mux.HandleFunc("/api/system/health", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/system/health/", http.StatusMovedPermanently)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if err := New(model.PanelConfig{BaseURL: srv.URL, APIToken: "T"}).Health(context.Background()); err != nil {
		t.Fatalf("редирект на слэш не пройден: %v", err)
	}
}

// Установка без Caddy: ключа нет вовсе — заголовок не появляется, а обычные
// вызовы панели работают ровно как раньше.
func TestNoKeyNoHeader(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if strings.HasSuffix(r.URL.Path, "/system/stats") {
			_, _ = w.Write([]byte(`{"response":{"users":{"totalUsers":3}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"isConnected":true}}`))
	}))
	defer srv.Close()

	c := New(model.PanelConfig{BaseURL: srv.URL, APIToken: "T"})
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if _, ok := got["X-Api-Key"]; ok {
		t.Fatalf("заголовок появился без ключа: %v", got["X-Api-Key"])
	}
	if n, err := c.SystemStats(context.Background()); err != nil || n != 3 {
		t.Fatalf("SystemStats = %d, %v", n, err)
	}
}

// Фронт, снимающий префикс пути (handle_path /panel*), — это тот же эндпоинт,
// а не портал входа: по такому редиректу идём.
func TestPrefixStripRedirectFollowed(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/system/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"response":{"isConnected":true}}`))
	}))
	defer backend.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, backend.URL+strings.TrimPrefix(r.URL.Path, "/panel"), http.StatusMovedPermanently)
	}))
	defer front.Close()

	if err := New(model.PanelConfig{BaseURL: front.URL + "/panel", APIToken: "T"}).Health(context.Background()); err != nil {
		t.Fatalf("редирект со снятием префикса не пройден: %v", err)
	}
}

func TestSameEndpoint(t *testing.T) {
	yes := [][2]string{
		{"/api/system/health", "/api/system/health"},
		{"/api/system/health/", "/api/system/health"},
		{"/panel/api/system/health", "/api/system/health"},
	}
	no := [][2]string{
		{"/r", "/api/system/health"},
		{"/restricted", "/api/system/health"},
		{"/", "/api/system/health"},
		{"/xapi/system/health", "/api/system/health"},
		{"/auth/login", "/api/users"},
	}
	for _, p := range yes {
		if !sameEndpoint(p[0], p[1]) {
			t.Errorf("sameEndpoint(%q,%q) = false", p[0], p[1])
		}
	}
	for _, p := range no {
		if sameEndpoint(p[0], p[1]) {
			t.Errorf("sameEndpoint(%q,%q) = true", p[0], p[1])
		}
	}
}

// Стойка из нескольких прокси не должна упираться в наш лимит раньше, чем в
// стандартный: как у Go, девять прыжков проходят, десятый обрывается.
func TestRedirectBudgetMatchesGo(t *testing.T) {
	for _, tc := range []struct {
		hops int
		ok   bool
	}{{4, true}, {9, true}, {10, false}} {
		var n int
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if n++; n <= tc.hops {
				http.Redirect(w, r, srv.URL+r.URL.Path, http.StatusFound)
				return
			}
			_, _ = w.Write([]byte(`{"response":{"isConnected":true}}`))
		}))
		err := New(model.PanelConfig{BaseURL: srv.URL, APIToken: "T"}).Health(context.Background())
		srv.Close()
		if tc.ok != (err == nil) {
			t.Errorf("%d прыжков: err = %v", tc.hops, err)
		}
	}
}

// Порт в сравнении хостов не участвует: тот же сервер, просто другой порт.
// А вот другое имя — уже другой сайт.
func TestNormHostIgnoresPort(t *testing.T) {
	for _, p := range [][2]string{{"panel.example.com:443", "panel.example.com"}, {"panel.example.com:8443", "panel.example.com"}} {
		if !sameSite(p[0], p[1]) {
			t.Errorf("sameSite(%q,%q) = false", p[0], p[1])
		}
	}
	if sameSite("other.example.com:443", "panel.example.com:443") {
		t.Error("разные имена посчитаны одним сайтом")
	}
}

// Портал caddy-security на кастомном маршруте (/health, /r) не должен
// притвориться «тем же эндпоинтом» за счёт совпадения хвоста пути.
func TestSameEndpointRejectsPortalRoutes(t *testing.T) {
	no := [][2]string{
		{"/health", "/api/system/health"},
		{"/stats", "/api/system/stats"},
		{"/users", "/api/users"},
		{"/r", "/api/system/health"},
	}
	for _, p := range no {
		if sameEndpoint(p[0], p[1]) {
			t.Errorf("sameEndpoint(%q,%q) = true — портал принят за панель", p[0], p[1])
		}
	}
	if !sameEndpoint("/panel/api/system/health", "/api/system/health") {
		t.Error("снятие префикса перестало распознаваться")
	}
}

// На чужой хост не идём вовсе: ответ оттуда бот принял бы за ответ панели.
func TestCrossHostRedirectRefused(t *testing.T) {
	var hit bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		_, _ = w.Write([]byte(`{"response":{"isConnected":true}}`))
	}))
	defer other.Close()
	foreign := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign+r.URL.Path, http.StatusFound)
	}))
	defer front.Close()

	err := New(model.PanelConfig{BaseURL: front.URL, APIToken: "T"}).Health(context.Background())
	if err == nil {
		t.Fatal("чужой хост принят за панель")
	}
	if hit {
		t.Fatal("клиент сходил на чужой хост")
	}
}

// Самый частый редирект — http→https из-за адреса панели с http. Подсказка
// должна вести к адресу, а не к несуществующему у человека аддону.
func TestHTTPSUpgradeHint(t *testing.T) {
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+r.Host+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer front.Close()

	c := New(model.PanelConfig{BaseURL: front.URL, APIToken: "T"})
	resp, err := c.do(context.Background(), http.MethodPatch, "/api/users", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	e := classifyHTTP(resp)
	if e == nil || !strings.Contains(e.Error(), "только по https") {
		t.Fatalf("подсказка не про https: %v", e)
	}
	if strings.Contains(e.Error(), "CADDY_AUTH_API_TOKEN") {
		t.Fatalf("подсказка уводит к аддону: %v", e)
	}
}

// Уход на чужой хост обрывается на любом прыжке, а не только на первом: Go на
// каждом прыжке пересобирает заголовки из первого запроса, поэтому сравнивать
// надо с ним, иначе ключ вернулся бы на втором прыжке.
func TestForeignHopRefusedMidChain(t *testing.T) {
	var foreignHit bool
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignHit = true
		_, _ = w.Write([]byte(`{"response":{"isConnected":true}}`))
	}))
	defer foreign.Close()
	foreignURL := strings.Replace(foreign.URL, "127.0.0.1", "localhost", 1)

	var hops int
	var own *httptest.Server
	own = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		if hops == 1 {
			// свой же хост, другой путь того же эндпоинта — идём
			http.Redirect(w, r, own.URL+r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		http.Redirect(w, r, foreignURL+r.URL.Path, http.StatusFound)
	}))
	defer own.Close()

	err := New(model.PanelConfig{BaseURL: own.URL, APIToken: "T", APIKey: "secret"}).Health(context.Background())
	if err == nil {
		t.Fatal("чужой хост принят за панель")
	}
	if hops < 2 {
		t.Fatalf("цепочка не отработала: %d прыжков", hops)
	}
	if foreignHit {
		t.Fatal("клиент дошёл до чужого хоста")
	}
}

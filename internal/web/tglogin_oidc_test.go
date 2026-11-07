package web

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// oidcMini — fakeMini плюс то, до чего доходит вход через Telegram.
type oidcMini struct {
	*fakeMini
	ensured  int64
	name     string
	newLogin bool
}

func (m *oidcMini) CabinetTGLoginNew() bool { return m.newLogin }

func (m *oidcMini) MiniStart(context.Context, int64, string, bool) string { return "" }
func (m *oidcMini) CabinetEnsureUser(_ context.Context, id int64)         { m.ensured = id }
func (m *oidcMini) CabinetBindName(_ context.Context, _ int64, username, first string) {
	m.name = username + "|" + first
}

type tgSigner struct {
	rsa  *rsa.PrivateKey
	ec   *ecdsa.PrivateKey
	hits atomic.Int32
	fail atomic.Bool
	kid  atomic.Value // kid RSA-ключа в выдаче; меняется в тесте смены ключей
	srv  *httptest.Server
}

func newTGSigner(t *testing.T) *tgSigner {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &tgSigner{rsa: rk, ec: ek}
	s.kid.Store("oidc-1")
	b64 := base64.RawURLEncoding.EncodeToString
	ecPub, err := ek.PublicKey.Bytes() // 0x04 || X || Y
	if err != nil {
		t.Fatal(err)
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.hits.Add(1)
		if s.fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		jwks, _ := json.Marshal(map[string]any{"keys": []map[string]any{
			{"kid": s.kid.Load(), "kty": "RSA", "alg": "RS256", "n": b64(rk.N.Bytes()), "e": "AQAB"},
			{"kid": "oidc-es256-1", "kty": "EC", "alg": "ES256", "crv": "P-256", "x": b64(ecPub[1:33]), "y": b64(ecPub[33:])},
			{"kid": "oidc-eddsa-1", "kty": "OKP", "alg": "EdDSA", "crv": "Ed25519", "x": b64(make([]byte, 32))},
		}})
		_, _ = w.Write(jwks)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *tgSigner) keys(now func() time.Time) *tgJWKS {
	return &tgJWKS{url: s.srv.URL, client: s.srv.Client(), now: now}
}

func (s *tgSigner) token(t *testing.T, alg, kid string, claims map[string]any) string {
	t.Helper()
	b64 := base64.RawURLEncoding.EncodeToString
	h, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	p, _ := json.Marshal(claims)
	in := b64(h) + "." + b64(p)
	d := sha256.Sum256([]byte(in))
	var sig []byte
	switch alg {
	case "RS256":
		var err error
		if sig, err = rsa.SignPKCS1v15(rand.Reader, s.rsa, crypto.SHA256, d[:]); err != nil {
			t.Fatal(err)
		}
	case "ES256":
		r, ss, err := ecdsa.Sign(rand.Reader, s.ec, d[:])
		if err != nil {
			t.Fatal(err)
		}
		sig = make([]byte, 64)
		r.FillBytes(sig[:32])
		ss.FillBytes(sig[32:])
	}
	return in + "." + b64(sig)
}

func goodClaims() map[string]any {
	now := time.Now().Unix()
	return map[string]any{
		"iss": tgOIDCIssuer, "aud": "123456", "sub": "77",
		"iat": now, "exp": now + 3600,
		"id": 1000000001, "preferred_username": "tester", "given_name": "Тест",
	}
}

func oidcServer(t *testing.T, s *tgSigner) (http.Handler, *oidcMini) {
	t.Helper()
	m := &oidcMini{fakeMini: &fakeMini{}}
	srv := newServer(nil, nil)
	srv.allowPlainHTTP = true
	srv.tgKeys = s.keys(time.Now)
	srv.SetMiniApp(m)
	return srv.wrap(srv.mux()), m
}

func TestCabinetTelegramIDToken(t *testing.T) {
	s := newTGSigner(t)
	with := func(k string, v any) map[string]any { c := goodClaims(); c[k] = v; return c }
	without := func(k string) map[string]any { c := goodClaims(); delete(c, k); return c }
	tampered := func() string {
		tok := s.token(t, "RS256", "oidc-1", goodClaims())
		forged, _ := json.Marshal(with("id", 1000000002))
		parts := splitJWT(tok)
		return parts[0] + "." + base64.RawURLEncoding.EncodeToString(forged) + "." + parts[2]
	}
	now := time.Now().Unix()
	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"RS256", s.token(t, "RS256", "oidc-1", goodClaims()), http.StatusOK},
		{"ES256", s.token(t, "ES256", "oidc-es256-1", goodClaims()), http.StatusOK},
		{"aud массивом", s.token(t, "RS256", "oidc-1", with("aud", []string{"1", "123456"})), http.StatusOK},
		{"aud числом", s.token(t, "RS256", "oidc-1", with("aud", 123456)), http.StatusOK},
		{"aud чужим числом", s.token(t, "RS256", "oidc-1", with("aud", 654321)), http.StatusUnauthorized},
		{"без kid", s.token(t, "RS256", "", goodClaims()), http.StatusOK},
		{"чужой бот", s.token(t, "RS256", "oidc-1", with("aud", "654321")), http.StatusUnauthorized},
		{"чужой издатель", s.token(t, "RS256", "oidc-1", with("iss", "https://evil.example.com")), http.StatusUnauthorized},
		{"истёк", s.token(t, "RS256", "oidc-1", with("exp", now-3600)), http.StatusUnauthorized},
		{"выдан давно", s.token(t, "RS256", "oidc-1", with("iat", now-3600)), http.StatusUnauthorized},
		{"выдан в будущем", s.token(t, "RS256", "oidc-1", with("iat", now+3600)), http.StatusUnauthorized},
		{"без id", s.token(t, "RS256", "oidc-1", without("id")), http.StatusUnauthorized},
		{"подменён id", tampered(), http.StatusUnauthorized},
		{"ключ не того алгоритма", s.token(t, "RS256", "oidc-es256-1", goodClaims()), http.StatusUnauthorized},
		{"alg none", s.token(t, "none", "oidc-1", goodClaims()), http.StatusUnauthorized},
		{"мусор", "a.b.c", http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, m := oidcServer(t, s)
			body, _ := json.Marshal(map[string]string{"id_token": c.token})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, gateReq(http.MethodPost, "/api/cabinet/auth/telegram", string(body), "", "203.0.113.60"))
			if w.Code != c.want {
				t.Fatalf("код %d, ждали %d: %s", w.Code, c.want, w.Body.String())
			}
			if c.want == http.StatusOK && m.ensured != 1000000001 {
				t.Fatalf("вход не на том id: %d", m.ensured)
			}
			if c.want != http.StatusOK && m.ensured != 0 {
				t.Fatal("отвергнутый токен не должен заводить пользователя")
			}
		})
	}
}

func splitJWT(tok string) []string {
	var out []string
	start := 0
	for i := range len(tok) {
		if tok[i] == '.' {
			out = append(out, tok[start:i])
			start = i + 1
		}
	}
	return append(out, tok[start:])
}

// Привязка берёт id и имя из проверенного токена, а не из тела запроса.
func TestCabinetBindIDToken(t *testing.T) {
	s := newTGSigner(t)
	h, m := oidcServer(t, s)
	body, _ := json.Marshal(map[string]any{"id_token": s.token(t, "RS256", "oidc-1", goodClaims()), "id": 1000000009, "username": "other"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, gateReq(http.MethodPost, "/api/cabinet/tg/bind", string(body), webToken(m.fakeMini, -7, 0), "203.0.113.61"))
	if w.Code != http.StatusOK {
		t.Fatalf("код %d: %s", w.Code, w.Body.String())
	}
	if m.bound != 1000000001 || m.name != "tester|Тест" {
		t.Fatalf("привязано %d, имя %q", m.bound, m.name)
	}
}

// Ключи кэшируются; неизвестный kid перечитывает их не чаще tgJWKSRetry,
// а после этого окна новый ключ Telegram подхватывается.
func TestTGJWKSCacheAndRotation(t *testing.T) {
	s := newTGSigner(t)
	now := time.Unix(1_800_000_000, 0)
	j := s.keys(func() time.Time { return now })
	for range 3 {
		if _, err := j.lookup("oidc-1", "RS256"); err != nil {
			t.Fatal(err)
		}
	}
	s.kid.Store("oidc-2")
	for range 3 {
		if _, err := j.lookup("oidc-2", "RS256"); err == nil {
			t.Fatal("новый ключ принят раньше окна повтора")
		}
	}
	if n := s.hits.Load(); n != 1 {
		t.Fatalf("запросов ключей: %d, ждали 1", n)
	}
	now = now.Add(tgJWKSRetry)
	if _, err := j.lookup("oidc-2", "RS256"); err != nil {
		t.Fatalf("новый ключ после окна повтора: %v", err)
	}
}

// Telegram недоступен: вход идёт по уже загруженным ключам, сбой — в лог.
func TestTGJWKSStaleKeysSurviveOutage(t *testing.T) {
	s := newTGSigner(t)
	now := time.Unix(1_800_000_000, 0)
	j := s.keys(func() time.Time { return now })
	var errs int
	j.onErr = func(error) { errs++ }
	if _, err := j.lookup("oidc-1", "RS256"); err != nil {
		t.Fatal(err)
	}
	s.fail.Store(true)
	now = now.Add(tgJWKSTTL + time.Minute)
	if _, err := j.lookup("oidc-1", "RS256"); err != nil {
		t.Fatalf("старый ключ при недоступном Telegram: %v", err)
	}
	if errs != 1 {
		t.Fatalf("сбой загрузки в лог: %d раз, ждали 1", errs)
	}
}

// Первая загрузка не удалась: ответ — «ключи не загружены» (а не молчаливый
// отказ), повтор через tgJWKSRetryEmpty, а не через минуту.
func TestTGJWKSFirstFetchFails(t *testing.T) {
	s := newTGSigner(t)
	now := time.Unix(1_800_000_000, 0)
	j := s.keys(func() time.Time { return now })
	var errs int
	j.onErr = func(error) { errs++ }
	s.fail.Store(true)
	if _, err := j.lookup("oidc-1", "RS256"); !errors.Is(err, errTGNoKeys) {
		t.Fatalf("ждали errTGNoKeys, получили %v", err)
	}
	s.fail.Store(false)
	if _, err := j.lookup("oidc-1", "RS256"); !errors.Is(err, errTGNoKeys) {
		t.Fatalf("повтор раньше окна: %v", err)
	}
	now = now.Add(tgJWKSRetryEmpty)
	if _, err := j.lookup("oidc-1", "RS256"); err != nil {
		t.Fatalf("после окна повтора: %v", err)
	}
	if errs != 1 || s.hits.Load() != 2 {
		t.Fatalf("сбоев в логе %d, запросов %d", errs, s.hits.Load())
	}
}

// Токен с nonce проходит только с verifier, из которого nonce получен.
func TestCabinetTelegramNonceBinding(t *testing.T) {
	s := newTGSigner(t)
	verifier := "test-verifier"
	sum := sha256.Sum256([]byte(verifier))
	c := goodClaims()
	c["nonce"] = hex.EncodeToString(sum[:])
	tok := s.token(t, "RS256", "oidc-1", c)
	for _, tc := range []struct {
		name     string
		verifier string
		want     int
	}{
		{"без verifier", "", http.StatusUnauthorized},
		{"чужой verifier", verifier + "x", http.StatusUnauthorized},
		{"слишком длинный", strings.Repeat("a", tgVerifierMax+1), http.StatusUnauthorized},
		{"свой verifier", verifier, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := oidcServer(t, s)
			body, _ := json.Marshal(map[string]string{"id_token": tok, "verifier": tc.verifier})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, gateReq(http.MethodPost, "/api/cabinet/auth/telegram", string(body), "", "203.0.113.63"))
			if w.Code != tc.want {
				t.Fatalf("код %d, ждали %d", w.Code, tc.want)
			}
		})
	}
}

// Один токен — один вход: повтор того же токена, в том числе с переписанной
// подписью ES256 (r, n-s), отвергается.
func TestCabinetTelegramReplay(t *testing.T) {
	s := newTGSigner(t)
	h, _ := oidcServer(t, s)
	tok := s.token(t, "ES256", "oidc-es256-1", goodClaims())
	parts := splitJWT(tok)
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	ss := new(big.Int).SetBytes(sig[32:])
	ss.Sub(elliptic.P256().Params().N, ss)
	ss.FillBytes(sig[32:])
	twin := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(sig)
	for i, c := range []struct {
		token string
		want  int
	}{{tok, http.StatusOK}, {tok, http.StatusUnauthorized}, {twin, http.StatusUnauthorized}} {
		body, _ := json.Marshal(map[string]string{"id_token": c.token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, gateReq(http.MethodPost, "/api/cabinet/auth/telegram", string(body), "", "203.0.113.64"))
		if w.Code != c.want {
			t.Fatalf("попытка %d: код %d, ждали %d", i+1, w.Code, c.want)
		}
	}
}

// client_id в конфиге — только при новом способе входа: по нему страница
// выбирает кнопку, иначе остаётся прежний виджет по bot_username.
func TestCabinetConfigClientID(t *testing.T) {
	s := newTGSigner(t)
	for _, tc := range []struct {
		newLogin bool
		want     int64
	}{{false, 0}, {true, 123456}} {
		m := &oidcMini{fakeMini: &fakeMini{}, newLogin: tc.newLogin}
		srv := newServer(nil, nil)
		srv.allowPlainHTTP = true
		srv.tgKeys = s.keys(time.Now)
		srv.SetMiniApp(m)
		w := httptest.NewRecorder()
		srv.wrap(srv.mux()).ServeHTTP(w, gateReq(http.MethodGet, "/api/cabinet/config", "", "", "203.0.113.62"))
		var cfg struct {
			BotUsername string `json:"bot_username"`
			ClientID    int64  `json:"client_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil || cfg.ClientID != tc.want || cfg.BotUsername != "testbot" {
			t.Fatalf("новый вход %v: client_id %d, bot_username %q (%v)", tc.newLogin, cfg.ClientID, cfg.BotUsername, err)
		}
	}
	if tgBotID("abc:def") != 0 || tgBotID("nocolon") != 0 {
		t.Fatal("кривой токен дал id бота")
	}
}

// Telegram отвечает отказом дольше окна повтора: вход, ждавший эту попытку
// под замком, не начинает сразу свою.
func TestTGJWKSSlowOutageSingleFetch(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		mu.Lock()
		now = now.Add(tgJWKSRetryEmpty + time.Second)
		mu.Unlock()
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	j := &tgJWKS{url: srv.URL, client: srv.Client(), now: clock}
	for range 2 {
		if _, err := j.lookup("oidc-1", "RS256"); !errors.Is(err, errTGNoKeys) {
			t.Fatalf("ждали errTGNoKeys, получили %v", err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("попыток загрузки: %d, ждали 1", n)
	}
}

// blockingJWKS — сервер ключей, который держит ответ до release(). release
// вызывается и при завершении теста: упавший тест не должен вешать закрытие
// сервера.
func blockingJWKS(t *testing.T, s *tgSigner) (url string, hits *atomic.Int32, started chan struct{}, release func()) {
	t.Helper()
	hits = new(atomic.Int32)
	started = make(chan struct{}, 8)
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		started <- struct{}{}
		<-gate
		resp, err := s.srv.Client().Get(s.srv.URL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(release)
	return srv.URL, hits, started, release
}

// Пока ключи перечитываются, вход с уже известным ключом не ждёт загрузку.
func TestTGJWKSRefreshDoesNotBlockKnownKey(t *testing.T) {
	s := newTGSigner(t)
	var mu sync.Mutex
	now := time.Unix(1_800_000_000, 0)
	j := &tgJWKS{url: s.srv.URL, client: s.srv.Client(), now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }}
	if _, err := j.lookup("oidc-1", "RS256"); err != nil {
		t.Fatal(err)
	}
	u, _, started, release := blockingJWKS(t, s)
	j.url = u
	mu.Lock()
	now = now.Add(tgJWKSTTL + time.Minute)
	mu.Unlock()
	go func() { _, _ = j.lookup("oidc-1", "RS256") }()
	<-started
	got := make(chan error, 1)
	go func() { _, err := j.lookup("oidc-es256-1", "ES256"); got <- err }()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("известный ключ во время загрузки: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("вход с известным ключом ждёт загрузку")
	}
	release()
}

// Холодный старт: одновременные входы ждут одну загрузку и получают ключ.
func TestTGJWKSWaitersShareFetch(t *testing.T) {
	s := newTGSigner(t)
	u, hits, started, release := blockingJWKS(t, s)
	j := &tgJWKS{url: u, client: s.srv.Client(), now: time.Now}
	got := make(chan error, 3)
	go func() { _, err := j.lookup("oidc-1", "RS256"); got <- err }()
	<-started
	for range 2 {
		go func() { _, err := j.lookup("oidc-1", "RS256"); got <- err }()
	}
	time.Sleep(100 * time.Millisecond)
	release()
	for range 3 {
		if err := <-got; err != nil {
			t.Fatalf("вход после общей загрузки: %v", err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("загрузок: %d, ждали 1", n)
	}
}

// Предупреждение о неподдерживаемом алгоритме — не чаще раза в час.
func TestTGAlgWarnThrottled(t *testing.T) {
	s := newTGSigner(t)
	var buf bytes.Buffer
	m := &oidcMini{fakeMini: &fakeMini{}}
	srv := newServer(slog.New(slog.NewTextHandler(&buf, nil)), nil)
	now := time.Unix(1_800_000_000, 0)
	srv.tgKeys = s.keys(func() time.Time { return now })
	srv.SetMiniApp(m)
	tok := s.token(t, "EdDSA", "oidc-eddsa-1", goodClaims())
	for range 3 {
		if _, err := srv.checkTGIDToken(tok, ""); !errors.Is(err, errTGAlg) {
			t.Fatalf("ждали errTGAlg, получили %v", err)
		}
	}
	now = now.Add(time.Hour)
	_, _ = srv.checkTGIDToken(tok, "")
	if n := strings.Count(buf.String(), "Login Widget"); n != 2 {
		t.Fatalf("предупреждений в логе: %d, ждали 2", n)
	}
}

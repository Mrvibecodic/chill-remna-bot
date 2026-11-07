package web

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Вход через Telegram на сайте — OpenID Connect: библиотека telegram-login.js
// отдаёт подписанный Telegram JWT (id_token). Прежний виджет telegram-widget.js
// Telegram закрыл — вместо кнопки он показывает страницу «deprecated».
const (
	tgOIDCIssuer  = "https://oauth.telegram.org"
	tgOIDCJWKSURL = "https://oauth.telegram.org/.well-known/jwks.json"
	tgJWKSTTL     = time.Hour
	// Неизвестный kid перечитывает ключи не чаще раза в минуту: мусорные
	// токены не должны превращать бота в генератор запросов к Telegram.
	tgJWKSRetry = time.Minute
	// Пока ключей нет вовсе, повтор чаще: без них не войдёт никто.
	tgJWKSRetryEmpty = 5 * time.Second
	tgJWKSTimeout    = 10 * time.Second
	tgVerifierMax    = 128
)

var (
	errTGAlg    = errors.New("алгоритм подписи не поддерживается")
	errTGNoKeys = errors.New("ключи Telegram ещё не загружены")
)

type tgKey struct {
	alg string
	pub crypto.PublicKey
}

// tgJWKS — кэш открытых ключей Telegram.
type tgJWKS struct {
	url    string
	client *http.Client
	now    func() time.Time
	// onErr получает сбой загрузки ключей: без него вход через Telegram
	// отвечает отказом, и причину в логе не найти.
	onErr func(error)

	mu      sync.Mutex
	keys    map[string]tgKey
	fetched time.Time
	tried   time.Time
	// fetching закрывается по окончании идущей загрузки; nil — загрузки нет.
	fetching chan struct{}
}

func newTGJWKS() *tgJWKS {
	return &tgJWKS{url: tgOIDCJWKSURL, client: &http.Client{Timeout: tgJWKSTimeout}, now: time.Now}
}

// lookup отдаёт ключ из кэша и при нужде перечитывает ключи. Загрузка идёт
// без замка: пока она длится, входы с известным ключом не ждут, а ждут только
// те, кому нужного ключа нет.
func (j *tgJWKS) lookup(kid, alg string) (tgKey, error) {
	j.mu.Lock()
	now := j.now()
	k, ok := j.find(kid, alg)
	stale := now.Sub(j.fetched) > tgJWKSTTL
	retry := tgJWKSRetry
	if j.keys == nil {
		retry = tgJWKSRetryEmpty
	}
	var fetchErr error
	switch {
	case (ok && !stale) || (!ok && j.fetching == nil && now.Sub(j.tried) < retry):
	case j.fetching != nil:
		if !ok {
			done := j.fetching
			j.mu.Unlock()
			<-done
			j.mu.Lock()
			k, ok = j.find(kid, alg)
		}
	case now.Sub(j.tried) >= retry:
		done := make(chan struct{})
		j.fetching = done
		j.mu.Unlock()
		// Свой контекст, а не запроса: закрытая посетителем вкладка не должна
		// оставлять бота без ключей до следующей попытки.
		ctx, cancel := context.WithTimeout(context.Background(), tgJWKSTimeout)
		keys, err := j.fetch(ctx)
		cancel()
		j.mu.Lock()
		// Отметка — по окончании попытки, иначе долгий отказ Telegram сразу
		// запускал бы следующую.
		j.tried = j.now()
		j.fetching = nil
		close(done)
		if err == nil {
			j.keys, j.fetched = keys, j.tried
		}
		fetchErr = err
		k, ok = j.find(kid, alg)
	}
	empty := j.keys == nil
	j.mu.Unlock()
	if fetchErr != nil && j.onErr != nil {
		j.onErr(fetchErr)
	}
	if !ok {
		if empty {
			return tgKey{}, errTGNoKeys
		}
		return tgKey{}, errAuth
	}
	return k, nil
}

// find ищет ключ по kid, а без kid — единственный ключ нужного алгоритма.
func (j *tgJWKS) find(kid, alg string) (tgKey, bool) {
	if kid != "" {
		k, ok := j.keys[kid]
		return k, ok && k.alg == alg
	}
	var out tgKey
	n := 0
	for _, k := range j.keys {
		if k.alg == alg {
			out, n = k, n+1
		}
	}
	return out, n == 1
}

func (j *tgJWKS) fetch(ctx context.Context) (map[string]tgKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	if err != nil {
		return nil, fmt.Errorf("ключи Telegram: %w", err)
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ключи Telegram: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ключи Telegram: HTTP %d", resp.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			Crv string `json:"crv"`
			N   string `json:"n"`
			E   string `json:"e"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64*1024))
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("ключи Telegram: %w", err)
	}
	keys := make(map[string]tgKey)
	for _, k := range doc.Keys {
		switch {
		case k.Kty == "RSA" && (k.Alg == "RS256" || k.Alg == ""):
			n, err1 := base64.RawURLEncoding.DecodeString(k.N)
			e, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 != nil || err2 != nil || len(n) < 256 || len(e) == 0 || len(e) > 4 {
				continue
			}
			ev := 0
			for _, b := range e {
				ev = ev<<8 | int(b)
			}
			keys[k.Kid] = tgKey{alg: "RS256", pub: &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: ev}}
		case k.Kty == "EC" && k.Crv == "P-256" && (k.Alg == "ES256" || k.Alg == ""):
			x, err1 := base64.RawURLEncoding.DecodeString(k.X)
			y, err2 := base64.RawURLEncoding.DecodeString(k.Y)
			if err1 != nil || err2 != nil || len(x) != 32 || len(y) != 32 {
				continue
			}
			pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
			if err != nil {
				continue
			}
			keys[k.Kid] = tgKey{alg: "ES256", pub: pub}
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("ключи Telegram: нет подходящих ключей")
	}
	return keys, nil
}

// tgBotID — числовой id бота из токена; он же Client ID для входа через
// Telegram и ожидаемый aud токена.
func tgBotID(botToken string) int64 {
	p, _, ok := strings.Cut(botToken, ":")
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(p, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// tgLoginUser — то, что берётся из проверенного id_token.
type tgLoginUser struct {
	ID        int64
	Username  string
	FirstName string
	// once и until — ключ токена для защиты от повтора и до какого времени
	// его помнить.
	once  [32]byte
	until time.Time
}

// tgReplay помнит принятые id_token, пока они могли бы пройти проверку
// срока: один токен — один вход.
type tgReplay struct {
	mu   sync.Mutex
	seen map[[32]byte]time.Time
}

func (r *tgReplay) first(key [32]byte, until, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = make(map[[32]byte]time.Time)
	}
	for k, t := range r.seen {
		if now.After(t) {
			delete(r.seen, k)
		}
	}
	if _, ok := r.seen[key]; ok {
		return false
	}
	r.seen[key] = until
	return true
}

// validateTelegramIDToken проверяет id_token входа через Telegram: подпись
// ключом Telegram, iss, aud = id бота, срок, и что токен выдан не раньше ttl
// назад (как у прежнего виджета: утёкший токен не меняется на пропуск часами).
// Если в токене есть nonce, браузер обязан предъявить verifier, из которого он
// получен (nonce = hex SHA-256 от verifier): токен, перехваченный по дороге,
// без него бесполезен.
func validateTelegramIDToken(keys *tgJWKS, idToken, verifier, botToken string, ttl time.Duration) (tgLoginUser, error) {
	botID := tgBotID(botToken)
	parts := strings.Split(idToken, ".")
	if botID == 0 || len(parts) != 3 {
		return tgLoginUser{}, errAuth
	}
	hb, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	pb, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return tgLoginUser{}, errAuth
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(hb, &hdr) != nil {
		return tgLoginUser{}, errAuth
	}
	if hdr.Alg != "RS256" && hdr.Alg != "ES256" {
		return tgLoginUser{}, fmt.Errorf("%w: %q", errTGAlg, hdr.Alg)
	}
	key, err := keys.lookup(hdr.Kid, hdr.Alg)
	if err != nil {
		return tgLoginUser{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch pub := key.pub.(type) {
	case *rsa.PublicKey:
		if rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig) != nil {
			return tgLoginUser{}, errAuth
		}
	case *ecdsa.PublicKey:
		if len(sig) != 64 || !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			return tgLoginUser{}, errAuth
		}
	default:
		return tgLoginUser{}, errAuth
	}

	var c struct {
		Iss       string          `json:"iss"`
		Aud       json.RawMessage `json:"aud"`
		Exp       json.Number     `json:"exp"`
		Iat       json.Number     `json:"iat"`
		ID        json.Number     `json:"id"`
		Username  string          `json:"preferred_username"`
		GivenName string          `json:"given_name"`
		Name      string          `json:"name"`
		Nonce     string          `json:"nonce"`
	}
	dec := json.NewDecoder(strings.NewReader(string(pb)))
	dec.UseNumber()
	if dec.Decode(&c) != nil || c.Iss != tgOIDCIssuer || !audHas(c.Aud, strconv.FormatInt(botID, 10)) {
		return tgLoginUser{}, errAuth
	}
	if c.Nonce != "" {
		sum := sha256.Sum256([]byte(verifier))
		if verifier == "" || len(verifier) > tgVerifierMax ||
			subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(c.Nonce)) != 1 {
			return tgLoginUser{}, errAuth
		}
	}
	now := keys.now()
	exp, err := c.Exp.Int64()
	if err != nil || now.After(time.Unix(exp, 0).Add(authClockSkew)) {
		return tgLoginUser{}, errAuth
	}
	until := time.Unix(exp, 0).Add(authClockSkew)
	if ttl > 0 {
		iat, err := c.Iat.Int64()
		if err != nil || iat <= 0 {
			return tgLoginUser{}, errAuth
		}
		if d := now.Sub(time.Unix(iat, 0)); d > ttl || d < -authClockSkew {
			return tgLoginUser{}, errAuth
		}
		if t := time.Unix(iat, 0).Add(ttl); t.Before(until) {
			until = t
		}
	}
	id, err := c.ID.Int64()
	if err != nil || id <= 0 {
		return tgLoginUser{}, errAuth
	}
	first := c.GivenName
	if first == "" {
		first = c.Name
	}
	// Ключ повтора — по содержимому, а не по строке токена: подпись ES256
	// можно перезаписать в другую верную, а содержимое — нет.
	return tgLoginUser{ID: id, Username: c.Username, FirstName: first, once: sha256.Sum256(pb), until: until}, nil
}

func audHas(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	// Стандарт OIDC требует строку, но id бота — число: на случай, если
	// Telegram пришлёт его без кавычек.
	var num json.Number
	if json.Unmarshal(raw, &num) == nil {
		return num.String() == want
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}

// checkTGIDToken — общая часть входа и привязки: проверка токена, отказ
// повторному токену и запись в лог не того алгоритма подписи (сбои загрузки
// ключей пишет onErr).
func (s *Server) checkTGIDToken(idToken, verifier string) (tgLoginUser, error) {
	u, err := validateTelegramIDToken(s.tgKeys, idToken, verifier, s.mini.MiniBotToken(), loginTTL)
	if err != nil {
		// Заголовок токена до проверки подписи может прислать кто угодно:
		// подсказка в логе — не чаще раза в час, чтобы её нельзя было
		// размножить.
		if errors.Is(err, errTGAlg) && s.log != nil {
			now := s.tgKeys.now().Unix()
			if last := s.tgAlgWarn.Load(); now-last >= int64(time.Hour/time.Second) && s.tgAlgWarn.CompareAndSwap(last, now) {
				s.log.Warn("вход через Telegram", "err", err, "hint", "в @BotFather → Login Widget → Advanced выберите RS256 или ES256")
			}
		}
		return tgLoginUser{}, err
	}
	if !s.tgUsed.first(u.once, u.until, s.tgKeys.now()) {
		return tgLoginUser{}, errAuth
	}
	return u, nil
}

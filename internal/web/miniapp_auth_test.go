package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// buildInitData produces a correctly-signed init data string for tests.
func buildInitData(botToken string, tgID int64, authDate time.Time) string {
	v := url.Values{}
	v.Set("auth_date", strconv.FormatInt(authDate.Unix(), 10))
	v.Set("user", `{"id":`+strconv.FormatInt(tgID, 10)+`,"first_name":"T"}`)
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(k + "=" + v.Get(k))
	}
	secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
	sig := hex.EncodeToString(hmacSHA256(secret, []byte(b.String())))
	v.Set("hash", sig)
	return v.Encode()
}

func TestValidateInitDataOK(t *testing.T) {
	tok := "123:abc"
	id, err := validateInitData(buildInitData(tok, 555, time.Now()), tok, time.Hour)
	if err != nil || id != 555 {
		t.Fatalf("id=%d err=%v", id, err)
	}
}

func TestValidateInitDataTampered(t *testing.T) {
	tok := "123:abc"
	data := buildInitData(tok, 555, time.Now())
	if _, err := validateInitData(data, "999:wrong", time.Hour); err == nil {
		t.Fatal("expected failure for wrong bot token")
	}
	if _, err := validateInitData(data+"&x=1", tok, time.Hour); err == nil {
		t.Fatal("expected failure for tampered payload")
	}
}

func TestValidateInitDataExpired(t *testing.T) {
	tok := "123:abc"
	data := buildInitData(tok, 555, time.Now().Add(-2*time.Hour))
	if _, err := validateInitData(data, tok, time.Hour); err == nil {
		t.Fatal("expected failure for expired auth_date")
	}
}

func TestJWTRoundTrip(t *testing.T) {
	key := jwtKey("123:abc")
	tok := issueJWT(777, false, key, time.Hour, 0)
	id, web, err := parseJWT(tok, key, 0)
	if err != nil || id != 777 || web {
		t.Fatalf("id=%d web=%v err=%v", id, web, err)
	}
	wtok := issueJWT(888, true, key, time.Hour, 0)
	if id, web, err := parseJWT(wtok, key, 0); err != nil || id != 888 || !web {
		t.Fatalf("web token: id=%d web=%v err=%v", id, web, err)
	}
	if _, _, err := parseJWT(tok, jwtKey("other"), 0); err == nil {
		t.Fatal("expected failure with wrong key")
	}
	if _, _, err := parseJWT(issueJWT(1, false, key, -time.Minute, 0), key, 0); err == nil {
		t.Fatal("expected failure for expired jwt")
	}
}

// Поколение сессий: поднятие версии отвергает все прежние пропуска. Это
// единственный способ «разлогинить всех» — ключ подписи выведен из токена бота
// и сам по себе не меняется даже при перезапуске.
func TestJWT_SessionVersionRevokes(t *testing.T) {
	key := jwtKey("123:abc")
	old := issueJWT(777, true, key, time.Hour, 3)
	if _, _, err := parseJWT(old, key, 3); err != nil {
		t.Fatalf("своё поколение обязано приниматься: %v", err)
	}
	if _, _, err := parseJWT(old, key, 4); err == nil {
		t.Fatal("после поднятия поколения прежний пропуск обязан быть отвергнут")
	}
	// Пропуска, выданные до появления поля, живут в нулевом поколении.
	if _, _, err := parseJWT(issueJWT(777, false, key, time.Hour, 0), key, 0); err != nil {
		t.Fatalf("нулевое поколение: %v", err)
	}
}

// Дата подписи «из будущего» отвергается: раньше разность была отрицательной и
// проверка «меньше срока» проходила всегда — перехваченная подпись с большой
// датой жила бы вечно.
func TestValidateInitData_RejectsFutureDate(t *testing.T) {
	tok := "123:abc"
	if _, err := validateInitData(buildInitData(tok, 555, time.Now().Add(time.Hour)), tok, time.Hour); err == nil {
		t.Fatal("дата из будущего обязана отвергаться")
	}
	// Небольшое расхождение часов допустимо.
	if _, err := validateInitData(buildInitData(tok, 555, time.Now().Add(time.Minute)), tok, time.Hour); err != nil {
		t.Fatalf("минута расхождения часов должна приниматься: %v", err)
	}
}

// sanity: ensure our test signer matches the hex format expectation
var _ = hmac.Equal
var _ = sha256.Size

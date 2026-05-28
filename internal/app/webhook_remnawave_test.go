package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestVerifyRemnawaveSignature_OK — корректная подпись принимается.
func TestVerifyRemnawaveSignature_OK(t *testing.T) {
	secret := "topsecret"
	body := []byte(`{"event":"user.expired","data":{"telegramId":42}}`)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	sig := hex.EncodeToString(m.Sum(nil))

	if err := verifyRemnawaveSignature(sig, secret, body); err != nil {
		t.Fatalf("ожидался OK, получили: %v", err)
	}
	// с префиксом sha256= тоже должно работать
	if err := verifyRemnawaveSignature("sha256="+sig, secret, body); err != nil {
		t.Fatalf("ожидался OK c префиксом, получили: %v", err)
	}
}

// TestVerifyRemnawaveSignature_Bad — подмена тела ломает подпись.
func TestVerifyRemnawaveSignature_Bad(t *testing.T) {
	secret := "topsecret"
	body := []byte(`{"event":"user.expired"}`)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	sig := hex.EncodeToString(m.Sum(nil))

	tampered := []byte(`{"event":"user.expired","data":{"telegramId":99}}`)
	if err := verifyRemnawaveSignature(sig, secret, tampered); err == nil {
		t.Fatalf("ожидалась ошибка при tampered теле")
	}
}

// TestVerifyRemnawaveSignature_EmptySecret — без секрета валидация
// пропускается (для локальной отладки).
func TestVerifyRemnawaveSignature_EmptySecret(t *testing.T) {
	if err := verifyRemnawaveSignature("", "", []byte("anything")); err != nil {
		t.Fatalf("при пустом секрете ошибок быть не должно: %v", err)
	}
}

// TestVerifyRemnawaveSignature_MissingHeader — секрет есть, заголовка нет → 403.
func TestVerifyRemnawaveSignature_MissingHeader(t *testing.T) {
	if err := verifyRemnawaveSignature("", "secret", []byte("body")); err == nil {
		t.Fatalf("ожидалась ошибка из-за отсутствия заголовка")
	}
}

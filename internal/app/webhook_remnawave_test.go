package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyRemnawaveSignature_OK(t *testing.T) {
	secret := "topsecret"
	body := []byte(`{"event":"user.expired","data":{"telegramId":42}}`)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	sig := hex.EncodeToString(m.Sum(nil))

	if err := verifyRemnawaveSignature(sig, secret, body); err != nil {
		t.Fatalf("ожидался OK, получили: %v", err)
	}

	if err := verifyRemnawaveSignature("sha256="+sig, secret, body); err != nil {
		t.Fatalf("ожидался OK c префиксом, получили: %v", err)
	}
}

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

func TestVerifyRemnawaveSignature_EmptySecret(t *testing.T) {
	if err := verifyRemnawaveSignature("", "", []byte("anything")); err != nil {
		t.Fatalf("при пустом секрете ошибок быть не должно: %v", err)
	}
}

func TestVerifyRemnawaveSignature_MissingHeader(t *testing.T) {
	if err := verifyRemnawaveSignature("", "secret", []byte("body")); err == nil {
		t.Fatalf("ожидалась ошибка из-за отсутствия заголовка")
	}
}

// Панель >= 2.8.20 шлёт одно событие user.expiration, а конкретный интервал
// кладёт в meta.expiration рядом с data (а не внутрь неё). Отрицательный
// интервал — столько часов ДО истечения.
func TestRemnawaveWebhook_ExpirationBefore(t *testing.T) {
	a, fm, _ := newTestApp(t)
	body := []byte(`{"scope":"user","event":"user.expiration","data":{"telegramId":42},"meta":{"expiration":-72}}`)

	handled, err := a.HandleRemnawaveWebhook(context.Background(), "", body)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if countContains(fm.texts, "Осталось меньше 3 дн.") != 1 {
		t.Fatalf("ожидалось предупреждение на 3 дня, тексты: %v", fm.texts)
	}
}

// Интервал, не кратный суткам, показывается в часах.
func TestRemnawaveWebhook_ExpirationBeforeHours(t *testing.T) {
	a, fm, _ := newTestApp(t)
	body := []byte(`{"scope":"user","event":"user.expiration","data":{"telegramId":42},"meta":{"expiration":-5}}`)

	if _, err := a.HandleRemnawaveWebhook(context.Background(), "", body); err != nil {
		t.Fatalf("err=%v", err)
	}
	if countContains(fm.texts, "Осталось меньше 5 ч.") != 1 {
		t.Fatalf("ожидалось предупреждение на 5 часов, тексты: %v", fm.texts)
	}
}

// Положительный интервал — подписка уже истекла столько часов назад:
// это напоминание продлить, а не предупреждение.
func TestRemnawaveWebhook_ExpirationAfter(t *testing.T) {
	a, fm, _ := newTestApp(t)
	body := []byte(`{"scope":"user","event":"user.expiration","data":{"telegramId":42},"meta":{"expiration":24}}`)

	if _, err := a.HandleRemnawaveWebhook(context.Background(), "", body); err != nil {
		t.Fatalf("err=%v", err)
	}
	if countContains(fm.texts, "Подписка истекла") != 1 {
		t.Fatalf("ожидалось напоминание об истёкшей подписке, тексты: %v", fm.texts)
	}
}

// Без meta (или с пустым интервалом) шлём общее предупреждение без числа —
// молчать в этом случае нельзя.
func TestRemnawaveWebhook_ExpirationNoMeta(t *testing.T) {
	a, fm, _ := newTestApp(t)
	body := []byte(`{"scope":"user","event":"user.expiration","data":{"telegramId":42}}`)

	if _, err := a.HandleRemnawaveWebhook(context.Background(), "", body); err != nil {
		t.Fatalf("err=%v", err)
	}
	if countContains(fm.texts, "Подписка скоро истечёт") != 1 || countContains(fm.texts, "Осталось") != 0 {
		t.Fatalf("ожидалось общее предупреждение без числа, тексты: %v", fm.texts)
	}
}

// Панели 2.7.0–2.8.19 продолжают работать на старых событиях.
func TestRemnawaveWebhook_LegacyExpiresIn(t *testing.T) {
	a, fm, _ := newTestApp(t)
	body := []byte(`{"event":"user.expires_in_48_hours","data":{"telegramId":42}}`)

	if _, err := a.HandleRemnawaveWebhook(context.Background(), "", body); err != nil {
		t.Fatalf("err=%v", err)
	}
	if countContains(fm.texts, "Осталось меньше 2 дн.") != 1 {
		t.Fatalf("ожидалось предупреждение на 2 дня, тексты: %v", fm.texts)
	}
}

// Сутками показываем только от двух суток: «меньше 1 дн.» звучит хуже, чем
// «меньше 24 ч», а в английском давало битое «Less than 1 days left».
func TestRemnawaveWebhook_ExpirationOneDayStaysHours(t *testing.T) {
	a, fm, _ := newTestApp(t)
	body := []byte(`{"scope":"user","event":"user.expiration","data":{"telegramId":42},"meta":{"expiration":-24}}`)

	if _, err := a.HandleRemnawaveWebhook(context.Background(), "", body); err != nil {
		t.Fatalf("err=%v", err)
	}
	if countContains(fm.texts, "Осталось меньше 24 ч.") != 1 {
		t.Fatalf("ожидались часы, тексты: %v", fm.texts)
	}
}

// Неожиданный тип поля в meta не должен ронять разбор всего конверта: иначе
// вебхук отвечает 500 и теряется ЛЮБОЕ событие, включая торрент-отчёты.
func TestRemnawaveWebhook_BadMetaDoesNotBreakEvent(t *testing.T) {
	a, fm, _ := newTestApp(t)
	body := []byte(`{"scope":"user","event":"user.expired","data":{"telegramId":42},"meta":{"expiration":"-72"}}`)

	handled, err := a.HandleRemnawaveWebhook(context.Background(), "", body)
	if err != nil || !handled {
		t.Fatalf("событие должно быть обработано: handled=%v err=%v", handled, err)
	}
	if countContains(fm.texts, "Подписка истекла") != 1 {
		t.Fatalf("тексты: %v", fm.texts)
	}
}

package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/web"
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
	if err := verifyRemnawaveSignature("", "", []byte("anything")); !errors.Is(err, errRWNoSecret) {
		t.Fatalf("без секрета запрос должен быть отклонён, получили: %v", err)
	}
}

// rwPost доставляет событие панели с верной подписью. Секрет задаёт сам,
// если в приложении его ещё нет.
func rwPost(t *testing.T, a *App, body []byte) (bool, error) {
	t.Helper()
	a.mu.Lock()
	if a.botCfg == nil {
		a.botCfg = &model.BotConfig{}
	}
	if a.botCfg.Webhook.RemnawaveSecret == "" {
		a.botCfg.Webhook.RemnawaveSecret = "s"
	}
	secret := a.botCfg.Webhook.RemnawaveSecret
	a.mu.Unlock()
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return a.HandleRemnawaveWebhook(context.Background(), hex.EncodeToString(m.Sum(nil)), body)
}

// rwDeliver — то же, но доставка обязана пройти.
func rwDeliver(t *testing.T, a *App, body string) {
	t.Helper()
	if _, err := rwPost(t, a, []byte(body)); err != nil {
		t.Fatalf("доставка события: %v", err)
	}
}

// Непрошедшая проверка подписи — отказ 401, а не сбой: событие не
// обрабатывается и человеку ничего не уходит.
func TestRemnawaveWebhook_BadSignatureUnauthorized(t *testing.T) {
	body := []byte(`{"scope":"user","event":"user.expired","data":{"telegramId":42}}`)
	for _, tc := range []struct{ name, sig string }{
		{"подписи нет", ""},
		{"подпись не hex", "zz"},
		{"подпись чужая", "00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fm, _ := newTestApp(t)
			a.botCfg = &model.BotConfig{}
			a.botCfg.Webhook.RemnawaveSecret = "s"
			handled, err := a.HandleRemnawaveWebhook(context.Background(), tc.sig, body)
			if handled || !errors.Is(err, web.ErrUnauthorized) {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			if len(fm.texts) != 0 {
				t.Fatalf("сообщений быть не должно: %v", fm.texts)
			}
		})
	}
}

// Без секрета событие не обрабатывается: человеку ничего не уходит, а
// администратор один раз узнаёт причину.
func TestRemnawaveWebhook_NoSecretRejected(t *testing.T) {
	body := []byte(`{"scope":"user","event":"user.expired","data":{"telegramId":42}}`)
	post := func(t *testing.T, a *App, sig string) {
		t.Helper()
		handled, err := a.HandleRemnawaveWebhook(context.Background(), sig, body)
		if handled || !errors.Is(err, web.ErrUnauthorized) {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
	}
	const notice = "секрет в боте не задан"

	t.Run("одно сообщение администратору", func(t *testing.T) {
		a, fm, _ := newTestApp(t)
		post(t, a, "00")
		post(t, a, "00")
		if countContains(fm.texts, "Подписка истекла") != 0 {
			t.Fatalf("без секрета человеку писать нельзя: %v", fm.texts)
		}
		if len(fm.texts) != 1 || countContains(fm.texts, notice) != 1 {
			t.Fatalf("ожидалось одно сообщение администратору, тексты: %v", fm.texts)
		}
	})
	t.Run("запрос без подписи — не панель", func(t *testing.T) {
		a, fm, _ := newTestApp(t)
		post(t, a, "")
		if len(fm.texts) != 0 {
			t.Fatalf("о запросе без подписи сообщать нечего: %v", fm.texts)
		}
	})
	t.Run("мессенджер ещё не поднят", func(t *testing.T) {
		a, fm, _ := newTestApp(t)
		a.msg = nil
		post(t, a, "00")
		a.msg = fm
		post(t, a, "00")
		if countContains(fm.texts, notice) != 1 {
			t.Fatalf("сообщение должно дойти, когда мессенджер поднимется: %v", fm.texts)
		}
	})
	t.Run("отказ Telegram не закрывает сообщение", func(t *testing.T) {
		a, fm, _ := newTestApp(t)
		fm.kbFail = true
		post(t, a, "00")
		fm.kbFail = false
		post(t, a, "00")
		if countContains(fm.texts, notice) != 1 {
			t.Fatalf("повтор раньше паузы: %v", fm.texts)
		}
		a.thrMu.Lock()
		a.rwNoSecretTried = time.Now().Add(-rwNoSecretRetry - time.Second)
		a.thrMu.Unlock()
		post(t, a, "00")
		post(t, a, "00")
		if countContains(fm.texts, notice) != 2 {
			t.Fatalf("после паузы сообщение должно дойти ровно один раз: %v", fm.texts)
		}
	})
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

	handled, err := rwPost(t, a, body)
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

	if _, err := rwPost(t, a, body); err != nil {
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

	if _, err := rwPost(t, a, body); err != nil {
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

	if _, err := rwPost(t, a, body); err != nil {
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

	if _, err := rwPost(t, a, body); err != nil {
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

	if _, err := rwPost(t, a, body); err != nil {
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

	handled, err := rwPost(t, a, body)
	if err != nil || !handled {
		t.Fatalf("событие должно быть обработано: handled=%v err=%v", handled, err)
	}
	if countContains(fm.texts, "Подписка истекла") != 1 {
		t.Fatalf("тексты: %v", fm.texts)
	}
}

// Порог расхода трафика — предупреждение, а не «трафик исчерпан»: лимит ещё
// не выбран и доступ работает.
func TestRemnawaveWebhook_TrafficThreshold(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"порог с процентом", `{"scope":"user","event":"user.bandwidth_usage_threshold_reached","data":{"telegramId":42,"lastTriggeredThreshold":80}}`, "Израсходовано 80% трафика"},
		{"порог без процента", `{"scope":"user","event":"user.bandwidth_usage_threshold_reached","data":{"telegramId":42}}`, "Трафик заканчивается"},
		{"процент вне диапазона", `{"scope":"user","event":"user.bandwidth_usage_threshold_reached","data":{"telegramId":42,"lastTriggeredThreshold":250}}`, "Трафик заканчивается"},
		{"лимит исчерпан", `{"scope":"user","event":"user.limited","data":{"telegramId":42,"lastTriggeredThreshold":80}}`, "Трафик исчерпан"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, fm, _ := newTestApp(t)
			handled, err := rwPost(t, a, []byte(tc.body))
			if err != nil || !handled {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			if len(fm.texts) != 1 || countContains(fm.texts, tc.want) != 1 {
				t.Fatalf("ожидалось одно сообщение с %q, тексты: %v", tc.want, fm.texts)
			}
		})
	}
}

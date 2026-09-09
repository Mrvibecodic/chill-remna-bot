package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// cabinetApp — кабинет включён, почта настроена на заведомо недоступный сервер.
//
// Настроена намеренно: без неё гейт подтверждения выключается (нечем
// подтверждать), и половина проверок ниже проходила бы «сама собой». Отправка
// при этом падает, но ни одна из проверок на её успех не опирается — ссылки
// выдаются до похода к серверу.
func cabinetApp(t *testing.T) (*App, *fakeStore) {
	t.Helper()
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeCabinet()
	a.botCfg.Cabinet.Enabled = true
	a.botCfg.Webhook.PublicBaseURL = "https://example.com"
	a.botCfg.Mail = model.MailConfig{
		Enabled: true, Mode: model.MailModeSMTP,
		From: "noreply@example.com", Host: "127.0.0.1", Port: 1,
	}
	a.botCfg.NormalizeMail()
	return a, fs
}

// Новый аккаунт не подтверждён, и платные действия ему закрыты. Проверка не
// про удобство: почта участвует в списках допущенных к тарифам, поэтому
// неподтверждённый адрес — это чужой адрес.
func TestCabinetMoneyBlockedUntilVerified(t *testing.T) {
	a, fs := cabinetApp(t)
	ctx := context.Background()

	id, err := a.CabinetEmailRegister(ctx, "new@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	if !a.CabinetMoneyBlocked(ctx, id) {
		t.Fatal("неподтверждённому аккаунту платные действия обязаны быть закрыты")
	}
	if err := fs.SetWebUserVerified(ctx, id, ""); err != nil {
		t.Fatalf("подтверждение: %v", err)
	}
	if a.CabinetMoneyBlocked(ctx, id) {
		t.Fatal("подтверждённому аккаунту закрывать нечего")
	}
}

// Аккаунты, заведённые до появления подтверждения, остаются рабочими: миграция
// проставляет им дату, и гейт их не трогает. Иначе апдейт разом отрезал бы всех
// действующих покупателей от оплаты.
func TestCabinetGrandfatheredAccountNotBlocked(t *testing.T) {
	a, fs := cabinetApp(t)
	ctx := context.Background()
	old := int64(-100)
	fs.webUsers = map[string]*model.WebUser{"old@example.com": {
		TgID: old, Email: "old@example.com", VerifiedAt: "2026-01-01T00:00:00Z",
	}}
	if a.CabinetMoneyBlocked(ctx, old) {
		t.Fatal("старый аккаунт с проставленной датой блокировать нельзя")
	}
}

// Пока почта не настроена, гейт не применяется вовсе: запирать оплату за то,
// чего человеку не предложили, нельзя.
func TestCabinetMoneyOpenWhenMailOff(t *testing.T) {
	a, _ := cabinetApp(t)
	ctx := context.Background()
	a.botCfg.Mail.Enabled = false
	id, err := a.CabinetEmailRegister(ctx, "nomail@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	if a.CabinetMoneyBlocked(ctx, id) {
		t.Fatal("без настроенной почты гейт обязан молчать")
	}
}

// Телеграм-аккаунт под гейт не попадает никогда: его личность подтверждена
// подписью Telegram, а не введённым адресом.
func TestCabinetMoneyOpenForTelegram(t *testing.T) {
	a, _ := cabinetApp(t)
	if a.CabinetMoneyBlocked(context.Background(), 777) {
		t.Fatal("телеграм-аккаунту гейт подтверждения не адресован")
	}
}

// Ссылка из письма одноразовая: повторное открытие её не принимает.
func TestVerifyLinkIsSingleUse(t *testing.T) {
	a, fs := cabinetApp(t)
	ctx := context.Background()
	id, err := a.CabinetEmailRegister(ctx, "one@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	tok, err := a.newEmailToken(ctx, id, "one@example.com", model.EmailPurposeVerify, verifyTTL)
	if err != nil {
		t.Fatalf("ссылка: %v", err)
	}
	if err := a.CabinetVerifyEmail(ctx, tok); err != nil {
		t.Fatalf("первое подтверждение: %v", err)
	}
	wu, _ := fs.GetWebUserByTgID(ctx, id)
	if wu == nil || wu.VerifiedAt == "" {
		t.Fatal("адрес не помечен подтверждённым")
	}
	if err := a.CabinetVerifyEmail(ctx, tok); err == nil {
		t.Fatal("та же ссылка обязана быть отвергнута во второй раз")
	}
	if err := a.CabinetVerifyEmail(ctx, "постороннее значение"); err == nil {
		t.Fatal("чужое значение обязано быть отвергнуто")
	}
}

// Выдача новой ссылки гасит прежнюю: иначе старое письмо остаётся рабочим
// ключом ещё сутки.
func TestNewLinkRevokesPrevious(t *testing.T) {
	a, _ := cabinetApp(t)
	ctx := context.Background()
	id, err := a.CabinetEmailRegister(ctx, "two@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	first, err := a.newEmailToken(ctx, id, "two@example.com", model.EmailPurposeVerify, verifyTTL)
	if err != nil {
		t.Fatalf("первая ссылка: %v", err)
	}
	if _, err := a.newEmailToken(ctx, id, "two@example.com", model.EmailPurposeVerify, verifyTTL); err != nil {
		t.Fatalf("вторая ссылка: %v", err)
	}
	if err := a.CabinetVerifyEmail(ctx, first); err == nil {
		t.Fatal("прежняя ссылка обязана перестать работать")
	}
}

// Смена пароля требует старый и выбивает прочие сессии поднятием поколения.
func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	a, fs := cabinetApp(t)
	ctx := context.Background()
	id, err := a.CabinetEmailRegister(ctx, "pw@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	before := a.SessionEpoch(ctx, id)

	if err := a.CabinetChangePassword(ctx, id, "неверный", "password2"); err == nil {
		t.Fatal("без верного текущего пароля менять нельзя")
	}
	if err := a.CabinetChangePassword(ctx, id, "password1", "коротк"); err == nil {
		t.Fatal("короткий пароль обязан быть отвергнут")
	}
	if err := a.CabinetChangePassword(ctx, id, "password1", "password1"); err == nil {
		t.Fatal("тот же самый пароль обязан быть отвергнут")
	}
	if err := a.CabinetChangePassword(ctx, id, "password1", "password2"); err != nil {
		t.Fatalf("смена пароля: %v", err)
	}
	if after := a.SessionEpoch(ctx, id); after == before {
		t.Fatal("после смены пароля поколение пропусков обязано вырасти")
	}
	if _, err := a.CabinetEmailLogin(ctx, "pw@example.com", "password2"); err != nil {
		t.Fatalf("вход новым паролем: %v", err)
	}
	if _, err := a.CabinetEmailLogin(ctx, "pw@example.com", "password1"); err == nil {
		t.Fatal("старый пароль обязан перестать работать")
	}
	_ = fs
}

// Сброс по ссылке ставит пароль И подтверждает адрес: кликнувший по ссылке
// доказал, что ящик его. Этим же чинится аккаунт, заведённый на чужой адрес —
// настоящий владелец забирает его себе.
func TestResetPasswordVerifiesEmail(t *testing.T) {
	a, fs := cabinetApp(t)
	ctx := context.Background()
	id, err := a.CabinetEmailRegister(ctx, "lost@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	tok, err := a.newEmailToken(ctx, id, "lost@example.com", model.EmailPurposeReset, resetTTL)
	if err != nil {
		t.Fatalf("ссылка: %v", err)
	}
	// Ссылка на сброс не годится для подтверждения и наоборот: назначение —
	// часть проверки, иначе одна ссылка работала бы вместо другой.
	if err := a.CabinetVerifyEmail(ctx, tok); err == nil {
		t.Fatal("ссылка сброса не должна подходить для подтверждения")
	}
	tok, err = a.newEmailToken(ctx, id, "lost@example.com", model.EmailPurposeReset, resetTTL)
	if err != nil {
		t.Fatalf("ссылка: %v", err)
	}
	got, err := a.CabinetResetPassword(ctx, tok, "password3")
	if err != nil || got != id {
		t.Fatalf("сброс: id=%d err=%v", got, err)
	}
	wu, _ := fs.GetWebUserByTgID(ctx, id)
	if wu == nil || wu.VerifiedAt == "" {
		t.Fatal("успешный сброс обязан подтверждать адрес")
	}
	if _, err := a.CabinetEmailLogin(ctx, "lost@example.com", "password3"); err != nil {
		t.Fatalf("вход после сброса: %v", err)
	}
}

// «Забыли пароль» отвечает одинаково на любой адрес — иначе форма становится
// проверялкой «есть ли у вас аккаунт».
func TestForgotPasswordDoesNotRevealAccounts(t *testing.T) {
	a, _ := cabinetApp(t)
	ctx := context.Background()
	// Ни один из вызовов не имеет права ни паниковать, ни возвращать признак
	// существования аккаунта: возвращаемого значения у него нет вовсе.
	a.CabinetForgotPassword(ctx, "nobody@example.com")
	a.CabinetForgotPassword(ctx, "мусор")
	if _, err := a.CabinetEmailRegister(ctx, "known@example.com", "password1"); err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	a.CabinetForgotPassword(ctx, "known@example.com")
}

// Адрес, на который нельзя отправить письмо, не регистрируется: иначе человек
// навсегда застревает на «подтвердите почту».
func TestRegisterRejectsUnsendableAddress(t *testing.T) {
	a, _ := cabinetApp(t)
	ctx := context.Background()
	for _, bad := range []string{"нет-собаки", "a@b", "a@@example.com", "a b@example.com", "a@example.com\r\nBcc: other@example.com"} {
		if _, err := a.CabinetEmailRegister(ctx, bad, "password1"); err == nil {
			t.Fatalf("адрес %q обязан быть отвергнут", bad)
		}
	}
}

// Порог отправки: второе письмо подряд не уходит.
func TestMailQuotaBlocksRapidResend(t *testing.T) {
	a, _ := cabinetApp(t)
	ctx := context.Background()
	id, err := a.CabinetEmailRegister(ctx, "quota@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	if _, err := a.newEmailToken(ctx, id, "quota@example.com", model.EmailPurposeVerify, verifyTTL); err != nil {
		t.Fatalf("ссылка: %v", err)
	}
	if err := a.mailQuota(ctx, id, model.EmailPurposeVerify); err == nil {
		t.Fatal("второе письмо подряд обязано упереться в порог")
	}
	if err := a.mailQuota(ctx, id, model.EmailPurposeReset); err != nil {
		t.Fatalf("порог считается по назначению письма отдельно: %v", err)
	}
}

// Ссылка в письме строится от публичного адреса кабинета. Без него письмо
// бессмысленно, и отправка обязана отказаться, а не уйти с битой ссылкой.
func TestVerifyMailNeedsPublicURL(t *testing.T) {
	a, _ := cabinetApp(t)
	ctx := context.Background()
	// Аккаунт заводим при выключенной почте, иначе фоновая отправка при
	// регистрации сама израсходует порог, и проверка упрётся в него, а не в
	// отсутствие адреса.
	a.botCfg.Mail.Enabled = false
	id, err := a.CabinetEmailRegister(ctx, "nourl@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	a.botCfg.Mail.Enabled = true
	a.botCfg.Webhook.PublicBaseURL = ""
	a.botCfg.Webhook.Domain = ""
	if err := a.sendVerifyMail(ctx, id, "nourl@example.com"); err == nil ||
		!strings.Contains(err.Error(), "адрес") {
		t.Fatalf("без публичного адреса отправка обязана отказаться: %v", err)
	}
}

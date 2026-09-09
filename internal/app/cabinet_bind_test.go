package app

import (
	"context"
	"testing"

	"remnabot/internal/model"
)

// Привязка переносит аккаунт целиком: подписка, баланс и история обязаны
// оказаться на телеграмном идентификаторе, а прежнего аккаунта — не остаться.
func TestBindTelegramMovesAccount(t *testing.T) {
	a, fs := cabinetApp(t)
	ctx := context.Background()

	id, err := a.CabinetEmailRegister(ctx, "bind@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	if err := fs.AddBalance(ctx, id, 12345); err != nil {
		t.Fatalf("баланс: %v", err)
	}
	if err := fs.SetSubExpiry(ctx, id, "2030-01-01T00:00:00Z", "paid"); err != nil {
		t.Fatalf("срок: %v", err)
	}

	const tg = int64(555001)
	got, err := a.CabinetBindTelegram(ctx, id, tg)
	if err != nil || got != tg {
		t.Fatalf("привязка: got=%d err=%v", got, err)
	}
	if u, _ := fs.GetUser(ctx, id); u != nil {
		t.Fatal("прежний аккаунт обязан исчезнуть — иначе человек раздвоился")
	}
	u, _ := fs.GetUser(ctx, tg)
	if u == nil || u.Balance != 12345 || u.SubExpireAt == "" {
		t.Fatalf("баланс и подписка не переехали: %+v", u)
	}
	wu, _ := fs.GetWebUserByTgID(ctx, tg)
	if wu == nil || wu.Email != "bind@example.com" {
		t.Fatal("вход по паролю обязан продолжать работать после привязки")
	}
	// Почта осталась при аккаунте, поэтому платные действия ей больше не
	// закрываются: личность подтверждена подписью Telegram.
	if a.CabinetMoneyBlocked(ctx, tg) {
		t.Fatal("привязавшему Telegram гейт подтверждения не адресован")
	}
}

// Занятый Telegram — отказ. Слить две истории покупок автоматически нельзя, не
// решая за человека судьбу оплаченного.
func TestBindTelegramRefusesBusyAccount(t *testing.T) {
	a, fs := cabinetApp(t)
	ctx := context.Background()
	id, err := a.CabinetEmailRegister(ctx, "busy@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	const tg = int64(555002)
	if err := fs.UpsertUser(ctx, tg); err != nil {
		t.Fatalf("аккаунт в боте: %v", err)
	}
	if err := fs.SetSubExpiry(ctx, tg, "2030-01-01T00:00:00Z", "paid"); err != nil {
		t.Fatalf("подписка: %v", err)
	}
	if _, err := a.CabinetBindTelegram(ctx, id, tg); err == nil {
		t.Fatal("к Telegram с подпиской привязываться нельзя")
	}
	if u, _ := fs.GetUser(ctx, id); u == nil {
		t.Fatal("отказ не имеет права трогать аккаунт кабинета")
	}
}

// Пустая строка от одного /start аккаунтом не считается: переносить нечего, а
// отказ здесь запирал бы почти всех — нажать «Старт» успевают почти все.
func TestBindTelegramAllowsBareStartRow(t *testing.T) {
	a, fs := cabinetApp(t)
	ctx := context.Background()
	id, err := a.CabinetEmailRegister(ctx, "bare@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	const tg = int64(555003)
	if err := fs.UpsertUser(ctx, tg); err != nil {
		t.Fatalf("аккаунт в боте: %v", err)
	}
	if _, err := a.CabinetBindTelegram(ctx, id, tg); err != nil {
		t.Fatalf("пустая строка не должна мешать привязке: %v", err)
	}
}

// Закрытый бот: привязка к Telegram, которого бот не пускает, заперла бы
// человека снаружи вместе с его оплаченной подпиской.
func TestBindTelegramRefusesWhenBotClosed(t *testing.T) {
	a, _ := cabinetApp(t)
	ctx := context.Background()
	id, err := a.CabinetEmailRegister(ctx, "closed@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	a.botCfg.WhitelistMode = true
	a.botCfg.AccessMode = model.AccessWhitelist
	a.botCfg.NormalizeAccess()
	if _, err := a.CabinetBindTelegram(ctx, id, 555004); err == nil {
		t.Fatal("в закрытом боте привязка к непущенному Telegram обязана быть отклонена")
	}
}

// Привязка — операция аккаунта, заведённого по почте. Телеграм-аккаунту
// привязывать нечего, и повторная привязка тоже недопустима.
func TestBindTelegramOnlyForEmailAccounts(t *testing.T) {
	a, _ := cabinetApp(t)
	ctx := context.Background()
	if _, err := a.CabinetBindTelegram(ctx, 555005, 555006); err == nil {
		t.Fatal("телеграм-аккаунт привязывать нечем")
	}
}

// Поколение пропусков после переезда обязано вырасти: выданные до привязки
// пропуска относились к аккаунту, которого больше нет.
func TestBindTelegramBumpsEpoch(t *testing.T) {
	a, _ := cabinetApp(t)
	ctx := context.Background()
	id, err := a.CabinetEmailRegister(ctx, "epoch@example.com", "password1")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	const tg = int64(555007)
	before := a.SessionEpoch(ctx, tg)
	if _, err := a.CabinetBindTelegram(ctx, id, tg); err != nil {
		t.Fatalf("привязка: %v", err)
	}
	if after := a.SessionEpoch(ctx, tg); after == before {
		t.Fatal("после привязки поколение пропусков обязано вырасти")
	}
}

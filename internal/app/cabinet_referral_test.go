package app

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// cabinetRefApp — кабинет и рефералка в режиме «бонус за регистрацию»:
// 50 ₽ пригласившему и 30 ₽ приглашённому.
func cabinetRefApp(t *testing.T) (*App, *fakeStore) {
	t.Helper()
	a, fs := cabinetApp(t)
	a.botCfg.Referral = model.ReferralConfig{
		Enabled: true, Init: true, OnFirstPay: false,
		BonusKind: model.ReferralBonusBalance, BonusValue: 50,
		InviteeKind: model.ReferralBonusBalance, InviteeValue: 30,
	}
	return a, fs
}

// Регистрация по почте по ссылке на кабинет привязывает к пригласившему, но
// разовые бонусы ждут первой оплаты: адреса заводятся бесплатно.
func TestCabinetRegisterReferral_BonusAfterPay(t *testing.T) {
	a, fs := cabinetRefApp(t)
	ctx := context.Background()
	const inviter int64 = 1000000060
	_ = fs.UpsertUser(ctx, inviter)

	id, err := a.CabinetEmailRegister(ctx, "friend@example.com", "password1", "ref_1000000060")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()
	u, _ := fs.GetUser(ctx, id)
	if u == nil || u.ReferredBy != inviter {
		t.Fatalf("не привязан к пригласившему: %+v", u)
	}
	if r, _ := fs.GetUser(ctx, inviter); r.Balance != 0 {
		t.Fatalf("бонус за регистрацию по почте выдан сразу: %d", r.Balance)
	}
	if u.Balance != 0 || u.RefBonusPaid {
		t.Fatalf("приглашённому выдано сразу: %+v", u)
	}

	a.grantReferralBonus(ctx, id)
	if r, _ := fs.GetUser(ctx, inviter); r.Balance != 5000 {
		t.Fatalf("после оплаты бонус пригласившему: %d", r.Balance)
	}
	if u, _ := fs.GetUser(ctx, id); u.Balance != 3000 || !u.RefBonusPaid {
		t.Fatalf("после оплаты бонус приглашённому: %+v", u)
	}
	a.grantReferralBonus(ctx, id)
	if r, _ := fs.GetUser(ctx, inviter); r.Balance != 5000 {
		t.Fatalf("двойной бонус: %d", r.Balance)
	}
}

// Пришедший в чат по-прежнему получает бонус в режиме «за регистрацию» сразу,
// а оплата его не повторяет.
func TestReferral_TelegramStillPaidOnBind(t *testing.T) {
	a, fs := cabinetRefApp(t)
	ctx := context.Background()
	const inviter, friend int64 = 1000000061, 1000000062
	_ = fs.UpsertUser(ctx, inviter)
	a.bindReferrer(ctx, friend, "ref_1000000061", false)
	if r, _ := fs.GetUser(ctx, inviter); r.Balance != 5000 {
		t.Fatalf("бонус при привязке: %d", r.Balance)
	}
	a.grantReferralBonus(ctx, friend)
	if r, _ := fs.GetUser(ctx, inviter); r.Balance != 5000 {
		t.Fatalf("оплата повторила бонус: %d", r.Balance)
	}
}

// Вход в существующий аккаунт по ссылке ничего не привязывает.
func TestCabinetRegisterReferral_ExistingNotBound(t *testing.T) {
	a, fs := cabinetRefApp(t)
	ctx := context.Background()
	const inviter int64 = 1000000063
	_ = fs.UpsertUser(ctx, inviter)
	id, err := a.CabinetEmailRegister(ctx, "old@example.com", "password1", "")
	if err != nil {
		t.Fatal(err)
	}
	a.waitMail()
	if _, err := a.CabinetEmailRegister(ctx, "old@example.com", "password1", "ref_1000000063"); err != nil {
		t.Fatalf("повторный вход: %v", err)
	}
	if u, _ := fs.GetUser(ctx, id); u == nil || u.ReferredBy != 0 {
		t.Fatalf("существующий аккаунт привязан: %+v", u)
	}
}

// Ссылка, разосланная аккаунтом по почте до привязки Telegram, ведёт к нему
// и после: номер сменился, прежний запомнен.
func TestReferral_OldLinkAfterTelegramBind(t *testing.T) {
	a, fs := cabinetRefApp(t)
	a.botCfg.Referral.OnFirstPay = true
	ctx := context.Background()
	old, err := a.CabinetEmailRegister(ctx, "inviter@example.com", "password1", "")
	if err != nil {
		t.Fatal(err)
	}
	a.waitMail()
	const tg, friend int64 = 1000000064, 1000000065
	if got, err := a.CabinetBindTelegram(ctx, old, tg); err != nil || got != tg {
		t.Fatalf("привязка: got=%d err=%v", got, err)
	}
	a.bindReferrer(ctx, friend, "ref_"+strconv.FormatInt(old, 10), false)
	if u, _ := fs.GetUser(ctx, friend); u == nil || u.ReferredBy != tg {
		t.Fatalf("старая ссылка не привела к пригласившему: %+v", u)
	}
}

// Ссылка на кабинет: есть, когда кабинет включён; у аккаунта без Telegram —
// первой.
func TestMiniReferral_CabinetLink(t *testing.T) {
	a, fs := cabinetRefApp(t)
	ctx := context.Background()
	const tg int64 = 1000000066
	_ = fs.UpsertUser(ctx, tg)
	r := a.MiniReferral(ctx, tg)
	if r.CabinetLink != "https://example.com/cabinet/?ref=1000000066" || r.CabinetFirst {
		t.Fatalf("ссылка на кабинет: %+v", r)
	}
	if r := a.MiniReferral(ctx, -5); !r.CabinetFirst || !strings.HasSuffix(r.CabinetLink, "?ref=-5") {
		t.Fatalf("аккаунт без Telegram: %+v", r)
	}
	a.botCfg.Cabinet.Enabled = false
	if r := a.MiniReferral(ctx, tg); r.CabinetLink != "" {
		t.Fatalf("кабинет выключен, а ссылка есть: %+v", r)
	}
}

// Приглашённый, привязанный, пока бонус шёл за оплату, после переключения
// на «за регистрацию» оплатой бонус не получает: так было и раньше, а
// отложенные бонусы помечены при привязке явно.
func TestReferral_ModeSwitchNoLateBonus(t *testing.T) {
	a, fs := cabinetRefApp(t)
	ctx := context.Background()
	a.botCfg.Referral.OnFirstPay = true
	const inviter, friend int64 = 1000000067, 1000000068
	_ = fs.UpsertUser(ctx, inviter)
	a.bindReferrer(ctx, friend, "ref_1000000067", false)
	a.botCfg.Referral.OnFirstPay = false
	a.grantReferralBonus(ctx, friend)
	if r, _ := fs.GetUser(ctx, inviter); r.Balance != 0 {
		t.Fatalf("поздний бонус после смены режима: %d", r.Balance)
	}
}

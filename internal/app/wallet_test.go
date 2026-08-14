package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

func walletApp(t *testing.T, topUp bool) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	a, msg, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.Wallet = model.WalletConfig{TopUp: topUp, Init: true}
	return a, msg, fs
}

// Обновление с версии без настройки не должно молча замораживать балансы:
// пополнение включается само.
func TestWallet_DefaultsToEnabled(t *testing.T) {
	cfg := &model.BotConfig{}
	cfg.NormalizeWallet()
	if !cfg.Wallet.TopUp {
		t.Fatal("после обновления пополнение должно остаться включённым")
	}
	a := &App{botCfg: cfg}
	if !a.topUpEnabled() {
		t.Fatal("topUpEnabled должен быть true по умолчанию")
	}
}

// Выключенное пополнение убирает кнопки и запрещает само действие — кнопка из
// старого сообщения не должна открывать экран пополнения.
func TestWallet_TopUpOffHidesAndBlocks(t *testing.T) {
	a, msg, _ := walletApp(t, false)
	ctx := context.Background()

	a.handleCallback(ctx, cb(700, "menu:balance"))
	for _, d := range msg.cbData {
		if d == "menu:topup" {
			t.Fatalf("кнопка пополнения показана при выключенном пополнении: %v", msg.cbData)
		}
	}
	var warned bool
	for _, txt := range msg.texts {
		if strings.Contains(txt, "Вывод средств невозможен") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("нет предупреждения про вывод: %v", msg.texts)
	}

	msg.cbData = nil
	a.handleCallback(ctx, cb(700, "menu:topup"))
	for _, d := range msg.cbData {
		if strings.HasPrefix(d, "top:") {
			t.Fatalf("экран пополнения открылся при выключенном пополнении: %v", msg.cbData)
		}
	}
}

// Ядро создания счёта — последний рубеж: оно общее для чата, мини-аппа и ЛК.
func TestWallet_TopUpCreateRefused(t *testing.T) {
	a, _, _ := walletApp(t, false)
	if _, _, err := a.topUpCreate(context.Background(), 700, 10000, "yk"); err == nil {
		t.Fatal("создание счёта на пополнение должно отказывать")
	}
	if dto := a.MiniTopUp(context.Background(), 700, 10000, "yk"); dto.Error == "" {
		t.Fatal("мини-апп должен отказывать в пополнении")
	}
	if dto := a.MiniTopUpOptions(context.Background(), 700); len(dto.Amounts) != 0 || len(dto.Methods) != 0 {
		t.Fatalf("мини-апп не должен предлагать пополнение: %+v", dto)
	}
}

// Оплата с баланса остаётся: реферальные начисления и промокоды приходят на
// баланс и при выключенном пополнении.
func TestWallet_BalancePaymentStillWorks(t *testing.T) {
	a, msg, fs := walletApp(t, false)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 701)
	if err := fs.AddBalance(ctx, 701, 100000); err != nil {
		t.Fatal(err)
	}
	a.handleCallback(ctx, cb(701, "menu:balance"))
	var hasBuy bool
	for _, d := range msg.cbData {
		if d == "menu:buy" {
			hasBuy = true
		}
	}
	if !hasBuy {
		t.Fatalf("кошелёк должен вести к покупке: %v", msg.cbData)
	}
	if bal := a.userBalance(ctx, 701); bal != 100000 {
		t.Fatalf("баланс = %d", bal)
	}
}

// Админ переключает пополнение кнопкой.
func TestWallet_AdminToggle(t *testing.T) {
	a, _, _ := walletApp(t, true)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "wal:topup"))
	if a.topUpEnabled() {
		t.Fatal("пополнение должно выключиться")
	}
	a.handleCallback(ctx, cb(100, "wal:topup"))
	if !a.topUpEnabled() {
		t.Fatal("пополнение должно включиться обратно")
	}
	// Обычный пользователь настройку не трогает.
	a.handleCallback(ctx, cb(701, "wal:topup"))
	if !a.topUpEnabled() {
		t.Fatal("не-админ не должен менять настройку кошелька")
	}
}

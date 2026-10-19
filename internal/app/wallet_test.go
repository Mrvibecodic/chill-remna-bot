package app

import (
	"context"
	"encoding/json"
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
	if _, _, err := a.topUpCreate(context.Background(), 700, 10000, "yk", false); err == nil {
		t.Fatal("создание счёта на пополнение должно отказывать")
	}
	if dto := a.MiniTopUp(context.Background(), 700, 10000, "yk", false); dto.Error == "" {
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

// Экран кошелька должен показывать РЕАЛЬНУЮ сумму на балансах: ради этого
// числа он и нужен — выключать пополнение вслепую нельзя.
func TestWallet_HeldAmount(t *testing.T) {
	a, _, fs := walletApp(t, true)
	ctx := context.Background()
	for _, id := range []int64{801, 802} {
		_ = fs.UpsertUser(ctx, id)
		if err := fs.AddBalance(ctx, id, 50000); err != nil {
			t.Fatal(err)
		}
	}
	_ = fs.UpsertUser(ctx, 803)

	sum, n := a.walletHeld(ctx)
	if sum != 100000 || n != 2 {
		t.Fatalf("на балансах = %d ₽ у %d чел., ожидалось 1000 у 2", sum/100, n)
	}
}

// Блок «На сколько хватит» админ прячет одной кнопкой — и в чате, и в
// мини-аппе. По умолчанию (и в конфиге без настройки) блок показан.
func TestWallet_ForecastToggle(t *testing.T) {
	a, fs := planApp(t)
	msg := a.msg.(*fakeMsg)
	ctx := context.Background()
	const uid int64 = 1000000001
	_ = fs.UpsertUser(ctx, uid)
	if err := fs.AddBalance(ctx, uid, 99000); err != nil {
		t.Fatal(err)
	}
	balanceText := func() string {
		msg.texts = nil
		a.handleCallback(ctx, cb(uid, "menu:balance"))
		return strings.Join(msg.texts, "\n")
	}

	if txt := balanceText(); !strings.Contains(txt, "На сколько хватит") {
		t.Fatalf("по умолчанию блок должен быть показан:\n%s", txt)
	}
	if w := a.MiniWallet(ctx, uid); len(w.Rows) == 0 || w.MaxMonths == 0 {
		t.Fatalf("мини-апп по умолчанию должен показывать прогноз: %+v", w)
	}

	// Не-админ настройку не трогает.
	a.handleCallback(ctx, cb(uid, "wal:forecast"))
	if !a.forecastShown() {
		t.Fatal("не-админ не должен менять настройку кошелька")
	}

	a.handleCallback(ctx, cb(100, "wal:forecast"))
	if a.forecastShown() {
		t.Fatal("блок должен скрыться")
	}
	if !strings.Contains(strings.Join(msg.texts, "\n"), "«На сколько хватит»: <b>скрыт</b>") {
		t.Fatalf("экран кошелька в админке не показывает состояние:\n%v", msg.texts)
	}
	txt := balanceText()
	if strings.Contains(txt, "На сколько хватит") || strings.Contains(txt, "<pre>") {
		t.Fatalf("скрытый блок виден в чате:\n%s", txt)
	}
	if !strings.Contains(txt, "Вывод средств невозможен") {
		t.Fatalf("остальной кошелёк пропал:\n%s", txt)
	}
	if w := a.MiniWallet(ctx, uid); len(w.Rows) != 0 || w.MaxMonths != 0 || w.PlanName != "" || w.BalanceK != 99000 {
		t.Fatalf("скрытый блок виден в мини-аппе: %+v", w)
	}

	a.handleCallback(ctx, cb(100, "wal:forecast"))
	if !a.forecastShown() || !strings.Contains(balanceText(), "На сколько хватит") {
		t.Fatal("блок должен вернуться")
	}

	// Конфиг с версии без настройки: блок показан.
	var cfg model.BotConfig
	if err := json.Unmarshal([]byte(`{"wallet":{"topup":true,"init":true}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Wallet.HideForecast {
		t.Fatal("старый конфиг не должен прятать блок")
	}
}

package app

import (
	"context"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

// showWalletAdmin — экран кошелька: единственная настройка здесь это
// пополнение. Оплату с баланса не выключаем: на баланс приходят реферальные
// начисления и промокоды, и человеку нужно чем-то их потратить.
func (a *App) showWalletAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	on := a.topUpEnabled()
	label := i18n.T(lang, "wallet.btn_topup_off")
	if !on {
		label = i18n.T(lang, "wallet.btn_topup_on")
	}
	state := i18n.T(lang, "wallet.state_off")
	if on {
		state = i18n.T(lang, "wallet.state_on")
	}
	// Сколько денег сейчас лежит у людей: выключать пополнение вслепую нельзя.
	held, holders := a.walletHeld(ctx)
	text := i18n.T(lang, "wallet.title", state, kopecksToRub(held), holders)
	a.sendPayKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(label, "wal:topup")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// walletHeld — сколько всего лежит на балансах и у скольких человек.
func (a *App) walletHeld(ctx context.Context) (int64, int) {
	if a.store == nil {
		return 0, 0
	}
	users, err := a.store.UsersForNotify(ctx)
	if err != nil {
		return 0, 0
	}
	var sum int64
	n := 0
	for _, u := range users {
		if u.Balance > 0 {
			sum += u.Balance
			n++
		}
	}
	return sum, n
}

// onWalletAdmin обрабатывает кнопки экрана кошелька (только админ).
func (a *App) onWalletAdmin(ctx context.Context, chatID int64, val string) {
	if val == "topup" {
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.NormalizeWallet()
			a.botCfg.Wallet.TopUp = !a.botCfg.Wallet.TopUp
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
	}
	a.showWalletAdmin(ctx, chatID)
}

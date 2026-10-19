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
	fcState, fcLabel := i18n.T(lang, "wallet.state_shown"), i18n.T(lang, "wallet.btn_fc_hide")
	if !a.forecastShown() {
		fcState, fcLabel = i18n.T(lang, "wallet.state_hidden"), i18n.T(lang, "wallet.btn_fc_show")
	}
	// Сколько денег сейчас лежит у людей: выключать пополнение вслепую нельзя.
	held, holders := a.walletHeld(ctx)
	text := i18n.T(lang, "wallet.title", state, fcState, kopecksToRub(held), holders)
	a.sendPayKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(label, "wal:topup")},
		{btn(fcLabel, "wal:forecast")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// walletHeld — сколько всего лежит на балансах и у скольких человек.
func (a *App) walletHeld(ctx context.Context) (int64, int) {
	if a.store == nil {
		return 0, 0
	}
	sum, n, err := a.store.BalanceHeld(ctx)
	if err != nil {
		a.log.Warn("кошелёк: подсчёт балансов", "err", err)
		return 0, 0
	}
	return sum, n
}

// onWalletAdmin обрабатывает кнопки экрана кошелька (только админ).
func (a *App) onWalletAdmin(ctx context.Context, chatID int64, val string) {
	switch val {
	case "topup", "forecast":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.NormalizeWallet()
			if val == "topup" {
				a.botCfg.Wallet.TopUp = !a.botCfg.Wallet.TopUp
			} else {
				a.botCfg.Wallet.HideForecast = !a.botCfg.Wallet.HideForecast
			}
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
	}
	a.showWalletAdmin(ctx, chatID)
}

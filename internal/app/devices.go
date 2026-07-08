package app

import (
	"context"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
)

// onDevices handles the user-facing "reset all devices" flow reachable from the
// "My subscription" screen. Confirming rotates the user's proxy credentials on
// the panel, which disconnects every currently connected device. The user then
// has to refresh their subscription on the devices they want to keep, otherwise
// nothing will connect.
func (a *App) onDevices(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	switch val {
	case "reset":
		a.sendKBSection(ctx, chatID, assets.SectionMySubscription, i18n.T(lang, "dev.reset_confirm"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "dev.btn_reset_yes"), "dev:confirm")},
			navBack(lang, "menu:mysubs"),
		})
	case "confirm":
		a.mu.Lock()
		panel := a.panel
		a.mu.Unlock()
		if panel == nil {
			a.notifyKB(ctx, chatID, i18n.T(lang, "dev.fail"), [][]models.InlineKeyboardButton{navBack(lang, "menu:mysubs")})
			return
		}
		ok, err := panel.RevokeDevicesByTelegramID(ctx, chatID)
		if err != nil || !ok {
			a.notifyKB(ctx, chatID, i18n.T(lang, "dev.fail"), [][]models.InlineKeyboardButton{navBack(lang, "menu:mysubs")})
			return
		}
		a.sendKBSection(ctx, chatID, assets.SectionMySubscription, i18n.T(lang, "dev.done"), [][]models.InlineKeyboardButton{
			navBack(lang, "menu:mysubs"),
		})
	}
}

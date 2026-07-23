package app

import (
	"context"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/web"
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
		res, found, err := panel.ResetDevicesByTelegramID(ctx, chatID)
		if err != nil || !found {
			a.notifyKB(ctx, chatID, i18n.T(lang, "dev.fail"), [][]models.InlineKeyboardButton{navBack(lang, "menu:mysubs")})
			return
		}
		if res.HwidErr != nil {
			a.log.Warn("reset devices: HWID delete-all failed; keys rotated only", "tg", chatID, "err", res.HwidErr)
		}
		a.sendKBSection(ctx, chatID, assets.SectionMySubscription, i18n.T(lang, "dev.done"), [][]models.InlineKeyboardButton{
			navBack(lang, "menu:mysubs"),
		})
	}
}

// MiniResetDevices resets the caller's own devices from the Mini App or the web
// cabinet: rotates credentials + clears all HWID registrations (same panel core
// as the chat flow). The caller is always the authenticated user, so there is no
// cross-user access. A delete-all failure is logged but still reported as success
// because the credential rotation already applied.
func (a *App) MiniResetDevices(ctx context.Context, tgID int64) web.MiniActionDTO {
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if panel == nil {
		return web.MiniActionDTO{Error: "панель недоступна"}
	}
	res, found, err := panel.ResetDevicesByTelegramID(ctx, tgID)
	if err != nil {
		a.log.Warn("miniapp reset devices failed", "tg", tgID, "err", err)
		return web.MiniActionDTO{Error: "не удалось сбросить устройства"}
	}
	if !found {
		return web.MiniActionDTO{Error: "подписка не найдена"}
	}
	if res.HwidErr != nil {
		a.log.Warn("miniapp reset devices: HWID delete-all failed; keys rotated only", "tg", tgID, "err", res.HwidErr)
	}
	a.invalidateSubCache(tgID)
	return web.MiniActionDTO{OK: true}
}

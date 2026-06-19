package app

import (
	"context"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
)

func (a *App) showBroadcast(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	count := 0
	if a.store != nil {
		if ids, err := a.store.AllUserIDs(ctx); err == nil {
			count = len(ids)
		}
	}
	a.sendKBSection(ctx, chatID, assets.SectionPromoCode, i18n.T(lang, "bcast.title", count), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "bcast.btn_new"), "bc:new")},
		navBack(lang, "menu:marketing"),
	})
}

func (a *App) onBroadcast(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	switch val {
	case "new":
		a.getUI(chatID).adminInput = "bcast"
		a.askInput(ctx, chatID, i18n.T(lang, "bcast.ask"), "menu:broadcast")
	case "send":
		ui := a.getUI(chatID)
		text := ui.broadcastText
		ui.broadcastText = ""
		if text == "" {
			a.showBroadcast(ctx, chatID)
			return
		}
		a.runBroadcast(chatID, text)
		a.sendKB(ctx, chatID, i18n.T(lang, "bcast.started"), [][]models.InlineKeyboardButton{navBack(lang, "menu:marketing")})
	}
}

func (a *App) previewBroadcast(ctx context.Context, chatID int64, text string) {
	lang := a.lang(chatID)
	a.getUI(chatID).broadcastText = text
	count := 0
	if a.store != nil {
		if ids, err := a.store.AllUserIDs(ctx); err == nil {
			count = len(ids)
		}
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "bcast.preview", count)+"\n\n"+text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "bcast.btn_send", count), "bc:send")},
		navBack(lang, "menu:broadcast"),
	})
}

func (a *App) runBroadcast(adminChat int64, text string) {
	if a.store == nil {
		return
	}
	lang := a.lang(adminChat)
	ctx := a.bgContext()
	go func() {
		ids, err := a.store.AllUserIDs(ctx)
		if err != nil {
			a.sendHome(ctx, adminChat, i18n.T(lang, "bcast.failed"))
			return
		}
		// Pace sends to stay well under Telegram's global rate limit and stop
		// promptly on shutdown. Per-message 429s are retried inside sendWithRetry.
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		var sent, failed int
		for _, id := range ids {
			select {
			case <-ctx.Done():
				a.log.Info("broadcast cancelled", "sent", sent, "failed", failed, "total", len(ids))
				return
			case <-ticker.C:
			}
			if a.msg.Send(ctx, id, a.applyPremium(text)) != 0 {
				sent++
			} else {
				failed++
			}
		}
		id := a.msg.Send(ctx, adminChat, a.applyPremium(i18n.T(lang, "bcast.done", sent, failed)))
		if id != 0 {
			time.AfterFunc(60*time.Second, func() {
				a.msg.Delete(a.bgContext(), adminChat, id)
			})
		}
	}()
}

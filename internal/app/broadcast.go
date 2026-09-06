package app

import (
	"context"
	"errors"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
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
		if !a.bcastRunning.CompareAndSwap(false, true) {
			a.sendHome(ctx, chatID, i18n.T(lang, "bcast.already_running"))
			return
		}
		a.bcastStop.Store(false)
		a.sendKB(ctx, chatID, i18n.T(lang, "bcast.started"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "bcast.btn_stop"), "bc:stop")},
			navBack(lang, "menu:marketing"),
		})
		a.runBroadcast(chatID, text, a.screenMsgID(chatID))
	case "stop":
		// Флаг на всём боте, а не на экране: рассылка одна, а админов может
		// быть несколько — остановить обязан любой. Раньше остановить её было
		// нельзя вообще, только погасив контейнер.
		if !a.bcastRunning.Load() {
			a.sendHome(ctx, chatID, i18n.T(lang, "bcast.not_running"))
			return
		}
		a.bcastStop.Store(true)
		a.sendHome(ctx, chatID, i18n.T(lang, "bcast.stopping"))
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
	a.sendMktKB(ctx, chatID, i18n.T(lang, "bcast.preview", count)+"\n\n"+text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "bcast.btn_send", count), "bc:send")},
		navBack(lang, "menu:broadcast"),
	})
}

// expandBroadcastVars substitutes per-recipient placeholders in a broadcast
// template: {name}, {username}, {id}, {balance}, {expire}. Every substituted
// value is HTML-escaped so a user's name or username can't break the Telegram
// HTML markup of the message. Empty fields fall back to a neutral word.
func (a *App) expandBroadcastVars(text string, u *model.User, lang string) string {
	if u == nil || !strings.Contains(text, "{") {
		return text
	}
	fallback := i18n.T(lang, "bcast.var_fallback")

	name := strings.TrimSpace(u.FirstName)
	if name == "" {
		name = strings.TrimSpace(u.Username)
	}
	if name == "" {
		name = fallback
	}

	username := strings.TrimSpace(u.Username)
	if username != "" {
		username = "@" + username
	} else {
		username = fallback
	}

	return strings.NewReplacer(
		"{name}", escapeName(name),
		"{username}", escapeName(username),
		"{id}", strconv.FormatInt(u.TelegramID, 10),
		"{balance}", html.EscapeString(kopecksToRub(u.Balance)),
		"{expire}", html.EscapeString(formatExpire(u.SubExpireAt, lang)),
	).Replace(text)
}

func (a *App) runBroadcast(adminChat int64, text string, statusID int) {
	if a.store == nil {
		a.bcastRunning.Store(false)
		return
	}
	lang := a.lang(adminChat)
	ctx := a.bgContext()
	go func() {
		defer a.bcastRunning.Store(false)
		ids, err := a.store.AllUserIDs(ctx)
		if err != nil {
			a.sendHome(ctx, adminChat, i18n.T(lang, "bcast.failed"))
			return
		}
		// Pace sends to stay well under Telegram's global rate limit and stop
		// promptly on shutdown. Per-message 429s are retried inside sendWithRetry.
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		var sent, failed, gone int
		stopped := false
		for _, id := range ids {
			select {
			case <-ctx.Done():
				a.log.Info("broadcast cancelled", "sent", sent, "failed", failed, "total", len(ids))
				return
			case <-ticker.C:
			}
			if a.bcastStop.Load() {
				stopped = true
				break
			}
			out := text
			if u, err := a.store.GetUser(ctx, id); err == nil && u != nil {
				out = a.expandBroadcastVars(text, u, a.lang(id))
			}
			mid, serr := a.msg.SendErr(ctx, id, a.applyPremium(out))
			switch {
			case mid != 0:
				sent++
			case errors.Is(serr, bot.ErrorForbidden):
				// Человек заблокировал бота или удалил аккаунт. Помечаем, чтобы
				// следующая рассылка на него не тратилась. Это НЕ бан: доступ
				// к боту не закрывается, метка снимается его же сообщением.
				gone++
				if err := a.store.SetUnreachable(ctx, id, time.Now().UTC().Format(time.RFC3339)); err != nil {
					a.log.Warn("метка недоступного чата не сохранена", "err", err)
				}
			default:
				failed++
			}
		}
		key := "bcast.done"
		if stopped {
			key = "bcast.stopped"
		}
		doneText := a.applyPremium(i18n.T(lang, key, sent, failed, gone))
		doneRows := [][]models.InlineKeyboardButton{navBack(lang, "menu:marketing")}
		if statusID == 0 || !a.msg.EditText(ctx, adminChat, statusID, doneText, doneRows) {
			a.msg.SendKB(ctx, adminChat, doneText, doneRows)
		}
	}()
}

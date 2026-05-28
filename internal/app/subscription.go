package app

import (
	"context"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

// subActiveText — единое сообщение «подписка активна»: имя пользователя, срок
// действия, КОПИРУЕМАЯ ссылка (в <code> — удобно скопировать тапом) и та же
// ссылка отдельно для открытия в браузере (Telegram сам сделает её кликабельной).
func (a *App) subActiveText(ctx context.Context, chatID int64, link, expireAt string) string {
	lang := a.lang(chatID)
	return i18n.T(lang, "sub.active", a.displayNameByID(ctx, chatID), formatExpire(expireAt, lang), link, link)
}

// sendSubActive шлёт постоянное сообщение «подписка активна» с кнопкой поддержки
// (если задана) и «На главную». Единая точка для всех путей выдачи подписки.
func (a *App) sendSubActive(ctx context.Context, chatID int64, link, expireAt string) {
	var rows [][]models.InlineKeyboardButton
	if sup := a.supportURL(); sup != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(a.lang(chatID), "btn.support"), URL: sup}})
	}
	a.notifyKB(ctx, chatID, a.subActiveText(ctx, chatID, link, expireAt), rows)
}

// displayNameByID — имя пользователя из хранилища (имя/ник) для приветствия.
func (a *App) displayNameByID(ctx context.Context, id int64) string {
	if a.store != nil {
		if u, _ := a.store.GetUser(ctx, id); u != nil {
			return displayName(u.FirstName, u.Username)
		}
	}
	return displayName("", "")
}

func (a *App) supportURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg != nil {
		return a.botCfg.Contact.SupportURL
	}
	return ""
}

// formatExpire переводит RFC3339 из панели в «ДД.ММ.ГГГГ ЧЧ:ММ» (UTC).
func formatExpire(raw, lang string) string {
	if raw == "" {
		return i18n.T(lang, "sub.no_expire")
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		// Москва — UTC+3 круглый год (без перехода на летнее время).
		msk := t.UTC().Add(3 * time.Hour)
		return msk.Format("02.01.2006 15:04") + " " + i18n.T(lang, "sub.tz_msk")
	}
	return raw
}

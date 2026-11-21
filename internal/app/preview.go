package app

import (
	"context"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

func (a *App) inPreview(chatID int64) bool {
	return a.cfg != nil && chatID == a.cfg.AdminID && a.preview.Load()
}

// previewFlow — нажатие админа в превью: экраны клиента остаются в превью,
// любая кнопка админки выходит из него.
func (a *App) previewFlow(ctx context.Context, chatID int64, data string) context.Context {
	if a.cfg == nil || chatID != a.cfg.AdminID {
		return ctx
	}
	if data == "menu:preview" {
		a.preview.Store(true)
	}
	if !a.preview.Load() {
		return ctx
	}
	switch data {
	case "menu:preview", "menu:home", "menu:trial", cbClose + ":home":
		return withClientFlow(ctx)
	case cbInput + ":cancel":
		if a.getUI(chatID).inputBack == "" {
			return withClientFlow(ctx)
		}
	case cbClose + ":close":
		return ctx
	}
	if flowOf(ctx) == "client" {
		return ctx
	}
	a.preview.Store(false)
	return ctx
}

func (a *App) previewMenu(lang, caption string, ents []models.MessageEntity, rows [][]models.InlineKeyboardButton) (string, []models.MessageEntity, [][]models.InlineKeyboardButton) {
	note := i18n.T(lang, "menu.preview_note") + "\n\n"
	shift := len(utf16.Encode([]rune(note)))
	for i := range ents {
		ents[i].Offset += shift
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.preview_exit"), "menu:manage")})
	return note + caption, ents, rows
}

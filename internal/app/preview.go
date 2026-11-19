package app

import (
	"context"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

func (a *App) inPreview(chatID int64) bool {
	return a.cfg != nil && chatID == a.cfg.AdminID && a.getUI(chatID).preview
}

// previewFlow — нажатие админа в превью: экраны клиента остаются в превью,
// любая кнопка админки выходит из него.
func (a *App) previewFlow(ctx context.Context, chatID int64, data string) context.Context {
	ui := a.getUI(chatID)
	if data == "menu:preview" {
		ui.preview = true
	}
	if !ui.preview {
		return ctx
	}
	switch data {
	case "menu:preview", "menu:home", "menu:trial", cbClose + ":home":
		return withClientFlow(ctx)
	case cbInput + ":cancel":
		if ui.inputBack == "" {
			return withClientFlow(ctx)
		}
	case cbClose + ":close":
		return ctx
	}
	if flowOf(ctx) == "client" {
		return ctx
	}
	ui.preview = false
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

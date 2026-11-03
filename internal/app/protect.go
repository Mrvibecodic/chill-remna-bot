package app

import (
	"context"
	"regexp"

	"github.com/go-telegram/bot/models"
)

// Экраны пользователя уходят с запретом пересылки (protect_content), если в
// них нечего копировать. Запрет снимает и копирование текста, поэтому
// открытыми остаются экраны со ссылками, реквизитами, кодом обращения и
// любым текстом, похожим на то, что нужно скопировать.

// copyableTagRe — разметка, которую ставят ради копирования или перехода.
var copyableTagRe = regexp.MustCompile(`(?i)<code|<pre|<a\s`)

// copyableRe — видимый текст, который человек может захотеть скопировать:
// ссылки, адреса, @ники, номера карт и телефонов (в том числе с пробелами),
// длинные ключи.
var copyableRe = regexp.MustCompile(`(?i)https?://|tg://|t\.me/|@[a-z0-9_]{3,}|\d{6,}|\+?\d[\d \-()]{8,}\d|[a-z0-9-]+\.[a-z]{2,}\b|[a-z0-9_-]{20,}`)

// copyableText — есть ли в экране (HTML) что копировать. Теги разбираются
// отдельно: в премиум-эмодзи длинный числовой id, он не текст.
func copyableText(s string) bool {
	return copyableTagRe.MatchString(s) || copyableRe.MatchString(stripHTMLTags(s))
}

// copyableEnts — то же для текста с entities (приветствие, набранное админом
// в Telegram).
func copyableEnts(text string, ents []models.MessageEntity) bool {
	for _, e := range ents {
		switch e.Type {
		case models.MessageEntityTypeCode, models.MessageEntityTypePre, models.MessageEntityTypeURL,
			models.MessageEntityTypeTextLink, models.MessageEntityTypeMention, models.MessageEntityTypeEmail,
			models.MessageEntityTypePhoneNumber:
			return true
		}
	}
	return copyableText(text)
}

// screenProtect — закрыть ли экран от пересылки. Только чаты пользователей:
// у экранов админки свой набор того, что нужно копировать.
func (a *App) screenProtect(ctx context.Context, chatID int64, copyable bool) bool {
	if copyable || ctx.Value(keepCopyKey{}) != nil {
		return false
	}
	return hasChat(chatID) && a.cfg != nil && chatID != a.cfg.AdminID
}

type (
	protectKey  struct{}
	keepCopyKey struct{}
)

// withKeepCopy — экран в этом контексте не закрывать, даже если детектор не
// нашёл в нём ничего для копирования (текст документов).
func withKeepCopy(ctx context.Context) context.Context {
	return context.WithValue(ctx, keepCopyKey{}, true)
}

// withProtect — отправка в этом контексте уходит с запретом пересылки
// (его читает botMessenger). Через контекст, а не через состояние чата: иначе
// уведомление, ушедшее в тот же чат в ту же секунду, закрылось бы тоже.
func withProtect(ctx context.Context, on bool) context.Context {
	if !on {
		return ctx
	}
	return context.WithValue(ctx, protectKey{}, true)
}

func protectedCtx(ctx context.Context) bool {
	on, _ := ctx.Value(protectKey{}).(bool)
	return on
}

// markEditProt — сообщение, под которым нажали кнопку, закрыто от пересылки.
func (a *App) markEditProt(chatID int64) {
	a.scrMu.Lock()
	defer a.scrMu.Unlock()
	if a.editProt == nil {
		a.editProt = map[int64]bool{}
	}
	a.editProt[chatID] = true
}

// editMatches — можно ли править сообщение, под которым нажали кнопку.
// Запрет пересылки задаётся только при отправке, поэтому экран с другим
// режимом уходит новым сообщением.
func (a *App) editMatches(chatID int64, prot bool) bool {
	a.scrMu.Lock()
	defer a.scrMu.Unlock()
	return a.editTarget[chatID] == 0 || a.editProt[chatID] == prot
}

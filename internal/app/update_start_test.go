package app

import (
	"context"
	"slices"
	"testing"

	"github.com/go-telegram/bot/models"
)

// Экран «Доступно обновление» после ручной проверки — баннер; текстом его не
// отредактировать, и после «Обновить» он не должен оставаться в чате.
func TestUpdateStartRemovesBannerScreen(t *testing.T) {
	a, fm, _ := newTestApp(t)
	ctx := context.Background()
	const chat = 100
	rows := [][]models.InlineKeyboardButton{{btn("x", "upd:now")}}
	a.sendSysKB(ctx, chat, "🆕 update", rows)
	old := a.screenMsgID(chat)
	if old == 0 {
		t.Fatal("экран не зарегистрирован")
	}
	id := a.postUpdateStart(ctx, chat, old, "starting", rows)
	if id == 0 || id == old {
		t.Fatalf("стартовое сообщение: %d (старое %d)", id, old)
	}
	fm.mu.Lock()
	gone := slices.Contains(fm.deleted, old)
	fm.mu.Unlock()
	if !gone {
		t.Fatal("старый экран не удалён")
	}
	if cur := a.screenMsgID(chat); cur != id {
		t.Fatalf("текущий экран %d, ждали %d", cur, id)
	}
}

// Уведомление по расписанию не экран — его тоже надо убрать.
func TestUpdateStartRemovesNotice(t *testing.T) {
	a, fm, _ := newTestApp(t)
	ctx := context.Background()
	const chat = 100
	notice := fm.SendKB(ctx, chat, "🆕 update", nil)
	id := a.postUpdateStart(ctx, chat, notice, "starting", nil)
	fm.mu.Lock()
	gone := slices.Contains(fm.deleted, notice)
	fm.mu.Unlock()
	if id == 0 || !gone {
		t.Fatalf("id=%d, уведомление удалено=%v", id, gone)
	}
}

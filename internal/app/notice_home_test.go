package app

import (
	"context"
	"slices"
	"testing"

	"remnabot/internal/model"
)

// Уведомление с кнопкой «Главная» уходит из чата, когда её нажимают; кнопки
// других сообщений его не трогают.
func TestNoticeRemovedOnItsHomeButton(t *testing.T) {
	const uid = int64(1000000001)
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		onNotice bool
		gone     bool
	}{
		{"«Главная» на уведомлении", true, true},
		{"кнопка другого экрана", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fm, _ := newTestApp(t)
			a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
			a.handleCallback(ctx, cb(uid, "menu:balance"))
			screen := fm.seq
			a.notify(ctx, uid, "✅ Ваш доступ в веб-кабинет одобрен. Войдите снова.")
			notice := fm.seq
			from := screen
			if tc.onNotice {
				from = notice
			}
			a.handleCallback(ctx, cbOn(uid, "menu:home", from))
			if got := slices.Contains(fm.deleted, notice); got != tc.gone {
				t.Fatalf("уведомление удалено: %v, ждали %v", got, tc.gone)
			}
			// Убранное уведомление не держит запись в памяти.
			a.scrMu.Lock()
			_, kept := a.notices[uid]
			a.scrMu.Unlock()
			if kept == tc.gone {
				t.Fatalf("запись учёта уведомлений осталась: %v", kept)
			}
		})
	}
}

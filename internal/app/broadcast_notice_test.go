package app

import (
	"context"
	"slices"
	"testing"
)

// «Запущена» уходит текстом, хотя предпросмотр — подпись под картинкой
// раздела: иначе итог не смог бы править её на месте.
func TestBroadcast_StartedIsTextScreen(t *testing.T) {
	a, _ := bcastSegApp(t, false)
	planTap(t, a, "bc:new")
	a.handleMessage(context.Background(), msgText(planAdmin, "msg"))
	if a.getScreenSection(planAdmin) == "" {
		t.Fatal("предпросмотр без картинки раздела — проверять нечего")
	}
	planTap(t, a, "bc:send")
	waitFor(t, func() bool { return !a.bcastRunning.Load() })
	if sec := a.getScreenSection(planAdmin); sec != "" {
		t.Fatalf("«запущена» осталась подписью под картинкой раздела %q", sec)
	}
}

func noticed(a *App, chatID int64, id int) bool {
	a.scrMu.Lock()
	defer a.scrMu.Unlock()
	return slices.Contains(a.notices[chatID], id)
}

func deletedMsg(fm *fakeMsg, id int) bool {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	return slices.Contains(fm.deleted, id)
}

// Итог рассылки, пришедший новым сообщением (админ уже ушёл с экрана
// «запущена»), уходит из чата по «Главной» под ним, как любое уведомление.
func TestBroadcast_FinishNoticeLeavesOnHome(t *testing.T) {
	ctx := context.Background()
	for _, home := range []string{"menu:home", "menu:marketing"} {
		t.Run(home, func(t *testing.T) {
			a, fm, _ := planAdminApp(t)
			planTap(t, a, "menu:marketing")
			fm.editOK = false // сообщения «запущена» уже нет
			a.bcastFinish(ctx, &bcastJob{Admin: planAdmin, StatusID: 999}, "✅ Рассылка завершена")
			id := fm.seq
			if !noticed(a, planAdmin, id) {
				t.Fatal("итог рассылки не учтён: после ухода с него он останется висеть")
			}
			a.handleCallback(ctx, cbMsg(planAdmin, home, id))
			if !deletedMsg(fm, id) {
				t.Fatalf("итог рассылки не убран после «%s»", home)
			}
		})
	}
}

// Пока «запущена» — текущий экран, итог правит его на месте и остаётся
// экраном, а не становится уведомлением.
func TestBroadcast_FinishEditsCurrentScreen(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	fm.editOK = true
	a.sendKBText(ctx, planAdmin, "🚀 запущена", nil)
	status := a.screenMsgID(planAdmin)
	sent := fm.seq
	a.bcastFinish(ctx, &bcastJob{Admin: planAdmin, StatusID: status}, "✅ Рассылка завершена")
	if fm.seq != sent || !slices.Contains(fm.edits, status) {
		t.Fatal("итог не вписан в экран «запущена»")
	}
	if noticed(a, planAdmin, status) || !a.isScreen(ctx, planAdmin, status) {
		t.Fatal("экран рассылки перестал быть экраном")
	}
}

// После перезапуска «продолжена» правит прежнее сообщение, которое бот уже не
// помнит экраном: оно учитывается как уведомление и тоже уходит по «Главной».
func TestBroadcast_ResumedStatusLeavesOnHome(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	fm.editOK = true
	status := fm.SendKB(ctx, planAdmin, "🚀 запущена", nil)
	if id := a.bcastStatus(ctx, planAdmin, status, "▶️ продолжена", nil); id != status {
		t.Fatalf("прежнее сообщение не исправлено: %d", id)
	}
	a.bcastFinish(ctx, &bcastJob{Admin: planAdmin, StatusID: status}, "✅ Рассылка завершена")
	if !noticed(a, planAdmin, status) {
		t.Fatal("сообщение рассылки после перезапуска не учтено")
	}
	fm.editOK = false // текст в экран с картинкой Telegram не правит
	a.handleCallback(ctx, cbMsg(planAdmin, "menu:home", status))
	if !deletedMsg(fm, status) {
		t.Fatal("сообщение рассылки осталось после «Главной»")
	}
}

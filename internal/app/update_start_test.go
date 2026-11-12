package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	id := a.showUpdateMsg(ctx, chat, old, "starting", rows)
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
	id := a.showUpdateMsg(ctx, chat, notice, "starting", nil)
	fm.mu.Lock()
	gone := slices.Contains(fm.deleted, notice)
	fm.mu.Unlock()
	if id == 0 || !gone {
		t.Fatalf("id=%d, уведомление удалено=%v", id, gone)
	}
}

// «Обновить» нажали на уведомлении под главным меню: уведомление становится
// сообщением об обновлении, а меню уходит.
func TestUpdateStartFromNoticeRemovesMenu(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	fm.editOK = true
	ctx := context.Background()
	const chat = 100
	a.sendKB(ctx, chat, "menu", nil)
	menu := a.screenMsgID(chat)
	a.setScreenSection(chat, "menu")
	notice := fm.SendKB(ctx, chat, "🆕 update", nil)
	id := a.showUpdateMsg(ctx, chat, notice, "starting", nil)
	if id != notice {
		t.Fatalf("стартовое сообщение %d, ждали правку уведомления %d", id, notice)
	}
	fm.mu.Lock()
	gone := slices.Contains(fm.deleted, menu)
	fm.mu.Unlock()
	if !gone {
		t.Fatal("главное меню осталось в чате")
	}
	if cur := a.screenMsgID(chat); cur != notice {
		t.Fatalf("текущий экран %d, ждали %d", cur, notice)
	}
	if pid, _ := fs.GetScreenMsg(ctx, chat); pid != notice {
		t.Fatalf("в базе экран %d, ждали %d", pid, notice)
	}
	// Раздел прежнего экрана остался бы — и следующий переход в тот же раздел
	// правил бы подпись у текстового сообщения.
	if sec := a.getScreenSection(chat); sec != "" {
		t.Fatalf("раздел экрана %q, ждали пустой", sec)
	}
}

// После перезапуска «обновлено» встаёт на место экрана, открытого во время
// скачивания, а тот экран удаляется, а не теряется.
func TestUpdatedRemovesScreenOpenedDuringPull(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	fm.editOK = true
	ctx := context.Background()
	const chat = 100
	start := fm.SendKB(ctx, chat, "starting", nil)
	menu := fm.SendKB(ctx, chat, "menu", nil)
	_ = fs.SetScreenMsg(ctx, chat, menu)
	marker := filepath.Join(a.cfg.DataDir, "update.pending")
	if err := os.WriteFile(marker, []byte("100:"+strconv.Itoa(start)+":old"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.notifyUpdated(ctx)
	fm.mu.Lock()
	gone := slices.Contains(fm.deleted, menu)
	fm.mu.Unlock()
	if !gone {
		t.Fatal("экран, открытый во время скачивания, остался в чате")
	}
	if pid, _ := fs.GetScreenMsg(ctx, chat); pid != start {
		t.Fatalf("в базе экран %d, ждали %d", pid, start)
	}
}

// Обновить кнопкой нельзя, а нажали на уведомлении: ответ «недоступно» встаёт
// на место главного меню и не исчезает вместе с таймером уведомления.
func TestUpdateUnavailableFromNoticeBecomesScreen(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	fm.editOK = true
	ctx := context.Background()
	const chat = 100
	a.sendKB(ctx, chat, "menu", nil)
	menu := a.screenMsgID(chat)
	notice := fm.SendKB(ctx, chat, "🆕 update", nil)
	a.setUpdNotice(chat, notice)
	a.handleMessage(ctx, msgText(chat, "/update"))
	if cur := a.screenMsgID(chat); cur != notice {
		t.Fatalf("текущий экран %d, ждали %d", cur, notice)
	}
	a.expireUpdNotice(chat, notice)
	fm.mu.Lock()
	menuGone := slices.Contains(fm.deleted, menu)
	noticeGone := slices.Contains(fm.deleted, notice)
	fm.mu.Unlock()
	if !menuGone {
		t.Fatal("главное меню осталось в чате")
	}
	if noticeGone {
		t.Fatal("таймер уведомления удалил текущий экран")
	}
}

// Уведомление, по которому не нажали, таймер убирает.
func TestUpdateNoticeExpiresWhenNotPressed(t *testing.T) {
	a, fm, _ := newTestApp(t)
	ctx := context.Background()
	const chat = 100
	notice := fm.SendKB(ctx, chat, "🆕 update", nil)
	a.setUpdNotice(chat, notice)
	a.expireUpdNotice(chat, notice)
	fm.mu.Lock()
	gone := slices.Contains(fm.deleted, notice)
	fm.mu.Unlock()
	if !gone {
		t.Fatal("уведомление осталось в чате")
	}
}

// Стартовое сообщение пропало, пока скачивался образ: «обновлено» уходит
// новым сообщением, становится экраном, а прежний экран удаляется.
func TestUpdatedSendsNewWhenStartGone(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	ctx := context.Background()
	const chat = 100
	start := fm.SendKB(ctx, chat, "starting", nil)
	menu := fm.SendKB(ctx, chat, "menu", nil)
	_ = fs.SetScreenMsg(ctx, chat, menu)
	marker := filepath.Join(a.cfg.DataDir, "update.pending")
	if err := os.WriteFile(marker, []byte("100:"+strconv.Itoa(start)+":old"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.notifyUpdated(ctx)
	done := a.screenMsgID(chat)
	if done == 0 || done == start || done == menu {
		t.Fatalf("текущий экран %d", done)
	}
	if pid, _ := fs.GetScreenMsg(ctx, chat); pid != done {
		t.Fatalf("в базе экран %d, ждали %d", pid, done)
	}
	fm.mu.Lock()
	menuGone := slices.Contains(fm.deleted, menu)
	doneGone := slices.Contains(fm.deleted, done)
	fm.mu.Unlock()
	if !menuGone || doneGone {
		t.Fatalf("меню удалено=%v, «обновлено» удалено=%v", menuGone, doneGone)
	}
}

// После перезапуска экран, отредактированный по нажатию на старом сообщении,
// убирает прежний экран, известный только из базы.
func TestEditInPlaceAfterRestartRemovesStoredScreen(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	fm.editOK = true
	ctx := context.Background()
	const chat = 100
	older := fm.SendKB(ctx, chat, "older", nil)
	cur := fm.SendKB(ctx, chat, "current", nil)
	_ = fs.SetScreenMsg(ctx, chat, cur)
	a.setEditTarget(chat, older)
	a.sendKB(ctx, chat, "next", nil)
	fm.mu.Lock()
	curGone := slices.Contains(fm.deleted, cur)
	olderGone := slices.Contains(fm.deleted, older)
	fm.mu.Unlock()
	if !curGone || olderGone {
		t.Fatalf("прежний экран удалён=%v, отредактированный удалён=%v", curGone, olderGone)
	}
	if pid, _ := fs.GetScreenMsg(ctx, chat); pid != older {
		t.Fatalf("в базе экран %d, ждали %d", pid, older)
	}
}

// Бот останавливается на перезапуск ровно когда истекло ожидание: маркер
// остаётся новому процессу, ложного «рестарт не произошёл» нет.
func TestNoRestartKeepsMarkerOnShutdown(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	fm.editOK = true
	ctx := context.Background()
	const chat = 100
	start := fm.SendKB(ctx, chat, "starting", nil)
	marker := filepath.Join(a.cfg.DataDir, "update.pending")
	if err := os.WriteFile(marker, []byte("100:"+strconv.Itoa(start)+":old"), 0o600); err != nil {
		t.Fatal(err)
	}
	stopped, stop := context.WithCancel(ctx)
	stop()
	a.bgCtx = stopped
	a.reportNoRestart(chat, start, marker)
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("маркер удалён при остановке")
	}
	fm.mu.Lock()
	edited := len(fm.edited)
	fm.mu.Unlock()
	if edited != 0 {
		t.Fatal("при остановке ушло сообщение")
	}
	a.bgCtx = ctx
	a.reportNoRestart(chat, start, marker)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("маркер остался, хотя рестарта не было")
	}
	if cur := a.screenMsgID(chat); cur != start {
		t.Fatalf("текущий экран %d, ждали %d", cur, start)
	}
}

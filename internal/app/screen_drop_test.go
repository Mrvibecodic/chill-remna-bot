package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

func waitDeleted(t *testing.T, fm *fakeMsg, id int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fm.mu.Lock()
		done := slices.Contains(fm.deleted, id)
		fm.mu.Unlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("сообщение %d так и не удалено", id)
}

func liveIDs(fm *fakeMsg) []int {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	var ids []int
	for id := range fm.live {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Переход между экранами: новый экран приходит раньше, чем удаляется старый,
// иначе чат на миг пустеет и Telegram показывает «Запустить».
func TestScreenSwapSendsBeforeDelete(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	a.screenDropDelay = 100 * time.Millisecond
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "menu:iface"))
	first := a.screenMsgID(100)
	if first == 0 {
		t.Fatal("первый экран не отрисован")
	}
	a.handleCallback(ctx, cb(100, "tx:home"))
	second := a.screenMsgID(100)
	if second == 0 || second == first {
		t.Fatalf("второй экран не отрисован: %d", second)
	}
	if live := liveIDs(fm); !slices.Contains(live, first) || !slices.Contains(live, second) {
		t.Fatalf("сразу после перехода должны быть видны оба экрана: %v", live)
	}
	waitDeleted(t, fm, first)
	if live := liveIDs(fm); slices.Contains(live, first) || !slices.Contains(live, second) {
		t.Fatalf("после задержки остаться должен только новый экран: %v", live)
	}
}

// Новый экран не ушёл — старый остаётся и считается текущим.
func TestScreenKeptWhenNewFails(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	ctx := context.Background()
	a.sendKB(ctx, 100, "первый", [][]models.InlineKeyboardButton{homeRow("ru")})
	first := a.screenMsgID(100)
	fm.kbFail = true
	a.sendKB(ctx, 100, "второй", [][]models.InlineKeyboardButton{homeRow("ru")})
	if slices.Contains(fm.deleted, first) {
		t.Fatal("старый экран удалён, хотя новый не доставлен")
	}
	if a.screenMsgID(100) != first {
		t.Fatal("старый экран перестал быть текущим")
	}
	fm.kbFail = false
	a.sendKB(ctx, 100, "третий", [][]models.InlineKeyboardButton{homeRow("ru")})
	if !slices.Contains(fm.deleted, first) {
		t.Fatal("старый экран не удалён при следующем переходе")
	}
}

// «🏠 Главная» под уведомлением: меню приходит раньше, чем убирается уведомление.
func TestCloseHomeSendsMenuFirst(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	a.screenDropDelay = 100 * time.Millisecond
	ctx := context.Background()
	note := fm.add("уведомление")
	q := cb(100, cbClose+":home")
	q.Message = models.MaybeInaccessibleMessage{Message: &models.Message{ID: note}}
	a.handleCallback(ctx, q)
	menu := a.screenMsgID(100)
	if menu == 0 || menu == note {
		t.Fatalf("меню не отрисовано: %d", menu)
	}
	if live := liveIDs(fm); !slices.Contains(live, note) {
		t.Fatalf("уведомление удалено раньше, чем пришло меню: %v", live)
	}
	waitDeleted(t, fm, note)
}

// Сообщение, которое за время задержки стало текущим экраном, не удаляется.
func TestDropScreenSparesCurrent(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	ctx := context.Background()
	a.sendKB(ctx, 100, "экран", [][]models.InlineKeyboardButton{homeRow("ru")})
	cur := a.screenMsgID(100)
	a.dropScreen(100, []int{cur})
	if slices.Contains(fm.deleted, cur) {
		t.Fatal("удалён текущий экран")
	}
}

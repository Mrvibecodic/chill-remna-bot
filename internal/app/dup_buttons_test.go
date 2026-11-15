package app

import (
	"context"
	"testing"

	"github.com/go-telegram/bot/models"
)

func countCB(rows [][]models.InlineKeyboardButton, data string) int {
	n := 0
	for _, r := range rows {
		for _, b := range r {
			if b.CallbackData == data {
				n++
			}
		}
	}
	return n
}

// Отказ сброса устройств — обычный экран с одной «Главной», а не отдельное
// уведомление, которое оставалось в чате с двумя.
func TestDeviceResetFailIsScreen(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	const uid int64 = 1000000001
	a.handleCallback(context.Background(), cb(uid, "dev:confirm"))
	if n := countCB(fm.lastKB, "menu:home"); n != 1 {
		t.Fatalf("«Главная» под отказом %d раз: %v", n, kbLabels(fm.lastKB))
	}
	if countCB(fm.lastKB, "menu:mysubs") != 1 {
		t.Fatalf("нет «Назад» к подписке: %v", kbLabels(fm.lastKB))
	}
	if a.screenMsgID(uid) == 0 {
		t.Fatal("отказ не стал экраном — останется висеть в чате")
	}
}

// Уведомление, в котором «Главная» уже есть, не получает вторую.
func TestNotifyKBNoSecondHome(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	const uid int64 = 1000000001
	a.notifyKB(context.Background(), uid, "текст", [][]models.InlineKeyboardButton{navBack("ru", "menu:mysubs")})
	if n := countCB(fm.lastKB, "menu:home"); n != 1 {
		t.Fatalf("«Главная» %d раз: %v", n, kbLabels(fm.lastKB))
	}
	a.notifyKB(context.Background(), uid, "текст", [][]models.InlineKeyboardButton{{btn("x", "menu:buy")}})
	if n := countCB(fm.lastKB, "menu:home"); n != 1 {
		t.Fatalf("уведомлению без «Главной» она не добавлена: %v", kbLabels(fm.lastKB))
	}
}

// Кнопка из старой версии бота: одна «Главная».
func TestOutdatedButtonSingleHome(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	a.handleCallback(context.Background(), cb(1000000001, "nosuchdomain:x"))
	if n := countCB(fm.lastKB, "menu:home"); n != 1 {
		t.Fatalf("«Главная» %d раз: %v", n, kbLabels(fm.lastKB))
	}
}

// На экране правил триала три переключателя — подписи у них разные.
func TestTrialTogglesDistinct(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	a.handleCallback(context.Background(), cb(100, "trial:rules"))
	a.handleCallback(context.Background(), cb(100, "trial:resettoggle"))
	seen := map[string]bool{}
	for _, r := range fm.lastKB {
		for _, b := range r {
			if seen[b.Text] {
				t.Fatalf("две кнопки «%s»: %v", b.Text, kbLabels(fm.lastKB))
			}
			seen[b.Text] = true
		}
	}
	for _, d := range []string{"trial:allowbuy", "trial:email", "trial:resettoggle", "trial:resetmax", "trial:resetpct"} {
		if countCB(fm.lastKB, d) != 1 {
			t.Fatalf("на экране правил нет %s: %v", d, kbLabels(fm.lastKB))
		}
	}
}

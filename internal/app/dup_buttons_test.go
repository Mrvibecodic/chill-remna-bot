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

// На экране триала два переключателя — подписи у них разные.
func TestTrialTogglesDistinct(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	a.handleCallback(context.Background(), cb(100, "menu:trial"))
	seen := map[string]bool{}
	for _, r := range fm.lastKB {
		for _, b := range r {
			if seen[b.Text] {
				t.Fatalf("две кнопки «%s»: %v", b.Text, kbLabels(fm.lastKB))
			}
			seen[b.Text] = true
			if b.CallbackData == "trial:resetpct" && b.Text != "📉 Порог трафика (%)" {
				t.Fatalf("подпись порога: %q", b.Text)
			}
		}
	}
	if countCB(fm.lastKB, "trial:resettoggle") != 1 {
		t.Fatalf("экран триала не отрисован: %v", kbLabels(fm.lastKB))
	}
}

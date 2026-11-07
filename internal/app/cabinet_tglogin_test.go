package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// Переключатель способа входа через Telegram: по умолчанию прежний виджет,
// кнопка в экране кабинета меняет его туда и обратно, правка сохраняется.
func TestCabinetTGLoginToggle(t *testing.T) {
	a, fm, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	if a.CabinetTGLoginNew() {
		t.Fatal("по умолчанию должен быть прежний виджет")
	}
	a.handleCallback(ctx, cb(100, "menu:cabinet"))
	if !strings.Contains(fm.lastKBButton("menu:cabtgl"), "старый виджет") {
		t.Fatal("в экране кабинета нет кнопки способа входа со значением «старый виджет»")
	}
	a.handleCallback(ctx, cb(100, "menu:cabtgl"))
	if !a.CabinetTGLoginNew() {
		t.Fatal("кнопка не включила новый способ")
	}
	if !strings.Contains(fm.lastKBButton("menu:cabtgl"), "новый") {
		t.Fatal("кнопка не показывает новое значение")
	}
	a.handleCallback(ctx, cb(555, "menu:cabtgl"))
	if !a.CabinetTGLoginNew() {
		t.Fatal("переключатель сработал не у админа")
	}
	a.handleCallback(ctx, cb(100, "menu:cabtgl"))
	if a.CabinetTGLoginNew() {
		t.Fatal("кнопка не вернула прежний виджет")
	}
}

// lastKBButton — подпись кнопки с данным callback в последней клавиатуре.
func (f *fakeMsg) lastKBButton(data string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.lastKB {
		for _, b := range row {
			if b.CallbackData == data {
				return b.Text
			}
		}
	}
	return ""
}

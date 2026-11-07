package app

import (
	"context"
	"testing"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// «Отмена» ввода промокода возвращает туда, откуда ввод открыли: из кошелька —
// в кошелёк, с экрана промокода — на экран промокода.
func TestPromoCancelReturnsToOrigin(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		back   string // кнопка, по которой узнаётся экран после «Отмены»
	}{
		{"из кошелька", "menu:balance", "menu:buy"},
		{"с экрана промокода", "menu:promo", "pr:enter"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, fm, _ := newTestApp(t)
			a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
			ctx := context.Background()
			a.handleCallback(ctx, cb(1000000001, c.screen))
			promo := fm.lastKBData(i18n.T("ru", "btn.promo"), i18n.T("ru", "promo.btn_enter"))
			if promo == "" {
				t.Fatal("на экране нет кнопки промокода")
			}
			a.handleCallback(ctx, cb(1000000001, promo))
			if fm.lastKBButton("inp:cancel") == "" {
				t.Fatal("ввод промокода не открылся")
			}
			a.handleCallback(ctx, cb(1000000001, "inp:cancel"))
			if fm.lastKBButton(c.back) == "" {
				t.Fatalf("после «Отмены» не тот экран: нет кнопки %s", c.back)
			}
			if a.getUI(1000000001).awaitPromo {
				t.Fatal("после «Отмены» бот всё ещё ждёт промокод")
			}
		})
	}
}

// lastKBData — callback первой кнопки последней клавиатуры с одной из подписей.
func (f *fakeMsg) lastKBData(labels ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.lastKB {
		for _, b := range row {
			for _, l := range labels {
				if b.Text == l {
					return b.CallbackData
				}
			}
		}
	}
	return ""
}

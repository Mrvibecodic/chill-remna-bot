package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"remnabot/internal/assets"
)

// Обрезка длинных строк: имя, ошибка и поле устройства получают «…», поле
// отчёта торрент-блокера режется без него — оно идёт в базу, а не на экран.
func TestClipHelpersKeepTheirTails(t *testing.T) {
	long := strings.Repeat("я", 400)
	cases := []struct {
		name string
		got  string
		n    int
		tail string
	}{
		{"имя", escapeName("  " + long + "  "), nameMaxLen, "…"},
		{"ошибка", escapeErr(errors.New(long)), errMaxLen, "…"},
		{"поле устройства", cutField(long), deviceFieldMaxLen, "…"},
		{"поле отчёта", clipField(long), fieldMaxLen, ""},
	}
	for _, c := range cases {
		if want := strings.Repeat("я", c.n) + c.tail; c.got != want {
			t.Errorf("%s: получено %d знаков с хвостом %q, ожидалось %d и %q",
				c.name, len([]rune(c.got)), string([]rune(c.got)[len([]rune(c.got))-1:]), c.n, c.tail)
		}
	}
	if got := escapeName(" <b> "); got != "&lt;b&gt;" {
		t.Errorf("короткое имя не режется и экранируется: %q", got)
	}
}

// Кнопки экрана баннеров: действие и раздел разбираются из данных кнопки.
func TestSectionBannerButtonsRoute(t *testing.T) {
	a, _, _ := newTestApp(t)
	ctx := context.Background()
	sec := assets.SectionMySubscription

	a.handleCallback(ctx, cb(100, "sec:upload:"+sec))
	if got := a.getUI(100).awaitSectionBanner; got != sec {
		t.Fatalf("после «Загрузить» бот должен ждать картинку раздела %q, ждёт %q", sec, got)
	}
	a.handleCallback(ctx, cb(100, "sec:cancel:"+sec))
	if got := a.getUI(100).awaitSectionBanner; got != "" {
		t.Fatalf("«Отмена» должна снять ожидание, осталось %q", got)
	}
}

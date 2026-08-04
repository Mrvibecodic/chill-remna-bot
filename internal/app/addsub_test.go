package app

import (
	"testing"

	"remnabot/internal/remnawave"
)

func TestFormatGB(t *testing.T) {
	const gb = int64(1024 * 1024 * 1024)
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{-5, "0"},
		{gb, "1"},
		{50 * gb, "50"},
		{gb / 2, "0.5"},
		{gb*12 + gb/2, "12.5"},
	}
	for _, c := range cases {
		if got := formatGB(c.in); got != c.want {
			t.Errorf("formatGB(%d) = %q, ожидалось %q", c.in, got, c.want)
		}
	}
}

func TestAddSubTargets(t *testing.T) {
	in := []remnawave.PanelUser{
		{Username: "tg_1", TelegramID: 1, Tag: remnawave.BotTag},           // свой, тег бота
		{Username: "vasya", TelegramID: 2},                                 // усыновлён panelsync (тега нет)
		{Username: "tg_3_addsub", TelegramID: 0, Tag: remnawave.BotTagAdd}, // сама доп-подписка
		{Username: "nobody", TelegramID: 0},                                // без telegram id
		{Username: "alien", TelegramID: 5, Tag: "OTHER_BOT"},               // чужая система
	}
	got := addSubTargets(in)
	if len(got) != 2 || got[0].Username != "tg_1" || got[1].Username != "vasya" {
		t.Fatalf("отобраны не те пользователи: %+v", got)
	}
}

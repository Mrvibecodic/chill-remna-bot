package app

import (
	"testing"
	"time"

	"remnabot/internal/model"
)

func TestUserSubStatus(t *testing.T) {
	now := time.Date(2030, 1, 10, 12, 0, 0, 0, time.UTC)
	future := now.Add(48 * time.Hour).Format(time.RFC3339)
	past := now.Add(-time.Hour).Format(time.RFC3339)
	cases := []struct {
		name        string
		u           model.User
		icon, style string
	}{
		{"нет подписки", model.User{}, "⚪", ""},
		{"битая дата", model.User{SubExpireAt: "мусор"}, "⚪", ""},
		{"активна", model.User{SubExpireAt: future, NotifyKind: "paid"}, "🟢", "success"},
		{"триал", model.User{SubExpireAt: future, NotifyKind: "trial"}, "🔵", "primary"},
		{"истекла", model.User{SubExpireAt: past, NotifyKind: "paid"}, "🔴", "danger"},
		{"истёкший триал", model.User{SubExpireAt: past, NotifyKind: "trial"}, "🔴", "danger"},
		{"заблокирован", model.User{Blocked: true, SubExpireAt: future}, "🚫", ""},
	}
	for _, c := range cases {
		icon, style := userSubStatus(&c.u, now)
		if icon != c.icon || style != c.style {
			t.Errorf("%s: %q/%q, ждали %q/%q", c.name, icon, style, c.icon, c.style)
		}
	}
	b := userListBtn(&model.User{TelegramID: 1000000001, SubExpireAt: future}, "x", now)
	if b.Text != "🟢 x" || b.Style != "success" || b.CallbackData != "usr:view:1000000001" {
		t.Errorf("кнопка: %+v", b)
	}
}

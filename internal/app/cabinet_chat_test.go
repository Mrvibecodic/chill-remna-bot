package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

func TestParseChatSrc(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"код вставки", `<script src="https://sub.example.com/chat.php?widget=lk" async></script>`, "https://sub.example.com/chat.php?widget=lk"},
		{"одинарные кавычки", `<script async src='https://code.example.com/widget/abc'></script>`, "https://code.example.com/widget/abc"},
		{"без кавычек", `<script src=https://code.example.com/w.js></script>`, "https://code.example.com/w.js"},
		{"адрес без схемы", `<script src="//code.example.com/w.js" async></script>`, "https://code.example.com/w.js"},
		{"сущности в адресе", `<script src="https://example.com/w.js?a=1&amp;b=2"></script>`, "https://example.com/w.js?a=1&b=2"},
		{"голый адрес", "  https://example.com/chat.js \n", "https://example.com/chat.js"},
		{"http", "http://example.com/chat.js", ""},
		{"скрипт без адреса", "<script>alert(1)</script>", ""},
		{"data-src не адрес", `<script data-src="https://example.com/a.js"></script>`, ""},
		{"не скрипт", `<img src="https://example.com/a.png">`, ""},
		{"javascript", "javascript:alert(1)", ""},
		{"кавычка в адресе", `https://example.com/a.js"onload="x`, ""},
		{"пробел в адресе", "https://example.com/a b.js", ""},
		{"логин в адресе", "https://user@example.com/a.js", ""},
		{"без домена", "https://localhost/a.js", ""},
		{"пусто", "", ""},
		{"слишком длинный", "https://example.com/" + strings.Repeat("a", chatSrcMax), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseChatSrc(tc.in)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("получено %q, %v", got, ok)
			}
		})
	}
}

// Настройка чата проверяется через настоящий вход: кнопка, текст ответа, экран.
func TestCabinetChatAdmin(t *testing.T) {
	a, fm, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	const code = `<script src="https://sub.example.com/chat.php?widget=lk" async></script>`
	const src = "https://sub.example.com/chat.php?widget=lk"

	a.handleCallback(ctx, cb(100, "menu:cabinet"))
	if fm.lastKBButton("menu:cabchat") == "" || fm.lastKBButton("menu:cabchatvis") != "" {
		t.Fatal("без чата на экране должна быть только кнопка ввода")
	}

	a.handleCallback(ctx, cb(555, "menu:cabchat"))
	a.handleMessage(ctx, msgText(555, code))
	if s, _ := a.CabinetChat(); s != "" {
		t.Fatal("чат задал не админ")
	}

	a.handleCallback(ctx, cb(100, "menu:cabchat"))
	a.handleMessage(ctx, msgText(100, "<script>alert(1)</script>"))
	if s, _ := a.CabinetChat(); s != "" || a.getUI(100).adminInput != "cab_chat" {
		t.Fatalf("код без адреса: сохранено %q, ожидание ввода %q", s, a.getUI(100).adminInput)
	}
	a.handleMessage(ctx, msgText(100, code))
	s, all := a.CabinetChat()
	if s != src || all {
		t.Fatalf("после ввода: %q, везде=%v", s, all)
	}
	if !strings.Contains(fm.lastKBButton("menu:cabchatvis"), "после входа") {
		t.Fatalf("кнопка видимости: %q", fm.lastKBButton("menu:cabchatvis"))
	}

	a.handleCallback(ctx, cb(555, "menu:cabchatvis"))
	if _, all := a.CabinetChat(); all {
		t.Fatal("видимость переключил не админ")
	}
	a.handleCallback(ctx, cb(100, "menu:cabchatvis"))
	if _, all := a.CabinetChat(); !all {
		t.Fatal("кнопка не включила показ везде")
	}
	if !strings.Contains(fm.lastKBButton("menu:cabchatvis"), "везде") {
		t.Fatalf("кнопка видимости: %q", fm.lastKBButton("menu:cabchatvis"))
	}

	a.handleCallback(ctx, cb(100, "menu:cabchat"))
	a.handleMessage(ctx, msgText(100, "-"))
	if s, _ := a.CabinetChat(); s != "" {
		t.Fatalf("прочерк не убрал чат: %q", s)
	}
	a.handleCallback(ctx, cb(100, "menu:cabchat"))
	a.handleMessage(ctx, msgText(100, src))
	if s, all := a.CabinetChat(); s != src || all {
		t.Fatalf("новый чат после удаления: %q, везде=%v", s, all)
	}
}

// Адрес, попавший в конфиг мимо проверки ввода, на страницу не уходит.
func TestCabinetChatBadStored(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.Cabinet.ChatSrc = "http://example.com/chat.js"
	a.botCfg.Cabinet.ChatAll = true
	if s, all := a.CabinetChat(); s != "" || all {
		t.Fatalf("непроверенный адрес отдан: %q, %v", s, all)
	}
}

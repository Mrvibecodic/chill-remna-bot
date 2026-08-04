package config

import "testing"

func TestLoadValid(t *testing.T) {
	t.Setenv("BOT_TOKEN", "tok")
	t.Setenv("ADMIN_TELEGRAM_ID", "12345")
	t.Setenv("DATA_DIR", "/data")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BotToken != "tok" || c.AdminID != 12345 || c.DataDir != "/data" {
		t.Fatalf("неожиданный конфиг: %+v", c)
	}
}

func TestLoadMissingToken(t *testing.T) {
	t.Setenv("BOT_TOKEN", "")
	t.Setenv("ADMIN_TELEGRAM_ID", "1")
	if _, err := Load(); err == nil {
		t.Fatal("ожидалась ошибка при пустом BOT_TOKEN")
	}
}

func TestLoadBadAdminID(t *testing.T) {
	t.Setenv("BOT_TOKEN", "tok")
	t.Setenv("ADMIN_TELEGRAM_ID", "not-a-number")
	if _, err := Load(); err == nil {
		t.Fatal("ожидалась ошибка при нечисловом ADMIN_TELEGRAM_ID")
	}
}

func TestLoadDefaultDataDir(t *testing.T) {
	t.Setenv("BOT_TOKEN", "tok")
	t.Setenv("ADMIN_TELEGRAM_ID", "1")
	t.Setenv("DATA_DIR", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/data" {
		t.Fatalf("DATA_DIR по умолчанию = %q", c.DataDir)
	}
}

func TestParseEmojiMap(t *testing.T) {
	if parseEmojiMap("") != nil {
		t.Fatal("пустая строка -> nil")
	}
	m := parseEmojiMap("✅=123, ⏳=456 ,bad")
	if m["✅"] != "123" || m["⏳"] != "456" {
		t.Fatalf("неверный парсинг: %+v", m)
	}
	if _, ok := m["bad"]; ok {
		t.Fatal("пара без '=' не должна попадать в карту")
	}
}

func TestLoadCaddyAuthToken(t *testing.T) {
	t.Setenv("BOT_TOKEN", "tok")
	t.Setenv("ADMIN_TELEGRAM_ID", "1")
	t.Setenv("CADDY_AUTH_API_TOKEN", "  key-123  ")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.CaddyAuthToken != "key-123" {
		t.Fatalf("CaddyAuthToken = %q, ожидалось обрезанное значение", c.CaddyAuthToken)
	}
}

func TestLoadCaddyAuthTokenUnset(t *testing.T) {
	t.Setenv("BOT_TOKEN", "tok")
	t.Setenv("ADMIN_TELEGRAM_ID", "1")
	t.Setenv("CADDY_AUTH_API_TOKEN", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.CaddyAuthToken != "" {
		t.Fatalf("CaddyAuthToken = %q, ожидалась пустая строка", c.CaddyAuthToken)
	}
}

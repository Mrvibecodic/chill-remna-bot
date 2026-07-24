package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	BotToken string
	AdminID  int64
	DataDir  string
	// StaticDir — папка с кастомной статикой мини-аппа/кабинета (оверлей поверх
	// вшитой); env CUSTOM_STATIC_DIR, по умолчанию /custom. Если папки нет —
	// работает вшитый дизайн.
	StaticDir string

	DBKind      string
	DatabaseURL string
	SecretKey   string

	PremiumEmoji map[string]string

	Commit    string
	BuildDate string
}

func Load() (*Config, error) {
	c := &Config{
		BotToken:     strings.TrimSpace(os.Getenv("BOT_TOKEN")),
		DataDir:      envOr("DATA_DIR", "/data"),
		StaticDir:    envOr("CUSTOM_STATIC_DIR", "/custom"),
		DBKind:       strings.TrimSpace(os.Getenv("DB_KIND")),
		DatabaseURL:  strings.TrimSpace(os.Getenv("DATABASE_URL")),
		SecretKey:    os.Getenv("SECRET_KEY"),
		PremiumEmoji: parseEmojiMap(os.Getenv("PREMIUM_EMOJI")),
	}
	if c.BotToken == "" {
		return nil, fmt.Errorf("BOT_TOKEN не задан")
	}
	rawAdmin := strings.TrimSpace(os.Getenv("ADMIN_TELEGRAM_ID"))
	if rawAdmin == "" {
		return nil, fmt.Errorf("ADMIN_TELEGRAM_ID не задан")
	}
	id, err := strconv.ParseInt(rawAdmin, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("ADMIN_TELEGRAM_ID должен быть числом: %w", err)
	}
	c.AdminID = id
	return c, nil
}

func parseEmojiMap(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	m := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && k != "" && v != "" {
			m[k] = v
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Package model содержит общие типы конфигурации, разделяемые между слоями
// (storage, remnawave, app), чтобы не плодить циклические импорты.
package model

import "encoding/json"

// Поддерживаемые движки БД.
const (
	DBSQLite   = "sqlite"
	DBPostgres = "postgres"
)

// Режим расположения бота относительно панели.
const (
	ModeLocal  = "local"  // бот в одной docker-сети с панелью, бьём в remnawave:3000 напрямую
	ModeRemote = "remote" // бот на другом сервере, ходим через публичный HTTPS-домен
)

// Способ установки панели (влияет на тип защиты публичного /api).
const (
	InstallDocs   = "docs"   // официальная установка (Caddy + caddy-with-auth)
	InstallEGames = "egames" // скрипт eGames (nginx + защита по куке)
)

// Языки интерфейса.
const (
	LangRU = "ru"
	LangEN = "en"
)

// PanelConfig — параметры подключения к панели Remnawave.
//
// Какие поля заполняются, зависит от Mode и InstallType:
//   - local:            BaseURL подставляется автоматически, Cookie/APIKey не нужны.
//   - remote + egames:  нужен Cookie ("ИМЯ=ЗНАЧЕНИЕ" из nginx.conf панели).
//   - remote + docs:    APIKey (X-API-Key) — только если оператор защитил /api в Caddy.
type PanelConfig struct {
	Mode        string `json:"mode"`
	InstallType string `json:"install_type"`
	BaseURL     string `json:"base_url"`
	APIToken    string `json:"api_token"` // Bearer-токен панели (role API)
	Cookie      string `json:"cookie"`    // "name=value" для eGames nginx, иначе ""
	APIKey      string `json:"api_key"`   // X-API-Key для защищённого Caddy /api, иначе ""
}

// BotConfig — вся конфигурация бота, хранится одной зашифрованной строкой в БД.
type BotConfig struct {
	Installed bool          `json:"installed"`
	Language  string        `json:"language"`
	DBKind    string        `json:"db_kind"`
	Panel     PanelConfig   `json:"panel"`
	P2P       P2PConfig     `json:"p2p"`
	Welcome   WelcomeConfig `json:"welcome"`
	// PremiumEmoji: карта "обычный эмодзи" -> custom_emoji_id (анимированные premium),
	// заполняется через /emoji. Дополняет/перекрывает env PREMIUM_EMOJI.
	PremiumEmoji map[string]string `json:"premium_emoji"`
}

// WelcomeConfig — стартовый баннер: картинка (file_id или URL) + текст с
// форматированием (entities Telegram, чтобы сохранить переносы и стили).
type WelcomeConfig struct {
	ImageFileID string          `json:"image_file_id"`
	ImageURL    string          `json:"image_url"`
	Text        string          `json:"text"`
	Entities    json.RawMessage `json:"entities"`
}

// PlanMonths — фиксированные сроки подписки (в месяцах).
var PlanMonths = []int{1, 3, 6, 12}

// Статусы заявки на P2P-оплату.
const (
	P2PAwaiting  = "awaiting"  // ждём скриншот оплаты
	P2PSubmitted = "submitted" // скрин загружен, на проверке
	P2PApproved  = "approved"
	P2PRejected  = "rejected"
)

// P2PConfig — настройки P2P-оплаты (перевод на карту с ручной проверкой).
type P2PConfig struct {
	Enabled   bool           `json:"enabled"`
	Cards     []string       `json:"cards"`      // реквизиты карт
	Rotate    bool           `json:"rotate"`     // выдавать карты по кругу
	RotateIdx int            `json:"rotate_idx"` // текущий индекс ротации
	Prices    map[int]string `json:"prices"`     // месяцы(1/3/6/12) -> цена
	Currency  string         `json:"currency"`   // символ валюты, напр. "руб"
	SquadUUID string         `json:"squad_uuid"` // сквад для создаваемых юзеров
}

// User — запись пользователя бота (гейт доступа к P2P и т.п.).
type User struct {
	TelegramID  int64
	P2PApproved bool
	Blocked     bool
	CreatedAt   string
}

// P2PRequest — заявка на оплату через P2P (ручная модерация).
type P2PRequest struct {
	ID         int64
	TelegramID int64
	Months     int
	Price      string
	Status     string
	Screenshot string // file_id скриншота
	Comment    string // причина отказа (при rejected)
	CreatedAt  string
	DecidedAt  string
}

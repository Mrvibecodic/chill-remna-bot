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
	Installed bool            `json:"installed"`
	Language  string          `json:"language"`
	DBKind    string          `json:"db_kind"`
	Panel     PanelConfig     `json:"panel"`
	P2P       P2PConfig       `json:"p2p"`
	Stars     StarsConfig     `json:"stars"`
	YooKassa  YooKassaConfig  `json:"yookassa"`
	CryptoBot CryptoBotConfig `json:"cryptobot"`
	Webhook   WebhookConfig   `json:"webhook"`
	Reminders RemindersConfig `json:"reminders"`
	Pricing   Pricing         `json:"pricing"`
	Welcome   WelcomeConfig   `json:"welcome"`
	// PremiumEmoji: карта "обычный эмодзи" -> custom_emoji_id (анимированные premium),
	// заполняется через /emoji. Дополняет/перекрывает env PREMIUM_EMOJI.
	PremiumEmoji map[string]string `json:"premium_emoji"`
	// SubscriptionDomain — если непусто, бот подменяет хост в ссылке
	// подписки на этот домен, сохраняя путь/short-id панели. Удобно
	// раздавать единый внешний домен, скрывая адрес панели.
	SubscriptionDomain string `json:"subscription_domain"`
	// Contact — пользовательские контакты бота (группа / поддержка / соглашение).
	// Все поля задаются админом. Дефолтов нет: пустое = блок скрыт.
	Contact ContactConfig `json:"contact"`
	// Plan — общие параметры подписки, применяются ко всем создаваемым ботом
	// юзерам Remnawave (internal squads — мульти, external — одиночный).
	Plan SubscriptionPlan `json:"plan"`
	// Trial — настройки бесплатного триала (отдельно от платных тарифов).
	// Если Enabled, новый пользователь видит на главной кнопку «🎁 Триал» —
	// доступную один раз (users.trial_used_at).
	Trial TrialConfig `json:"trial"`
}

// TrialConfig — параметры триала, выдаваемого без оплаты.
//   - Days: срок в днях (1..30). 0/нет = «не задан», кнопка не показывается.
//   - TrafficGB: лимит трафика на триал (0 = безлимит).
//   - DeviceLimit: HWID-override для триала (0 = дефолт панели).
//   - InternalSquads / ExternalSquadUUID: куда положить триальную подписку
//     (может отличаться от платных — например, отдельные ноды).
type TrialConfig struct {
	Enabled           bool     `json:"enabled"`
	Days              int      `json:"days"`
	TrafficGB         int      `json:"traffic_gb"`
	DeviceLimit       int      `json:"device_limit"`
	InternalSquads    []string `json:"internal_squads"`
	ExternalSquadUUID string   `json:"external_squad_uuid"`
}

// SubscriptionPlan — то, что бот передаёт в панель при создании/продлении.
//   - ActiveInternalSquads: набор UUID internal-сквадов (юзер пойдёт в эти ноды).
//   - ExternalSquadUUID: один UUID external-сквада (если задан).
//
// Если массив пуст / UUID пуст — соответствующее поле не передаём.
type SubscriptionPlan struct {
	ActiveInternalSquads []string `json:"active_internal_squads"`
	ExternalSquadUUID    string   `json:"external_squad_uuid"`
}

// ContactConfig — то, что показывается пользователю «о боте»:
//   - GroupURL: ссылка на канал/чат (кнопка «👥 Группа» на главной).
//   - SupportURL: ссылка на чат поддержки или t.me/<админа>.
//   - TermsText: пользовательское соглашение (HTML); показывается ОДИН раз
//     перед первой покупкой, после нажатия «✅ Принимаю» больше не выводится.
//     Если TermsText пустой — соглашение не запрашивается.
type ContactConfig struct {
	GroupURL   string `json:"group_url"`
	SupportURL string `json:"support_url"`
	TermsText  string `json:"terms_text"`
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

// Способы оплаты (для лога и единого финализатора).
const (
	PayMethodP2P       = "p2p"
	PayMethodStars     = "stars"
	PayMethodYooKassa  = "yookassa"
	PayMethodCryptoBot = "cryptobot"
)

// Статусы записи в логе оплат.
const (
	PaymentPaid     = "paid"
	PaymentRejected = "rejected"
)

// StarsConfig — оплата через Telegram Stars (валюта XTR, без внешнего мерчанта).
type StarsConfig struct {
	Enabled bool        `json:"enabled"`
	Prices  map[int]int `json:"prices"` // месяцы(1/3/6/12) -> цена в звёздах
}

// YooKassaConfig — оплата картами РФ через ЮKassa (api.yookassa.ru).
// Подтверждение оплаты — опросом статуса по кнопке (без входящих вебхуков).
type YooKassaConfig struct {
	Enabled   bool           `json:"enabled"`
	ShopID    string         `json:"shop_id"`
	SecretKey string         `json:"secret_key"`
	ReturnURL string         `json:"return_url"` // куда вернуть после оплаты, напр. https://t.me/<bot>
	Currency  string         `json:"currency"`   // обычно RUB
	Prices    map[int]string `json:"prices"`     // месяцы -> сумма, напр. "150.00"
}

// Payment — запись в логе оплат/действий (видна админу).
type Payment struct {
	ID         int64
	TelegramID int64
	Method     string // p2p | stars | yookassa
	Months     int
	Amount     string // человекочитаемая сумма, напр. "150 руб" или "100 ⭐"
	Status     string // paid | rejected
	Comment    string // напр. причина отказа
	ExtID      string // id платежа у внешнего провайдера (для идемпотентности)
	CreatedAt  string
}

// PendingInvoice — выставленный, но ещё не подтверждённый инвойс (YooKassa /
// CryptoBot). Рабочий список реконсилятора: если вебхук провайдера не дошёл,
// фоновый проход перепроверит статус и добьёт выдачу. P2P/Stars сюда не пишем
// (P2P подтверждает админ вручную, Stars приходит апдейтом бота).
type PendingInvoice struct {
	ID         int64
	Method     string
	ExtID      string
	TelegramID int64
	Months     int
	CreatedAt  string
	Resolved   bool
}

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
	TelegramID      int64
	Username        string // @username из Telegram (без @), может быть пустым
	FirstName       string // имя из Telegram, может быть пустым
	P2PApproved     bool
	Blocked         bool
	CreatedAt       string
	TermsAcceptedAt string // ISO-время принятия пользовательского соглашения; пусто = не принимал
	TrialUsedAt     string // ISO-время активации триала; пусто = ещё не использовал
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

// WebhookConfig — параметры HTTP-сервера для приёма входящих вебхуков от
// платёжных провайдеров (YooKassa, CryptoBot) и панели Remnawave
// (user.expired, user.created, …). Без публичного URL вебхуки не работают —
// бот будет жить на polling-fallback'е. PublicBaseURL заполняет админ из UI
// и должен указывать на ВНЕШНИЙ адрес reverse-proxy перед ботом
// (например https://bot.example.com), без завершающего слэша.
type WebhookConfig struct {
	Enabled         bool   `json:"enabled"`          // запускать HTTP-сервер?
	ListenAddr      string `json:"listen_addr"`      // host:port, по умолчанию ":8080"
	PublicBaseURL   string `json:"public_base_url"`  // напр. https://bot.example.com
	RemnawaveSecret string `json:"remnawave_secret"` // WEBHOOK_SECRET_HEADER из панели
}

// CryptoBotConfig — настройки оплаты через @CryptoBot (Crypto Pay API).
// Цены задаются в USD (внутренний прайс CryptoPay), он сам конвертирует
// в TON/BTC/USDT при оплате. WebhookSecret — он же API-токен (CryptoPay
// подписывает входящие вебхуки HMAC-SHA256 по SHA256(token)).
type CryptoBotConfig struct {
	Enabled  bool           `json:"enabled"`
	Token    string         `json:"token"`    // X-Crypto-Pay-API-Token
	Currency string         `json:"currency"` // обычно USD
	Asset    string         `json:"asset"`    // конкретный актив для инвойсов: USDT|TON|BTC|...
	Prices   map[int]string `json:"prices"`   // месяцы -> цена в Currency, напр. "1.99"
}

// RemindersConfig — авто-напоминания об истечении подписки. Тикер бота
// раз в час проверяет панель и пушит юзеру кнопку «Продлить» за N дней до
// expireAt. Дни хранятся как отсортированный список (3,1,0 — значит за
// 3 дня, за 1 день, в день истечения).
type RemindersConfig struct {
	Enabled  bool  `json:"enabled"`
	DaysList []int `json:"days_list"` // напр. [3,1,0]
}

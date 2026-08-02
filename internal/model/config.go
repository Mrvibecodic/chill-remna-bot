package model

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	DBSQLite   = "sqlite"
	DBPostgres = "postgres"
)

const (
	ModeLocal  = "local"
	ModeRemote = "remote"
)

const (
	InstallDocs   = "docs"
	InstallEGames = "egames"
)

const (
	LangRU = "ru"
	LangEN = "en"
)

// Режимы публичности бота.
const (
	// AccessPublic — вход свободный: любой, кто открыл бота, регистрируется.
	AccessPublic = "public"
	// AccessInvite — вход только по одноразовой ссылке-приглашению
	// (t.me/<bot>?start=inv_<код>), которую генерит админ.
	AccessInvite = "invite"
	// AccessWhitelist — вход только тем, кого админ добавил в белый список.
	AccessWhitelist = "whitelist"
)

// NormalizeAccess приводит режим публичности к валидному значению и держит
// legacy-флаг WhitelistMode в синхроне с новым AccessMode.
//
// Legacy-флаг трактуется как «бот закрыт» (режим не публичный) — так откат на
// старую версию бота, которая знает только WhitelistMode, оставляет бота
// ЗАКРЫТЫМ, а не открывает его всем: приглашённые уже помечены whitelisted и
// проходят и по старой логике. Рассинхрон значений возможен только если конфиг
// писала старая версия — тогда верим её флагу.
func (c *BotConfig) NormalizeAccess() {
	valid := c.AccessMode == AccessPublic || c.AccessMode == AccessInvite || c.AccessMode == AccessWhitelist
	closed := c.AccessMode == AccessWhitelist || c.AccessMode == AccessInvite
	if !valid || c.WhitelistMode != closed {
		switch {
		case c.WhitelistMode && (!valid || c.AccessMode == AccessPublic):
			c.AccessMode = AccessWhitelist
		case !c.WhitelistMode:
			c.AccessMode = AccessPublic
		}
	}
	c.WhitelistMode = c.AccessMode != AccessPublic
}

// AccessClosed сообщает, ограничен ли вход в бота (любой режим кроме
// публичного).
func (c *BotConfig) AccessClosed() bool {
	return c.AccessMode == AccessWhitelist || c.AccessMode == AccessInvite
}

// Invite — одноразовая (или многоразовая) ссылка-приглашение в бота.
// Срок жизни и число регистраций задаёт админ при создании.
type Invite struct {
	Code      string
	MaxUses   int
	Used      int
	ExpiresAt string
	CreatedAt string
	Revoked   bool
	Note      string
}

// Active сообщает, можно ли ещё активировать приглашение на момент now
// (RFC3339-время в UTC).
func (i *Invite) Active(now time.Time) bool {
	if i == nil || i.Revoked {
		return false
	}
	if i.MaxUses > 0 && i.Used >= i.MaxUses {
		return false
	}
	if i.ExpiresAt != "" {
		exp, err := time.Parse(time.RFC3339, i.ExpiresAt)
		if err == nil && !exp.After(now) {
			return false
		}
	}
	return true
}

// AutoPay — подключённое автосписание: сохранённый в ЮKassa способ оплаты,
// которым бот сам продлевает подписку пользователя.
type AutoPay struct {
	TelegramID int64
	Method     string
	MethodID   string
	Title      string
	Months     int
	Amount     string
	Currency   string
	Enabled    bool
	CreatedAt  string
	LastPayAt  string
	NextTryAt  string
	Fails      int
	LastError  string
}

type PanelConfig struct {
	Mode        string `json:"mode"`
	InstallType string `json:"install_type"`
	BaseURL     string `json:"base_url"`
	APIToken    string `json:"api_token"`
	Cookie      string `json:"cookie"`
	APIKey      string `json:"api_key"`
}

type BotConfig struct {
	Installed bool            `json:"installed"`
	Language  string          `json:"language"`
	DBKind    string          `json:"db_kind"`
	Panel     PanelConfig     `json:"panel"`
	P2P       P2PConfig       `json:"p2p"`
	Stars     StarsConfig     `json:"stars"`
	YooKassa  YooKassaConfig  `json:"yookassa"`
	CryptoBot CryptoBotConfig `json:"cryptobot"`
	Platega   PlategaConfig   `json:"platega"`
	Tribute   TributeConfig   `json:"tribute"`
	Webhook   WebhookConfig   `json:"webhook"`
	Reminders RemindersConfig `json:"reminders"`
	Referral  ReferralConfig  `json:"referral"`
	MoyNalog  MoyNalogConfig  `json:"moynalog"`

	// WhitelistMode — legacy-флаг «вайтлист включён». Оставлен ради обратной
	// совместимости со старыми конфигами и старым UI; актуальное состояние
	// хранится в AccessMode, NormalizeAccess синхронизирует их в обе стороны.
	WhitelistMode bool `json:"whitelist_mode"`
	// AccessMode — режим публичности бота: public / invite / whitelist.
	AccessMode string        `json:"access_mode"`
	Pricing    Pricing       `json:"pricing"`
	Welcome    WelcomeConfig `json:"welcome"`

	PremiumEmoji map[string]string `json:"premium_emoji"`

	SubscriptionDomain string `json:"subscription_domain"`

	Contact ContactConfig `json:"contact"`

	Plan SubscriptionPlan `json:"plan"`

	Trial TrialConfig `json:"trial"`

	UpdateCheck UpdateCheckConfig `json:"update_check"`

	AddSub AddSubConfig `json:"addsub"`

	MiniApp MiniAppConfig `json:"miniapp"`

	Cabinet CabinetConfig `json:"cabinet"`
}

type UpdateCheckConfig struct {
	Enabled    bool   `json:"enabled"`
	Hour       int    `json:"hour"`
	LastSeenAt string `json:"last_seen_sha"`
	Init       bool   `json:"init"`
	// Channel selects the update channel / git branch: "stable" (main) or "dev".
	Channel string `json:"channel"`
	// ChannelChosen is false until the admin has explicitly picked a channel.
	// Used for the transitional migration that obliges a choice after update.
	ChannelChosen bool `json:"channel_chosen"`
}

func (c *BotConfig) NormalizeUpdateCheck() {
	u := &c.UpdateCheck
	if !u.Init {
		u.Enabled = true
		u.Hour = 12
		u.Init = true
	}
	if u.Hour < 0 || u.Hour > 23 {
		u.Hour = 12
	}
	if u.Channel != "dev" && u.Channel != "stable" {
		u.Channel = "stable"
	}
}

// AddSubConfig configures the optional second ("add-on") panel subscription B.
// Only squads and traffic are configurable; expiry, reset strategy and device
// limit are inherited from the main subscription A at sync time.
type AddSubConfig struct {
	Enabled        bool     `json:"enabled"`
	UsernameSuffix string   `json:"username_suffix"`
	TrafficGB      int      `json:"traffic_gb"`
	InternalSquads []string `json:"internal_squads"`
	Init           bool     `json:"init"`
}

func (c *BotConfig) NormalizeAddSub() {
	a := &c.AddSub
	if !a.Init {
		a.Enabled = false
		a.UsernameSuffix = "_addsub"
		a.Init = true
	}
	if a.UsernameSuffix == "" {
		a.UsernameSuffix = "_addsub"
	}
}

type TrialConfig struct {
	Enabled           bool     `json:"enabled"`
	Days              int      `json:"days"`
	TrafficGB         int      `json:"traffic_gb"`
	DeviceLimit       int      `json:"device_limit"`
	InternalSquads    []string `json:"internal_squads"`
	ExternalSquadUUID string   `json:"external_squad_uuid"`
}

type SubscriptionPlan struct {
	ActiveInternalSquads []string `json:"active_internal_squads"`
	ExternalSquadUUID    string   `json:"external_squad_uuid"`
}

type ContactConfig struct {
	GroupURL   string `json:"group_url"`
	SupportURL string `json:"support_url"`
	TermsText  string `json:"terms_text"`
}

type WelcomeConfig struct {
	ImageFileID string          `json:"image_file_id"`
	ImageURL    string          `json:"image_url"`
	Text        string          `json:"text"`
	Entities    json.RawMessage `json:"entities"`
}

var PlanMonths = []int{1, 3, 6, 12}

const (
	P2PAwaiting  = "awaiting"
	P2PSubmitted = "submitted"
	P2PApproved  = "approved"
	P2PRejected  = "rejected"
)

const (
	PayMethodP2P       = "p2p"
	PayMethodStars     = "stars"
	PayMethodYooKassa  = "yookassa"
	PayMethodCryptoBot = "cryptobot"
	PayMethodPlatega   = "platega"
	PayMethodTribute   = "tribute"
	PayMethodBalance   = "balance"
)

const (
	PaymentPaid     = "paid"
	PaymentRejected = "rejected"
)

type StarsConfig struct {
	Enabled bool        `json:"enabled"`
	Prices  map[int]int `json:"prices"`
}

type YooKassaConfig struct {
	Enabled   bool           `json:"enabled"`
	ShopID    string         `json:"shop_id"`
	SecretKey string         `json:"secret_key"`
	ReturnURL string         `json:"return_url"`
	Currency  string         `json:"currency"`
	Prices    map[int]string `json:"prices"`
	// AutoPay включает автопродление: при оплате бот просит ЮKassa сохранить
	// способ оплаты, а потом сам списывает деньги перед окончанием подписки.
	// Требует, чтобы в личном кабинете ЮKassa магазину были включены
	// автоплатежи (рекуррентные платежи).
	AutoPay bool `json:"autopay"`
	// AutoPayDays — за сколько дней до конца подписки списывать (0 = в день
	// окончания). Нормализуется в диапазон 0..14.
	AutoPayDays int `json:"autopay_days"`
}

// NormalizeYooKassa приводит настройки автосписания к валидным значениям.
func (c *BotConfig) NormalizeYooKassa() {
	y := &c.YooKassa
	if y.AutoPayDays < 0 {
		y.AutoPayDays = 0
	}
	if y.AutoPayDays > 14 {
		y.AutoPayDays = 14
	}
}

// AutoPayMaxFails — после скольких неудачных попыток подряд автосписание
// выключается само (пользователю приходит уведомление).
const AutoPayMaxFails = 3

type Payment struct {
	ID         int64
	TelegramID int64
	Method     string
	Months     int
	Amount     string
	Status     string
	Comment    string
	ExtID      string
	CreatedAt  string
}

type PendingInvoice struct {
	ID         int64
	Method     string
	ExtID      string
	TelegramID int64
	Months     int
	CreatedAt  string
	Resolved   bool

	Purpose string

	Kopecks int64
}

type P2PConfig struct {
	Enabled bool `json:"enabled"`
	// OpenForAll делает перевод обычным способом оплаты: реквизиты выдаются
	// всем сразу, без ручного одобрения каждого пользователя админом.
	// Скриншот и подтверждение платежа админом при этом остаются.
	OpenForAll bool           `json:"open_for_all"`
	Cards      []string       `json:"cards"`
	Rotate     bool           `json:"rotate"`
	RotateIdx  int            `json:"rotate_idx"`
	Prices     map[int]string `json:"prices"`
	Currency   string         `json:"currency"`
	SquadUUID  string         `json:"squad_uuid"`
}

type User struct {
	TelegramID      int64
	Username        string
	FirstName       string
	P2PApproved     bool
	Blocked         bool
	CreatedAt       string
	TermsAcceptedAt string
	TrialUsedAt     string

	SubExpireAt string

	NotifyKind string

	NotifySent string

	Balance int64

	ReferredBy   int64
	RefBonusPaid bool
	RefEarned    int64
	Whitelisted  bool
	WebApproved  bool
	// WebDenied: админ явно отклонил заявку на вход в веб-кабинет. Пока флаг
	// стоит, повторные заявки админу не шлются, а юзер при попытке входа видит
	// «доступ отклонён». Снимается ручным одобрением (adm:wok).
	WebDenied bool
}

type P2PRequest struct {
	ID         int64
	TelegramID int64
	Months     int
	Price      string
	Status     string
	Screenshot string
	Comment    string
	CreatedAt  string
	DecidedAt  string
}

type WebhookConfig struct {
	Enabled         bool   `json:"enabled"`
	ListenAddr      string `json:"listen_addr"`
	PublicBaseURL   string `json:"public_base_url"`
	RemnawaveSecret string `json:"remnawave_secret"`
	Domain          string `json:"domain"`
	TLS             bool   `json:"tls"`
}

type CryptoBotConfig struct {
	Enabled  bool   `json:"enabled"`
	Token    string `json:"token"`
	Currency string `json:"currency"`
	Asset    string `json:"asset"`
}

type PayLogEntry struct {
	ID         int64
	ExtID      string
	TelegramID int64
	Method     string
	Stage      string
	Detail     string
	CreatedAt  string
}

type RemindersConfig struct {
	Enabled         bool  `json:"enabled"`
	DaysList        []int `json:"days_list"`
	TrialEnabled    bool  `json:"trial_enabled"`
	TrialDaysBefore int   `json:"trial_days_before"`
	Init            bool  `json:"init"`
}

func (c *BotConfig) NormalizeReminders() {
	r := &c.Reminders
	if r.Init {
		return
	}
	r.Enabled = true
	r.DaysList = []int{3, 1}
	r.TrialEnabled = true
	r.TrialDaysBefore = 1
	r.Init = true
}

var ReminderWindows = []int{7, 3, 1}

func (r RemindersConfig) HasReminderDay(d int) bool {
	for _, x := range r.DaysList {
		if x == d {
			return true
		}
	}
	return false
}

type ReferralConfig struct {
	Enabled    bool   `json:"enabled"`
	BonusKind  string `json:"bonus_kind"`
	BonusValue int    `json:"bonus_value"`
	OnFirstPay bool   `json:"on_first_pay"`
	// InviteeKind/InviteeValue: a welcome bonus for the invited friend
	// ("" = off, balance, days). Paid together with the referrer bonus (once).
	InviteeKind  string `json:"invitee_kind"`
	InviteeValue int    `json:"invitee_value"`
	// Percent: share of every invitee payment credited to the referrer balance.
	Percent int  `json:"percent"`
	Init    bool `json:"init"`
}

func (c *BotConfig) NormalizeReferral() {
	r := &c.Referral
	if !r.Init {
		r.Enabled = false
		r.BonusKind = ReferralBonusBalance
		r.BonusValue = 50
		r.OnFirstPay = true
		r.Init = true
	}
	if r.BonusKind != ReferralBonusBalance && r.BonusKind != ReferralBonusDays {
		r.BonusKind = ReferralBonusBalance
	}
	if r.BonusValue < 0 {
		r.BonusValue = 0
	}
	if r.InviteeKind != ReferralBonusBalance && r.InviteeKind != ReferralBonusDays {
		r.InviteeKind = ""
	}
	if r.InviteeValue < 0 {
		r.InviteeValue = 0
	}
	if r.Percent < 0 {
		r.Percent = 0
	}
	if r.Percent > 100 {
		r.Percent = 100
	}
}

const (
	ReferralBonusBalance = "balance"
	ReferralBonusDays    = "days"
)

type PromoCode struct {
	Code      string
	Kind      string
	Value     int
	MaxUses   int
	Used      int
	ExpiresAt string
	CreatedAt string
}

const (
	PromoKindBalance = "balance"
	PromoKindDays    = "days"
)

type MoyNalogConfig struct {
	Enabled     bool   `json:"enabled"`
	Login       string `json:"login"`
	Password    string `json:"password"`
	ServiceName string `json:"service_name"`
}

type PlategaConfig struct {
	Enabled    bool   `json:"enabled"`
	MerchantID string `json:"merchant_id"`
	Secret     string `json:"secret"`
	Method     int    `json:"method"`
	ReturnURL  string `json:"return_url"`
}

type TributeConfig struct {
	Enabled bool   `json:"enabled"`
	APIKey  string `json:"api_key"`
	PayURL  string `json:"pay_url"`
}

// MiniAppConfig toggles the Telegram Mini App / web cabinet. Disabled by
// default; when off, the /api/miniapp/* routes and static app are not served.
type MiniAppConfig struct {
	Enabled bool `json:"enabled"`
	Init    bool `json:"init"`
}

func (c *BotConfig) NormalizeMiniApp() {
	if !c.MiniApp.Init {
		c.MiniApp.Enabled = false
		c.MiniApp.Init = true
	}
}

// CabinetConfig toggles the web cabinet (a browser site, not inside Telegram)
// and the URL path it is served at. Disabled by default.
type CabinetConfig struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
	// Approval gates new web sign-ins requiring admin approval:
	// "" / "off" (none), "tg", "email", "all".
	Approval string `json:"approval"`
	// Branding / privacy: page <title>, meta description, favicon URL, and an
	// anti-fingerprint mode (randomize markers so the cabinet is harder to
	// identify as this bot).
	Title   string `json:"title"`
	Desc    string `json:"desc"`
	Favicon string `json:"favicon"`
	AntiFP  bool   `json:"anti_fp"`
	Init    bool   `json:"init"`
}

// Cabinet approval modes.
const (
	CabinetApprovalOff   = "off"
	CabinetApprovalTG    = "tg"
	CabinetApprovalEmail = "email"
	CabinetApprovalAll   = "all"
)

func (c *BotConfig) NormalizeCabinet() {
	cab := &c.Cabinet
	if !cab.Init {
		cab.Enabled = false
		cab.Path = "/cabinet/"
		cab.Init = true
	}
	p := strings.TrimSpace(cab.Path)
	if p == "" {
		p = "/cabinet/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	cab.Path = p
	switch cab.Approval {
	case CabinetApprovalTG, CabinetApprovalEmail, CabinetApprovalAll:
	default:
		cab.Approval = CabinetApprovalOff
	}
}

// WebUser is an email+password account for the web cabinet. TgID is a synthetic
// negative identity that maps the account into the bot's telegram-id-keyed
// system (so it can buy/manage like any user); it never collides with a real
// Telegram id (those are positive).
type WebUser struct {
	TgID      int64
	Email     string
	PassHash  string
	CreatedAt string
}

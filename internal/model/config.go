package model

import "encoding/json"

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

	WhitelistMode bool          `json:"whitelist_mode"`
	Pricing       Pricing       `json:"pricing"`
	Welcome       WelcomeConfig `json:"welcome"`

	PremiumEmoji map[string]string `json:"premium_emoji"`

	SubscriptionDomain string `json:"subscription_domain"`

	Contact ContactConfig `json:"contact"`

	Plan SubscriptionPlan `json:"plan"`

	Trial TrialConfig `json:"trial"`

	UpdateCheck UpdateCheckConfig `json:"update_check"`
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
}

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
	Enabled   bool           `json:"enabled"`
	Cards     []string       `json:"cards"`
	Rotate    bool           `json:"rotate"`
	RotateIdx int            `json:"rotate_idx"`
	Prices    map[int]string `json:"prices"`
	Currency  string         `json:"currency"`
	SquadUUID string         `json:"squad_uuid"`
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
	Whitelisted  bool
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
	Init       bool   `json:"init"`
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

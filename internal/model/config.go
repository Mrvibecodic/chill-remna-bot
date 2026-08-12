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
	// PaidPeriod — дата окончания подписки, за продление которой уже списали
	// деньги. Защищает от повторного списания за тот же период, если продление
	// в панели не удалось и срок подписки не сдвинулся.
	PaidPeriod string
	NextTryAt  string
	Fails      int
	LastError  string

	// Snapshot — условия последнего продления. Нужен, чтобы заметить, что
	// тариф изменился, и предупредить человека: списание всегда идёт по
	// действующим условиям, а не по замороженным.
	Snapshot *PlanSnapshot
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
	Heleket   HeleketConfig   `json:"heleket"`
	Tribute   TributeConfig   `json:"tribute"`
	Webhook   WebhookConfig   `json:"webhook"`
	Torrent   TorrentConfig   `json:"torrent"`
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

// Clone возвращает независимую копию конфига.
//
// Нужна на записи в базу. Хранилище получало живой указатель и превращало
// конфиг в JSON уже без замка, а внутри конфига десяток карт, которые админка
// правит под замком: обход карты одновременно с записью в неё убивает процесс
// без всякой возможности перехватить. Копия снимается под замком, дальше в
// хранилище едет она, и записывается ровно то состояние, что было на момент
// снятия.
//
// Круг через JSON выбран намеренно: конфиг и хранится как JSON, то есть круг по
// определению переносит всё, что вообще подлежит сохранению, и не требует
// дописывать копию для каждого нового поля — иначе первое же добавленное поле
// осталось бы поделённым между копиями молча.
func (c *BotConfig) Clone() (*BotConfig, error) {
	if c == nil {
		return nil, nil
	}
	raw, err := c.SnapshotJSON()
	if err != nil {
		return nil, err
	}
	return ConfigFromJSON(raw)
}

// SnapshotJSON и ConfigFromJSON — те же две половины копии, но по отдельности.
// Замок конфига защищает не только конфиг: под ним же лежат хранилище, клиент
// панели и состояния экранов, и берут его на каждом сообщении. Обход карт
// (Marshal) обязан идти под замком, а вот разбор обратно — уже нет, и держать
// на нём общий замок бота незачем.
func (c *BotConfig) SnapshotJSON() ([]byte, error) {
	if c == nil {
		return nil, nil
	}
	return json.Marshal(c)
}

// ConfigFromJSON собирает конфиг из снимка. Пустой снимок — пустой конфиг без
// ошибки: так вызывающему не приходится отличать «конфига нет» от сбоя.
func ConfigFromJSON(raw []byte) (*BotConfig, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out BotConfig
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
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
	// Name и Description — общие название и описание опции для витрины и
	// экранов подписки; тариф может переопределить их своими (Plan.AddSubName,
	// Plan.AddSubDesc). Пустые — стандартный текст.
	//
	// ⚠ Поля живут в конфиге: предыдущий образ бота при сохранении конфига их
	// молча выбрасывает. Потеря косметическая (тексты вводятся заново), ради
	// неё отдельную таблицу не заводим.
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
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
	// Strategy — своя стратегия сброса трафика триала; пусто — как у сетки
	// «Базового» (историческое поведение). ⚠ Поле живёт в конфиге-блобе:
	// старый образ при сохранении конфига его молча выбрасывает — триал
	// вернётся к стратегии сетки. Потеря косметическая.
	Strategy string `json:"strategy,omitempty"`
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
	PayMethodHeleket   = "heleket"
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

	// Snapshot — условия, на которых платёж был проведён (см. PlanSnapshot).
	Snapshot *PlanSnapshot
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

	Snapshot *PlanSnapshot
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

	// Snapshot — условия действующей подписки. Локальной сущности подписки у
	// бота нет, истина в панели; снимок нужен, чтобы знать, что именно
	// продано, и уметь это восстановить.
	Snapshot *PlanSnapshot
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

	Snapshot *PlanSnapshot
}

type WebhookConfig struct {
	Enabled         bool   `json:"enabled"`
	ListenAddr      string `json:"listen_addr"`
	PublicBaseURL   string `json:"public_base_url"`
	RemnawaveSecret string `json:"remnawave_secret"`
	Domain          string `json:"domain"`
	TLS             bool   `json:"tls"`
}

// TorrentConfig — реакция бота на отчёты торрент-блокера панели
// (вебхук torrent_blocker.report плагина ноды).
type TorrentConfig struct {
	NotifyAdmin bool `json:"notify_admin"`
	NotifyUser  bool `json:"notify_user"`
	// UnblockText/UnblockEntities — заданный админом текст сообщения о снятии
	// блокировки (с телеграмным форматированием 1-в-1, как у приветствия).
	// Пустой текст = стандартное сообщение из i18n.
	UnblockText     string          `json:"unblock_text"`
	UnblockEntities json.RawMessage `json:"unblock_entities"`
	Init            bool            `json:"init"`
	// StrikeLimit — сколько нарушений за 30 дней автоматически отключают
	// подписку. 0 — политика выключена (значение по умолчанию: отключать
	// платящего человека без ведома владельца бот сам не должен).
	StrikeLimit int `json:"strike_limit"`
}

// NormalizeTorrent включает оба уведомления по умолчанию для конфигов,
// созданных до появления настройки.
func (c *BotConfig) NormalizeTorrent() {
	t := &c.Torrent
	if t.Init {
		return
	}
	t.NotifyAdmin = true
	t.NotifyUser = true
	t.Init = true
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

// TorrentReport — запись журнала торрент-блокера (одна на каждый отчёт
// панели torrent_blocker.report).
type TorrentReport struct {
	ID              int64
	TelegramID      int64
	Username        string
	Node            string
	IP              string
	Protocol        string
	Inbound         string
	Source          string
	Destination     string
	BlockSeconds    int
	WillUnblockAt   string
	UnblockNotified bool
	CreatedAt       string
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

// HeleketConfig — крипто-шлюз Heleket. Счёт выставляется в валюте прайса
// (₽), клиент выбирает криптовалюту и сеть уже на странице оплаты.
type HeleketConfig struct {
	Enabled    bool   `json:"enabled"`
	MerchantID string `json:"merchant_id"`
	// APIKey — ПЛАТЁЖНЫЙ ключ мерчанта. У выплат в Heleket ключ отдельный,
	// перепутанный не пройдёт ни в запросах, ни при проверке вебхука.
	APIKey string `json:"api_key"`
	// ToCurrency — криптовалюта, в которую Heleket конвертирует полученные
	// средства (например USDT — защита от волатильности). Пусто — как настроено
	// в личном кабинете мерчанта.
	ToCurrency string `json:"to_currency"`
	// Subtract — какой процент комиссии сети платит клиент (0..100). Указатель
	// намеренно: у обычного int ноль неотличим от «не задано», и вариант
	// «комиссию платит магазин» молча превратился бы в дефолт.
	Subtract *int `json:"subtract"`
	// Lifetime — срок жизни счёта в секундах (300..43200), 0 — дефолт 3600.
	Lifetime  int    `json:"lifetime"`
	ReturnURL string `json:"return_url"`
}

// Значения по умолчанию для Heleket.
const (
	HeleketDefaultSubtract = 100
	HeleketDefaultLifetime = 3600
	HeleketMinLifetime     = 300
	HeleketMaxLifetime     = 43200
)

// SubtractOrDefault — процент комиссии, который платит клиент.
func (c HeleketConfig) SubtractOrDefault() int {
	if c.Subtract == nil {
		return HeleketDefaultSubtract
	}
	v := *c.Subtract
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// LifetimeOrDefault — срок жизни счёта в пределах, которые принимает Heleket.
func (c HeleketConfig) LifetimeOrDefault() int {
	if c.Lifetime < HeleketMinLifetime || c.Lifetime > HeleketMaxLifetime {
		return HeleketDefaultLifetime
	}
	return c.Lifetime
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

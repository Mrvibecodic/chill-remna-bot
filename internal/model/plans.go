package model

import (
	"encoding/json"
	"strings"
)

// Plan — тариф: набор условий, который бот продаёт. До его появления тарифа как
// объекта не было: цены, лимиты и сквады лежали одной глобальной картой
// model.Pricing, где ключом служило число месяцев. Из-за этого два разных
// предложения на один и тот же срок были невозможны в принципе.
//
// Тарифы живут в ОТДЕЛЬНОЙ таблице, а не в конфиге. Конфиг хранится одним
// зашифрованным JSON-блобом, и предыдущий образ бота молча выбрасывает
// незнакомые поля при любом сохранении — откатились, поправили что-нибудь в
// админке, и тарифы стёрты. Про таблицу старый образ не знает и испортить её не
// может.
type Plan struct {
	// Code — стабильный идентификатор тарифа. Не меняется при переименовании:
	// он уходит в ссылки, callback-данные и метаданные платежей.
	Code string
	Name string
	// Description — описание для витрины.
	Description string
	Icon        string
	// Order — порядок в витрине (меньше — выше).
	Order   int
	Enabled bool

	// Лимиты тарифа. Раньше жили глобально на весь бот (Pricing.DeviceLimit,
	// Pricing.TrafficStrategy) или картой по числу месяцев.
	//
	// TrafficGB: 0 = безлимит (панель снимает ограничение), как и в админке.
	// DeviceLimit: 0 = лимит не задаётся, остаётся дефолт панели.
	TrafficGB   int
	DeviceLimit int
	Strategy    string
	IntSquads   []string
	ExtSquad    string

	// Availability — режим доступности (см. PlanAvail*). Поле заводится сразу,
	// работать по нему бот начнёт на своём этапе; до тех пор у всех тарифов
	// стоит PlanAvailAll и поведение витрины не меняется.
	Availability string

	// AddSub — продаётся ли с тарифом доп-подписка (см. PlanAddSub*). Пустое
	// значение означает «наследовать глобальный переключатель»: так тариф
	// «Базовый» и существующие установки после обновления ведут себя ровно как
	// раньше — опция есть у всех, пока включена глобально.
	AddSub string
	// AddSubName и AddSubDesc — название и описание опции ДЛЯ ЭТОГО тарифа.
	// Пустые — берутся общие (из настроек доп-подписки), а без них — стандартный
	// текст. Пользователь видит опцию только там, где она включена.
	AddSubName string
	AddSubDesc string

	Currency string
	// Durations — длительности тарифа, у каждой свои цены. Пустой список =
	// тариф ничего не продаёт.
	Durations []PlanDuration

	// FromConfig — тариф ведомый от старой сетки цен в конфиге: бот
	// пересобирает его при каждом сохранении конфига. Так живёт «Базовый», пока
	// редактора тарифов нет. Как только тариф правят редактором, флаг снимается
	// — и предыдущая версия бота, где ведомая сетка ещё была источником истины,
	// после отката не затрёт правки редактора своей копией.
	FromConfig bool

	CreatedAt string
	UpdatedAt string
}

// PlanDuration — одна длительность тарифа с ценами по способам оплаты.
//
// Про единицу срока: канонической единицей считаются дни, но длительности,
// заведённые как месяцы, продлеваются КАЛЕНДАРНО — 30 дней не равны месяцу, и
// молчаливая подмена сдвинула бы даты всем действующим подписчикам. Поэтому
// Months и Days существуют одновременно: заполнено ровно одно из двух.
//
// Переопределения лимитов — указатели, а не значения: у обычного int ноль
// неотличим от «не задано», а ноль здесь означает «безлимит». Именно на этом
// раньше горел безлимитный трафик — до панели он не доезжал.
type PlanDuration struct {
	Months int `json:"months,omitempty"`
	Days   int `json:"days,omitempty"`

	// Цены. Base — цена по умолчанию, P2P и YooKassa — переопределения для
	// этих способов (пусто = берётся Base), Stars — цена в звёздах.
	Base     string `json:"base,omitempty"`
	P2P      string `json:"p2p,omitempty"`
	YooKassa string `json:"yookassa,omitempty"`
	Stars    int    `json:"stars,omitempty"`

	TrafficGB   *int      `json:"traffic_gb,omitempty"`
	DeviceLimit *int      `json:"device_limit,omitempty"`
	IntSquads   *[]string `json:"int_squads,omitempty"`
	ExtSquad    *string   `json:"ext_squad,omitempty"`
}

// Режимы доступности тарифа.
const (
	// PlanAvailAll — тариф виден всем.
	PlanAvailAll = "all"
	// PlanAvailNew — только тем, кто ещё ничего не покупал.
	PlanAvailNew = "new"
	// PlanAvailExisting — только действующим подписчикам.
	PlanAvailExisting = "existing"
	// PlanAvailList — только тем, кого админ добавил в список допущенных.
	PlanAvailList = "list"
	// PlanAvailLink — тариф скрыт в витрине и открывается по прямой ссылке.
	PlanAvailLink = "link"
)

// Режимы доп-подписки у тарифа.
const (
	// PlanAddSubInherit — наследовать глобальный переключатель доп-подписки.
	PlanAddSubInherit = ""
	// PlanAddSubOn — опция продаётся с тарифом (при включённой инфраструктуре).
	PlanAddSubOn = "on"
	// PlanAddSubOff — тариф продаётся без опции.
	PlanAddSubOff = "off"
)

// NormalizeAddSubMode приводит режим доп-подписки к валидному значению.
// Неизвестное значение — «наследовать»: поведение как до появления поля.
func NormalizeAddSubMode(mode string) string {
	switch mode {
	case PlanAddSubOn, PlanAddSubOff:
		return mode
	}
	return PlanAddSubInherit
}

// PlanCodeBase — код тарифа, в который переезжает текущая сетка цен.
const PlanCodeBase = "base"

// Границы длины кода тарифа. Верхняя — чтобы код помещался в callback-данные
// Telegram (64 байта на всю строку вместе с префиксом) и в метаданные платежей;
// нижняя — чтобы код по ссылке нельзя было перебрать за разумное время.
const (
	PlanCodeMinLen = 3
	PlanCodeMaxLen = 32
)

// ValidPlanCode проверяет код тарифа. Разрешены латиница, цифры, дефис и
// подчёркивание: код едет в ссылки, callback-данные и метаданные платёжек,
// поэтому ничего экзотического в нём быть не должно.
func ValidPlanCode(code string) bool {
	if len(code) < PlanCodeMinLen || len(code) > PlanCodeMaxLen {
		return false
	}
	for _, r := range code {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// NormalizeAvailability приводит режим доступности к валидному значению.
// Неизвестное значение трактуется как «доступен всем» — так тариф, записанный
// более новой версией бота, после отката остаётся продаваемым, а не пропадает
// из витрины молча.
func NormalizeAvailability(mode string) string {
	switch mode {
	case PlanAvailNew, PlanAvailExisting, PlanAvailList, PlanAvailLink:
		return mode
	default:
		return PlanAvailAll
	}
}

// Normalize приводит тариф к консистентному виду перед сохранением.
func (p *Plan) Normalize() {
	if p == nil {
		return
	}
	p.Code = strings.TrimSpace(p.Code)
	p.Name = strings.TrimSpace(p.Name)
	p.Availability = NormalizeAvailability(p.Availability)
	p.AddSub = NormalizeAddSubMode(p.AddSub)
	if p.TrafficGB < 0 {
		p.TrafficGB = 0
	}
	if p.DeviceLimit < 0 {
		p.DeviceLimit = 0
	}
	p.Strategy = normalizeStrategy(p.Strategy)
	for i := range p.Durations {
		d := &p.Durations[i]
		if d.Months < 0 {
			d.Months = 0
		}
		if d.Days < 0 {
			d.Days = 0
		}
		// Месяцы главнее: длительность, заведённая как месяцы, обязана
		// продлеваться календарно.
		if d.Months > 0 {
			d.Days = 0
		}
		if d.Stars < 0 {
			d.Stars = 0
		}
	}
}

// ValidStrategy — допустимая для панели стратегия сброса трафика.
func ValidStrategy(s string) bool {
	switch s {
	case "NO_RESET", "DAY", "WEEK", "MONTH", "MONTH_ROLLING":
		return true
	}
	return false
}

// normalizeStrategy повторяет проверку Pricing.ResetStrategy: панель принимает
// только этот набор, всё остальное — MONTH.
func normalizeStrategy(s string) string {
	if ValidStrategy(s) {
		return s
	}
	return "MONTH"
}

// Duration возвращает длительность тарифа в календарных месяцах или nil.
func (p *Plan) Duration(months int) *PlanDuration {
	if p == nil || months <= 0 {
		return nil
	}
	for i := range p.Durations {
		if p.Durations[i].Months == months {
			return &p.Durations[i]
		}
	}
	return nil
}

// TrafficGBFor — лимит трафика для длительности: переопределение длительности,
// иначе значение тарифа.
func (p *Plan) TrafficGBFor(d *PlanDuration) int {
	if d != nil && d.TrafficGB != nil {
		return *d.TrafficGB
	}
	if p == nil {
		return 0
	}
	return p.TrafficGB
}

// DeviceLimitFor — лимит устройств для длительности.
func (p *Plan) DeviceLimitFor(d *PlanDuration) int {
	if d != nil && d.DeviceLimit != nil {
		return *d.DeviceLimit
	}
	if p == nil {
		return 0
	}
	return p.DeviceLimit
}

// IntSquadsFor — набор внутренних сквадов для длительности.
func (p *Plan) IntSquadsFor(d *PlanDuration) []string {
	if d != nil && d.IntSquads != nil {
		return append([]string(nil), *d.IntSquads...)
	}
	if p == nil {
		return nil
	}
	return append([]string(nil), p.IntSquads...)
}

// ExtSquadFor — внешний сквад для длительности.
func (p *Plan) ExtSquadFor(d *PlanDuration) string {
	if d != nil && d.ExtSquad != nil {
		return *d.ExtSquad
	}
	if p == nil {
		return ""
	}
	return p.ExtSquad
}

// Fiat — цена длительности для способа оплаты: переопределение способа, иначе
// базовая цена. Повторяет поведение Pricing.Fiat.
func (d *PlanDuration) Fiat(method string) string {
	if d == nil {
		return ""
	}
	switch method {
	case PayMethodP2P:
		if d.P2P != "" {
			return d.P2P
		}
	case PayMethodYooKassa:
		if d.YooKassa != "" {
			return d.YooKassa
		}
	}
	return d.Base
}

// EncodeStrings/DecodeStrings — хранение списков строк в текстовой колонке.
// Пустой список пишется пустой строкой, а не "null": колонки объявлены
// NOT NULL DEFAULT ”.
func EncodeStrings(v []string) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeStrings читает список строк из хранилища.
func DecodeStrings(raw string) []string {
	if raw == "" {
		return nil
	}
	var v []string
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}

// EncodeDurations сериализует длительности тарифа для хранения.
func EncodeDurations(v []PlanDuration) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeDurations читает длительности тарифа из хранилища.
func DecodeDurations(raw string) []PlanDuration {
	if raw == "" {
		return nil
	}
	var v []PlanDuration
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil
	}
	return v
}

// PlanAccess — одна запись списка допущенных к тарифу (режим «по списку»).
// Запись либо про Telegram-аккаунт (TelegramID != 0), либо про e-mail-аккаунт
// кабинета (Email != ""): такие аккаунты живут с синтетическим отрицательным
// Telegram ID, и сопоставлять их можно только по почте.
type PlanAccess struct {
	PlanCode   string
	TelegramID int64
	Email      string
	CreatedAt  string
}

// NormalizeEmail приводит почту к виду, в котором она хранится и сравнивается:
// без пробелов по краям и в нижнем регистре. Тот же вид использует кабинет.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// PurchaseIntent — намерение покупки: что человек выбрал на экране «выбор
// срока», прежде чем перейти к способу оплаты.
//
// Хранится в базе, а не в памяти процесса. Экран с кнопками переживает
// рестарт, поэтому выбор обязан переживать его тоже: иначе выбравший год после
// перезапуска бота получает счёт на месяц — молча, без единого сообщения.
//
// Строка одна на человека и отвечает ровно на один вопрос: «что выбрано
// сейчас». Условия выставленных счетов лежат отдельно (см. таблицу
// invoice_snapshots): счёт из мини-аппа не должен перебивать выбор, сделанный
// в чате, а снятие выбора после покупки — стирать условия ещё не оплаченного
// счёта.
type PurchaseIntent struct {
	TelegramID int64
	PlanCode   string
	Months     int
	Days       int
	CreatedAt  string
}

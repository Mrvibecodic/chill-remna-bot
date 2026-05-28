package model

// Pricing — единый прайс для всех способов оплаты (вынесен отдельно).
// Base — базовая денежная цена за тариф; для конкретного метода её можно
// переопределить (P2P/YooKassa). Stars — цены в звёздах (отдельная единица).
type Pricing struct {
	Currency string         `json:"currency"` // символ/код для денежных методов, напр. "руб"
	Base     map[int]string `json:"base"`     // месяцы(1/3/6/12) -> базовая цена
	P2P      map[int]string `json:"p2p"`      // переопределение для P2P
	YooKassa map[int]string `json:"yookassa"` // переопределение для ЮKassa
	Stars    map[int]int    `json:"stars"`    // цены в звёздах

	// Per-tariff лимиты, передаются в Remnawave при создании/продлении юзера.
	// 0 = безлимит / использовать дефолт панели. RW Shop этого не делает —
	// у нас каждый тариф может иметь свой объём трафика и свой лимит устройств.
	Traffic map[int]int `json:"traffic"` // месяцы -> GB трафика (0 = безлимит)
	Devices map[int]int `json:"devices"` // месяцы -> макс. число HWID-устройств (0 = дефолт панели)

	// TrafficStrategy — стратегия сброса трафика, общая для всех тарифов.
	// Допустимые значения: "NO_RESET" | "DAY" | "WEEK" | "MONTH". Пусто = MONTH.
	TrafficStrategy string `json:"traffic_strategy"`
}

// TrafficBytes возвращает лимит трафика в байтах для тарифа (0 = безлимит).
func (p Pricing) TrafficBytes(months int) int64 {
	gb := int64(p.Traffic[months])
	if gb <= 0 {
		return 0
	}
	return gb * 1024 * 1024 * 1024
}

// DeviceLimit возвращает лимит устройств для тарифа (0 = не выставлять, дефолт панели).
func (p Pricing) DeviceLimit(months int) int { return p.Devices[months] }

// ResetStrategy возвращает безопасное значение для API (MONTH по умолчанию).
func (p Pricing) ResetStrategy() string {
	switch p.TrafficStrategy {
	case "NO_RESET", "DAY", "WEEK", "MONTH":
		return p.TrafficStrategy
	}
	return "MONTH"
}

// Fiat возвращает денежную цену тарифа для метода: сначала переопределение
// метода, иначе базовую цену. Пусто, если цена не задана.
func (p Pricing) Fiat(method string, months int) string {
	var ov map[int]string
	switch method {
	case PayMethodP2P:
		ov = p.P2P
	case PayMethodYooKassa:
		ov = p.YooKassa
	}
	if ov != nil {
		if v, ok := ov[months]; ok && v != "" {
			return v
		}
	}
	return p.Base[months]
}

// StarPrice — цена тарифа в звёздах (0, если не задана).
func (p Pricing) StarPrice(months int) int { return p.Stars[months] }

// NormalizePricing инициализирует карты прайса и однократно переносит legacy-цены
// (P2P.Prices/Stars.Prices/YooKassa.Prices) в единый Pricing при первой загрузке.
func (c *BotConfig) NormalizePricing() {
	p := &c.Pricing
	if p.Base == nil {
		p.Base = map[int]string{}
		for k, v := range c.P2P.Prices {
			p.Base[k] = v
		}
	}
	if p.Currency == "" {
		if c.P2P.Currency != "" {
			p.Currency = c.P2P.Currency
		} else {
			p.Currency = c.YooKassa.Currency
		}
	}
	if p.Stars == nil {
		p.Stars = map[int]int{}
		for k, v := range c.Stars.Prices {
			p.Stars[k] = v
		}
	}
	if p.YooKassa == nil {
		p.YooKassa = map[int]string{}
		for k, v := range c.YooKassa.Prices {
			p.YooKassa[k] = v
		}
	}
	if p.P2P == nil {
		p.P2P = map[int]string{}
	}
	if p.Traffic == nil {
		p.Traffic = map[int]int{}
	}
	if p.Devices == nil {
		p.Devices = map[int]int{}
	}
	if p.TrafficStrategy == "" {
		p.TrafficStrategy = "MONTH"
	}
}

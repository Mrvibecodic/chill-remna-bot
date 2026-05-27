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
}

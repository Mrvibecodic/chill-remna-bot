package model

import "maps"

type Pricing struct {
	Currency string         `json:"currency"`
	Base     map[int]string `json:"base"`
	P2P      map[int]string `json:"p2p"`
	YooKassa map[int]string `json:"yookassa"`
	Stars    map[int]int    `json:"stars"`

	Traffic map[int]int `json:"traffic"`

	Devices map[int]int `json:"devices"`

	SquadsInt map[int][]string `json:"squads_int"`
	SquadsExt map[int]string   `json:"squads_ext"`

	DeviceLimit int `json:"device_limit"`

	TrafficStrategy string `json:"traffic_strategy"`
}

// Clone возвращает сетку цен, не связанную с исходной ни одной картой.
//
// Структура Pricing состоит почти целиком из карт, поэтому обычное присваивание
// копирует только заголовки: копия продолжает делить с оригиналом сами данные.
// Сетка живёт в общем конфиге, её правит админка под замком, а читают её
// продажи, витрина, мини-апп, автосписание и вебхуки — если такое чтение
// уходит из-под замка, получается одновременное чтение и запись карты, а это
// не паника, которую можно перехватить, а немедленная смерть процесса.
// Поэтому всё, что покидает замок, обязано быть копией.
//
// Слайсы сквадов копируются тоже: админка добавляет сквад через append, который
// при запасе ёмкости пишет в тот же массив.
func (p Pricing) Clone() Pricing {
	c := p
	c.Base = maps.Clone(p.Base)
	c.P2P = maps.Clone(p.P2P)
	c.YooKassa = maps.Clone(p.YooKassa)
	c.Stars = maps.Clone(p.Stars)
	c.Traffic = maps.Clone(p.Traffic)
	c.Devices = maps.Clone(p.Devices)
	c.SquadsExt = maps.Clone(p.SquadsExt)
	if p.SquadsInt != nil {
		c.SquadsInt = make(map[int][]string, len(p.SquadsInt))
		for k, v := range p.SquadsInt {
			c.SquadsInt[k] = append([]string(nil), v...)
		}
	}
	return c
}

func (p Pricing) TrafficBytes(months int) int64 {
	gb := int64(p.Traffic[months])
	if gb <= 0 {
		return 0
	}
	return gb * 1024 * 1024 * 1024
}

func (p Pricing) DeviceLimitFor(months int) int {
	if d := p.Devices[months]; d > 0 {
		return d
	}
	return p.DeviceLimit
}

func (p Pricing) ResetStrategy() string {
	switch p.TrafficStrategy {
	case "NO_RESET", "DAY", "WEEK", "MONTH", "MONTH_ROLLING":
		return p.TrafficStrategy
	}
	return "MONTH"
}

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

func (p Pricing) StarPrice(months int) int { return p.Stars[months] }

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
	if p.SquadsInt == nil {
		p.SquadsInt = map[int][]string{}
	}
	if p.SquadsExt == nil {
		p.SquadsExt = map[int]string{}
	}
	if p.TrafficStrategy == "" {
		p.TrafficStrategy = "MONTH"
	}
}

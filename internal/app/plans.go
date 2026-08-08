package app

import (
	"context"

	"remnabot/internal/model"
)

// Тариф «Базовый» — переезд текущей сетки цен в сущность тарифа.
//
// Сетка цен исторически лежит в конфиге одной картой по числу месяцев
// (model.Pricing). Тариф — строка в отдельной таблице. Пока редактор тарифов не
// написан, единственный, кто правит цены, — старая админка цен, то есть конфиг;
// поэтому «Базовый» здесь ВЕДОМЫЙ: он пересобирается из конфига при загрузке и
// после каждого сохранения конфига. Направление перевернётся вместе с
// появлением редактора тарифов: тогда истина переедет в таблицу, а блок цен в
// конфиге станет ведомым — его продолжат заполнять из «Базового», чтобы откат
// на предыдущий образ оставался рабочим.
//
// Оформлять это очередной функцией Normalize* было нельзя: на старте они
// вызываются, но сохранения конфига после них не происходит — миграция
// пересчитывалась бы при каждом запуске, а в базе оставалась бы старая форма.
// Здесь же результат немедленно пишется в таблицу, и отметкой о выполнении
// служит сама строка тарифа.

// basePlanName — имя тарифа при первом создании. Дальше его правит админ, и
// заново мы его не навязываем.
func basePlanName(lang string) string {
	if lang == model.LangEN {
		return "Basic"
	}
	return "Базовый"
}

// basePlanFrom собирает «Базовый» из текущей сетки цен. existing — тариф,
// который уже лежит в базе (nil, если тарифа ещё нет): его оформление (имя,
// описание, значок, порядок, режим доступности, включённость) сохраняется, а
// коммерческая часть — цены, лимиты и сквады — берётся из конфига.
//
// Вызывать под a.mu: функция читает карты конфига, которые админка правит на
// лету.
func basePlanFrom(cfg *model.BotConfig, existing *model.Plan) *model.Plan {
	if cfg == nil {
		return nil
	}
	pr := cfg.Pricing
	p := &model.Plan{
		Code:         model.PlanCodeBase,
		Name:         basePlanName(cfg.Language),
		Availability: model.PlanAvailAll,
		Enabled:      true,
		FromConfig:   true,
	}
	if existing != nil {
		p.Name = existing.Name
		p.Description = existing.Description
		p.Icon = existing.Icon
		p.Order = existing.Order
		p.Availability = existing.Availability
		p.Enabled = existing.Enabled
		p.CreatedAt = existing.CreatedAt
	}

	// Лимиты тарифа — то, что раньше было глобальным на весь бот.
	p.TrafficGB = 0
	p.DeviceLimit = pr.DeviceLimit
	p.Strategy = pr.ResetStrategy()
	p.Currency = pr.Currency

	// Цепочка сквадов повторяет исторический порядок финализации: глобальный
	// набор → одиночный сквад P2P (легаси) → набор, заданный для конкретного
	// срока. Первые два звена — уровень тарифа, третье — переопределение
	// длительности.
	p.IntSquads = append([]string(nil), cfg.Plan.ActiveInternalSquads...)
	p.ExtSquad = cfg.Plan.ExternalSquadUUID
	if len(p.IntSquads) == 0 && cfg.P2P.SquadUUID != "" {
		p.IntSquads = []string{cfg.P2P.SquadUUID}
	}

	for _, mo := range model.PlanMonths {
		d := model.PlanDuration{
			Months:   mo,
			Base:     pr.Base[mo],
			P2P:      pr.P2P[mo],
			YooKassa: pr.YooKassa[mo],
			Stars:    pr.Stars[mo],
		}
		// Длительность без единой цены не продаётся — в витрине её нет, в
		// тарифе тоже быть не должно.
		if d.Base == "" && d.P2P == "" && d.YooKassa == "" && d.Stars <= 0 {
			continue
		}
		// Переопределения длительности заводим только там, где в сетке
		// действительно что-то задано: ноль в старой карте означал «не
		// задано», и превращать его в «безлимит» нельзя.
		if gb := pr.Traffic[mo]; gb > 0 {
			v := gb
			d.TrafficGB = &v
		}
		if dev := pr.Devices[mo]; dev > 0 {
			v := dev
			d.DeviceLimit = &v
		}
		if sq := pr.SquadsInt[mo]; len(sq) > 0 {
			v := append([]string(nil), sq...)
			d.IntSquads = &v
		}
		if e := pr.SquadsExt[mo]; e != "" {
			v := e
			d.ExtSquad = &v
		}
		p.Durations = append(p.Durations, d)
	}
	p.Normalize()
	return p
}

// syncBasePlan держит «Базовый» в соответствии с сеткой цен из конфига.
// Вызывается при загрузке конфига и после каждого его сохранения.
//
// Ошибку возвращает, но вызывающие её только логируют: тариф пока никем не
// читается, и падать из-за него на старте бот не должен.
func (a *App) syncBasePlan(ctx context.Context) error {
	a.mu.Lock()
	cfg, st := a.botCfg, a.store
	a.mu.Unlock()
	if cfg == nil || st == nil {
		return nil
	}
	existing, err := st.GetPlan(ctx, model.PlanCodeBase)
	if err != nil {
		return err
	}
	// Тариф, который уже правили редактором, конфигом не перезаписываем: это
	// ровно та деградация отката, ради которой заведён признак. Иначе
	// откатились на этот образ, сохранили что-нибудь в админке — и правки
	// редактора тарифов затёрты копией из старой сетки.
	if existing != nil && !existing.FromConfig {
		return nil
	}
	a.mu.Lock()
	want := basePlanFrom(a.botCfg, existing)
	a.mu.Unlock()
	if want == nil {
		return nil
	}
	if existing != nil && samePlan(existing, want) {
		return nil
	}
	if existing == nil {
		a.log.Info("тариф «Базовый» создан из текущей сетки цен", "durations", len(want.Durations))
	}
	return st.SavePlan(ctx, want)
}

// samePlan сравнивает содержательную часть тарифов, не трогая отметки времени:
// без этого каждое сохранение конфига писало бы строку заново.
func samePlan(a, b *model.Plan) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Code != b.Code || a.Name != b.Name || a.Description != b.Description ||
		a.Icon != b.Icon || a.Order != b.Order || a.Enabled != b.Enabled ||
		a.TrafficGB != b.TrafficGB || a.DeviceLimit != b.DeviceLimit ||
		a.Strategy != b.Strategy || a.ExtSquad != b.ExtSquad ||
		a.Availability != b.Availability || a.Currency != b.Currency ||
		a.FromConfig != b.FromConfig {
		return false
	}
	if model.EncodeStrings(a.IntSquads) != model.EncodeStrings(b.IntSquads) {
		return false
	}
	return model.EncodeDurations(a.Durations) == model.EncodeDurations(b.Durations)
}

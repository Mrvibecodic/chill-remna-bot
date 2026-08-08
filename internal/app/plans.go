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
	a.rememberBasePlan(existing)
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
	if err := st.SavePlan(ctx, want); err != nil {
		return err
	}
	a.rememberBasePlan(want)
	return nil
}

// rememberBasePlan кладёт тариф в память процесса: снимок условий сделки
// снимается под замком конфига, и ходить оттуда в базу нельзя.
func (a *App) rememberBasePlan(p *model.Plan) {
	if p == nil {
		return
	}
	cp := *p
	a.mu.Lock()
	a.basePlanRef = &cp
	a.mu.Unlock()
}

// basePlanIdent — код и имя тарифа для снимка сделки. Вызывать под a.mu.
func (a *App) basePlanIdentLocked() (code, name string) {
	if a.basePlanRef != nil {
		return a.basePlanRef.Code, a.basePlanRef.Name
	}
	lang := ""
	if a.botCfg != nil {
		lang = a.botCfg.Language
	}
	return model.PlanCodeBase, basePlanName(lang)
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

// Намерение покупки — что человек выбрал на экране «выбор срока».
//
// Раньше выбор жил в памяти процесса (uiState.buyMonths) и терялся при
// рестарте: экран с кнопками рестарт переживает, и выбравший год после
// перезапуска получал счёт на месяц — молча. Теперь выбор пишется в базу и
// оттуда же читается всеми способами оплаты.

// setBuyIntent запоминает выбранный тариф и срок.
func (a *App) setBuyIntent(ctx context.Context, chatID int64, planCode string, months int) error {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return nil
	}
	if planCode == "" {
		planCode = model.PlanCodeBase
	}
	return st.SetPurchaseIntent(ctx, &model.PurchaseIntent{
		TelegramID: chatID,
		PlanCode:   planCode,
		Months:     months,
	})
}

// buyIntent возвращает намерение покупки (nil, если человек ничего не выбирал).
func (a *App) buyIntent(ctx context.Context, chatID int64) *model.PurchaseIntent {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return nil
	}
	in, err := st.PurchaseIntent(ctx, chatID)
	if err != nil {
		a.log.Warn("намерение покупки не прочитано", "err", err, "user", chatID)
		return nil
	}
	return in
}

// buyMonths — выбранный срок в месяцах (0, если выбора нет).
func (a *App) buyMonths(ctx context.Context, chatID int64) int {
	in := a.buyIntent(ctx, chatID)
	if in == nil {
		return 0
	}
	return in.Months
}

// rememberStarsSnapshot кладёт условия сделки в намерение покупки. У Stars нет
// строки счёта в базе, а payload трогать нельзя — намерение остаётся
// единственным местом, где снимок доживёт до подтверждения оплаты.
func (a *App) rememberStarsSnapshot(ctx context.Context, chatID int64, months int, snap *model.PlanSnapshot) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil || snap == nil {
		return
	}
	in, err := st.PurchaseIntent(ctx, chatID)
	if err != nil {
		return
	}
	if in == nil {
		in = &model.PurchaseIntent{TelegramID: chatID, PlanCode: model.PlanCodeBase, Months: months}
	}
	in.Months = months
	in.Snapshot = snap
	if err := st.SetPurchaseIntent(ctx, in); err != nil {
		a.log.Warn("снимок Stars не сохранён", "err", err, "user", chatID)
	}
}

// starsSnapshot достаёт условия сделки, снятые при отправке счёта Stars.
// Снимок берётся только если срок совпал с оплаченным: иначе человек успел
// выбрать другой срок и снимок уже не про эту покупку.
func (a *App) starsSnapshot(ctx context.Context, chatID int64, months int) *model.PlanSnapshot {
	in := a.buyIntent(ctx, chatID)
	if in == nil || in.Snapshot == nil || in.Months != months {
		return nil
	}
	return in.Snapshot
}

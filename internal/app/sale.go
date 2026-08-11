package app

import (
	"context"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Продажа по выбранному тарифу.
//
// Намерение покупки всегда несло код тарифа, но до этого этапа он был всегда
// «base». Теперь экран тарифа по ссылке пишет в намерение любой код, и все
// способы оплаты обязаны считать цену и снимать условия С ТОГО ЖЕ тарифа.
//
// «Базовый» намеренно продолжает продаваться по сетке цен из конфига (Plan ==
// nil): на ней держится совместимость с предыдущим образом бота, и менять её
// источник в этом этапе нельзя. После разворота синхронизации сетка — зеркало
// «Базового», так что расхождения нет.

// sale — что именно продаём: тариф, длительность и срок.
type sale struct {
	// Plan == nil — «Базовый»: цены и лимиты берутся из сетки конфига, как
	// раньше. Не nil — продажа по строке тарифа.
	Plan *model.Plan
	// D — длительность тарифа на Months (для Plan != nil).
	D      *model.PlanDuration
	Months int
}

// planCodeOf — код тарифа из снимка сделки ("" — снимка нет).
func planCodeOf(s *model.PlanSnapshot) string {
	if s == nil {
		return ""
	}
	return s.Code
}

// planCode — код продаваемого тарифа.
func (s *sale) planCode() string {
	if s == nil || s.Plan == nil {
		return model.PlanCodeBase
	}
	return s.Plan.Code
}

// baseSale — продажа «Базового» на months: путь всех старых экранов.
func baseSale(months int) *sale {
	return &sale{Months: months}
}

// saleFor восстанавливает продажу из намерения покупки. nil без ошибки —
// продавать нечего: выбора нет, срок снят с продажи, тариф выключен, удалён
// или недоступен покупателю (вторая точка гейта — создание счёта). Ошибка —
// недоступное хранилище.
func (a *App) saleFor(ctx context.Context, chatID int64) (*sale, error) {
	in, err := a.buyIntent(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if in == nil {
		return nil, nil
	}
	if in.PlanCode == "" || in.PlanCode == model.PlanCodeBase {
		// Проверяем не только «выбор есть», но и «срок ещё продаётся» и «тариф
		// доступен»: админ мог убрать цену или сменить режим доступности, пока
		// экран способов висел в переписке.
		//
		// Гейт здесь — planAccessibleFor, а не baseSaleAllowed: у последнего
		// режим «по ссылке» закрыт (это правило ВИТРИНЫ). Намерение на
		// «Базовый» в режиме «по ссылке» создаётся только его экраном по
		// ссылке — витрина в этом режиме намерений не создаёт вовсе.
		if !a.periodOnSale(in.Months) || !a.planAccessibleFor(ctx, a.basePlanRow(ctx), chatID) {
			return nil, nil
		}
		return baseSale(in.Months), nil
	}
	p, err := a.planByCode(ctx, in.PlanCode)
	if err != nil {
		return nil, err
	}
	// Выключенный тариф не продаётся, каким бы ни был режим доступности: это
	// та же проверка, что и непустая базовая цена у «Базового».
	if p == nil || !p.Enabled {
		return nil, nil
	}
	d := p.Duration(in.Months)
	if d == nil || d.Base == "" {
		return nil, nil
	}
	if !a.planAccessibleFor(ctx, p, chatID) {
		return nil, nil
	}
	return &sale{Plan: p, D: d, Months: in.Months}, nil
}

// saleOrAsk — продажа из намерения; если продавать нечего, показывает витрину
// и возвращает nil. Прямой наследник buyMonthsOrAsk: фолбэков «считаем, что
// месяц» здесь нет и не будет.
func (a *App) saleOrAsk(ctx context.Context, chatID int64) *sale {
	s, err := a.saleFor(ctx, chatID)
	if err != nil {
		// Хранилище недоступно: витрина замкнула бы человека в круг без единого
		// слова о причине. Текст ошибки драйвера в чат не отдаём.
		a.log.Warn("покупка: хранилище недоступно", "err", err, "user", chatID)
		a.sendHome(ctx, chatID, i18n.T(a.lang(chatID), "err.storage"))
		return nil
	}
	if s == nil {
		a.showPlans(ctx, chatID)
		return nil
	}
	return s
}

// saleBase — базовая цена продажи (признак «продаётся» и цена CryptoBot,
// Heleket и оплаты с баланса).
func (a *App) saleBase(s *sale) string {
	if s == nil {
		return ""
	}
	if s.Plan == nil {
		return a.pricing().Base[s.Months]
	}
	return s.D.Base
}

// saleFiat — цена продажи для способа оплаты (переопределение способа, иначе
// базовая).
func (a *App) saleFiat(s *sale, method string) string {
	if s == nil {
		return ""
	}
	if s.Plan == nil {
		return a.pricing().Fiat(method, s.Months)
	}
	return s.D.Fiat(method)
}

// saleStars — цена продажи в звёздах (0 — звёздами не продаётся).
func (a *App) saleStars(s *sale) int {
	if s == nil {
		return 0
	}
	if s.Plan == nil {
		return a.pricing().StarPrice(s.Months)
	}
	return s.D.Stars
}

// saleCurrency — валюта продажи. У тарифа без валюты (импорт из чужого файла)
// берётся валюта сетки: пустая валюта дальше превращалась бы в «RUB» у одних
// способов и в пустой суффикс у других.
func (a *App) saleCurrency(s *sale) string {
	if s == nil || s.Plan == nil || s.Plan.Currency == "" {
		return a.pricing().Currency
	}
	return s.Plan.Currency
}

// saleSnapshot — условия сделки в момент выставления счёта.
func (a *App) saleSnapshot(s *sale) *model.PlanSnapshot {
	if s == nil {
		return nil
	}
	if s.Plan == nil {
		return a.planSnapshot(s.Months)
	}
	return a.planSnapshotOf(s.Plan, s.D, s.Months)
}

// planSnapshotOf снимает условия сделки со строки тарифа — аналог
// planSnapshotLocked, читающего сетку конфига.
func (a *App) planSnapshotOf(p *model.Plan, d *model.PlanDuration, months int) *model.PlanSnapshot {
	sold := a.planAddSubOn(p)
	snap := &model.PlanSnapshot{
		Code:        p.Code,
		Name:        p.Name,
		Months:      months,
		TrafficGB:   p.TrafficGBFor(d),
		DeviceLimit: p.DeviceLimitFor(d),
		Strategy:    p.Strategy,
		IntSquads:   p.IntSquadsFor(d),
		ExtSquad:    p.ExtSquadFor(d),
		// Продана ли доп-подписка — часть условий сделки: продление применяет
		// ровно то, что было продано, а не сегодняшний флаг тарифа.
		AddSub:   &sold,
		Currency: p.Currency,
	}
	if d != nil {
		snap.Price = d.Base
	}
	if snap.Currency == "" {
		snap.Currency = a.pricing().Currency
	}
	return snap
}

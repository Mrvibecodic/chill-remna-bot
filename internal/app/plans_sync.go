package app

import (
	"context"
	"sort"
	"strings"

	"remnabot/internal/model"
)

// Разворот синхронизации: тариф становится ведущим.
//
// До первой коммерческой правки «Базовый» ведомый: цены правит конфиг, тариф
// пересобирается из сетки (syncBasePlan). Первая же правка цены, лимита, сквада,
// валюты или стратегии — с любого экрана — снимает plans.from_config, и с этого
// момента направление обратное: правка идёт В ТАРИФ, а блок цен в конфиге
// заполняется ИЗ него (зеркало). Старый образ бота после отката продаёт по
// конфигу — зеркало держит его актуальным, а флаг не даёт откатившемуся образу
// затереть тариф пересборкой из сетки.
//
// У сетки в конфиге в каждый момент ровно один автор: до снятия флага — старая
// админка, после — зеркало. Двух авторов не бывает, потому что все
// коммерческие сеттеры (и старых экранов, и карточки тарифа) идут через
// editPlanPricing.
//
// Порядок замков: plansMu → cfgSaveMu → mu. Зеркало живёт под plansMu и пишет
// конфиг через saveConfigOnly (cfgSaveMu → mu) — БЕЗ хвоста синхронизации,
// иначе plansMu бралась бы повторно.

// syncPlansConfig держит тариф и сетку в согласии, направление — по флагу.
// Вызывается при загрузке конфига и после каждого его сохранения.
//
// Возвращает true, если зеркало нашло расхождение и переписало конфиг: на
// старте после отката это повод сказать админу, что правки сетки, сделанные
// старым образом, заменены значениями тарифа.
func (a *App) syncPlansConfig(ctx context.Context) (bool, error) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return false, nil
	}
	p, err := st.GetPlan(ctx, model.PlanCodeBase)
	if err != nil {
		return false, err
	}
	if p == nil || p.FromConfig {
		// Тариф ведомый (или его ещё нет) — прежнее направление: конфиг → тариф.
		return false, a.syncBasePlan(ctx)
	}
	a.plansMu.Lock()
	defer a.plansMu.Unlock()
	// Перечитываем под замком: между чтением выше и захватом замка тариф могла
	// править карточка.
	p, err = a.planByCode(ctx, model.PlanCodeBase)
	if err != nil || p == nil {
		return false, err
	}
	a.rememberBasePlan(p)
	return a.mirrorBasePlanLocked(ctx, p)
}

// mirrorBasePlanLocked переписывает блок цен в конфиге значениями тарифа.
// Вызывать под a.plansMu. Пишет в базу только при реальном отличии — зеркало
// висит на каждом сохранении конфига, и без этой проверки каждое сохранение
// удваивалось бы.
func (a *App) mirrorBasePlanLocked(ctx context.Context, p *model.Plan) (bool, error) {
	if p == nil || p.Code != model.PlanCodeBase {
		return false, nil
	}
	a.mu.Lock()
	if a.botCfg == nil {
		a.mu.Unlock()
		return false, nil
	}
	before, berr := a.botCfg.SnapshotJSON()
	applyPlanToConfig(a.botCfg, p)
	after, aerr := a.botCfg.SnapshotJSON()
	a.mu.Unlock()
	if berr == nil && aerr == nil && string(before) == string(after) {
		return false, nil
	}
	if err := a.saveConfigOnly(ctx); err != nil {
		return true, err
	}
	return true, nil
}

// applyPlanToConfig — обратная сторона basePlanFrom: тариф → сетка цен.
//
// Карты сетки пересобираются с нуля: зеркало — единственный автор, и месяц,
// удалённый из тарифа, обязан исчезнуть из сетки (пустая базовая цена снимает
// срок с продажи). Переопределения длительностей без базовой цены переносятся
// тоже — так же, как жила старая сетка: срок снят с продажи, но его настройки
// не пропадают и вернутся вместе с ценой.
func applyPlanToConfig(cfg *model.BotConfig, p *model.Plan) {
	if cfg == nil || p == nil {
		return
	}
	pr := &cfg.Pricing
	pr.Currency = p.Currency
	pr.TrafficStrategy = p.Strategy
	pr.DeviceLimit = p.DeviceLimit
	cfg.Plan.ActiveInternalSquads = append([]string(nil), p.IntSquads...)
	cfg.Plan.ExternalSquadUUID = p.ExtSquad

	pr.Base = map[int]string{}
	pr.P2P = map[int]string{}
	pr.YooKassa = map[int]string{}
	pr.Stars = map[int]int{}
	pr.Traffic = map[int]int{}
	pr.Devices = map[int]int{}
	pr.SquadsInt = map[int][]string{}
	pr.SquadsExt = map[int]string{}

	for i := range p.Durations {
		d := &p.Durations[i]
		mo := d.Months
		if mo <= 0 {
			// Длительности в днях появятся на этапе витрины — в старой сетке им
			// выразиться нечем.
			continue
		}
		if d.Base != "" {
			pr.Base[mo] = d.Base
		}
		if d.P2P != "" {
			pr.P2P[mo] = d.P2P
		}
		if d.YooKassa != "" {
			pr.YooKassa[mo] = d.YooKassa
		}
		if d.Stars > 0 {
			pr.Stars[mo] = d.Stars
		}
		// Ноль в переопределении — «безлимит» и «дефолт», в сетке то же самое
		// значит отсутствие записи.
		if d.TrafficGB != nil && *d.TrafficGB > 0 {
			pr.Traffic[mo] = *d.TrafficGB
		} else if d.TrafficGB == nil && p.TrafficGB > 0 {
			// У «Базового» трафик уровня тарифа не заводится (basePlanFrom
			// держит его нулём), но если он появился — сетка умеет только
			// помесячно.
			pr.Traffic[mo] = p.TrafficGB
		}
		if d.DeviceLimit != nil && *d.DeviceLimit > 0 {
			pr.Devices[mo] = *d.DeviceLimit
		}
		if d.IntSquads != nil && len(*d.IntSquads) > 0 {
			pr.SquadsInt[mo] = append([]string(nil), (*d.IntSquads)...)
		}
		if d.ExtSquad != nil && *d.ExtSquad != "" {
			pr.SquadsExt[mo] = *d.ExtSquad
		}
	}
}

// editPlanPricing — коммерческая правка тарифа: цены, лимиты, сквады, валюта,
// стратегия. От editPlan отличается двумя вещами: у «Базового» снимает
// from_config (тариф становится ведущим) и держит сетку в конфиге зеркалом.
func (a *App) editPlanPricing(ctx context.Context, code string, apply func(*model.Plan) error) error {
	if code == "" {
		code = model.PlanCodeBase
	}
	a.plansMu.Lock()
	defer a.plansMu.Unlock()
	p, err := a.planByCode(ctx, code)
	if err != nil {
		return err
	}
	if p == nil && code == model.PlanCodeBase {
		// «Базового» может не оказаться только из-за сбоя стартовой синхронизации
		// — собираем его из текущей сетки прямо здесь, а не отказываем админу на
		// экране цен непонятным «тариф не найден».
		a.mu.Lock()
		p = basePlanFrom(a.botCfg, nil)
		a.mu.Unlock()
	}
	if p == nil {
		return errPlanGone
	}
	if err := apply(p); err != nil {
		return err
	}
	sortDurations(p)
	if p.Code == model.PlanCodeBase {
		// Первая коммерческая правка делает тариф ведущим. Снятие флага и
		// зеркало — в одной записи: иначе откат между ними оставил бы сетку
		// с чужой ценой.
		p.FromConfig = false
	}
	if err := a.savePlan(ctx, p); err != nil {
		return err
	}
	if p.Code == model.PlanCodeBase {
		if _, err := a.mirrorBasePlanLocked(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func sortDurations(p *model.Plan) {
	sort.SliceStable(p.Durations, func(i, j int) bool {
		di, dj := &p.Durations[i], &p.Durations[j]
		if di.Months != dj.Months {
			return di.Months < dj.Months
		}
		return di.Days < dj.Days
	})
}

// durationFor возвращает длительность на months, создавая её при needCreate.
// Новая длительность появляется без базовой цены: до цены срок не продаётся,
// а заведённые заранее лимиты и сквады дождутся её, как жили в старой сетке.
func durationFor(p *model.Plan, months int, needCreate bool) *model.PlanDuration {
	for i := range p.Durations {
		if p.Durations[i].Months == months {
			return &p.Durations[i]
		}
	}
	if !needCreate {
		return nil
	}
	p.Durations = append(p.Durations, model.PlanDuration{Months: months})
	return &p.Durations[len(p.Durations)-1]
}

// Плановые сеттеры. Все экраны — и старые («Цены и лимиты», Stars, ЮKassa,
// P2P, сквады), и карточка тарифа — правят цены ТОЛЬКО через них: это и есть
// «один автор сетки». code == "" означает «Базовый».

// setPlanPrice ставит цену длительности. kind: base / p2p / yk / stars — по
// способам оплаты. Прочерк снимает значение; у базовой цены это снимает срок с
// продажи (переопределения остаются и вернутся вместе с ценой).
func (a *App) setPlanPrice(ctx context.Context, code string, months int, kind, val string) error {
	if months <= 0 {
		return nil
	}
	val = strings.TrimSpace(val)
	if val == "-" {
		val = ""
	}
	return a.editPlanPricing(ctx, code, func(p *model.Plan) error {
		d := durationFor(p, months, val != "")
		if d == nil {
			return nil
		}
		switch kind {
		case "p2p":
			d.P2P = val
		case "yk":
			d.YooKassa = val
		default:
			d.Base = val
		}
		return nil
	})
}

// setPlanStars ставит цену в звёздах (0 — убрать).
func (a *App) setPlanStars(ctx context.Context, code string, months, stars int) error {
	if months <= 0 {
		return nil
	}
	if stars < 0 {
		stars = 0
	}
	return a.editPlanPricing(ctx, code, func(p *model.Plan) error {
		d := durationFor(p, months, stars > 0)
		if d == nil {
			return nil
		}
		d.Stars = stars
		return nil
	})
}

// setPlanTraffic ставит лимит трафика длительности. 0 = безлимит — в модели
// тарифа это снятое переопределение (у «Базового» трафик уровня тарифа ноль).
func (a *App) setPlanTraffic(ctx context.Context, code string, months, gb int) error {
	if months <= 0 {
		return nil
	}
	return a.editPlanPricing(ctx, code, func(p *model.Plan) error {
		d := durationFor(p, months, gb > 0)
		if d == nil {
			return nil
		}
		if gb > 0 {
			v := gb
			d.TrafficGB = &v
		} else {
			d.TrafficGB = nil
		}
		return nil
	})
}

// setPlanDevices ставит лимит устройств длительности (0 = как у тарифа).
func (a *App) setPlanDevices(ctx context.Context, code string, months, n int) error {
	if months <= 0 {
		return nil
	}
	return a.editPlanPricing(ctx, code, func(p *model.Plan) error {
		d := durationFor(p, months, n > 0)
		if d == nil {
			return nil
		}
		if n > 0 {
			v := n
			d.DeviceLimit = &v
		} else {
			d.DeviceLimit = nil
		}
		return nil
	})
}

// setPlanDeviceLimit ставит лимит устройств тарифа (0 = дефолт панели).
func (a *App) setPlanDeviceLimit(ctx context.Context, code string, n int) error {
	if n < 0 {
		n = 0
	}
	return a.editPlanPricing(ctx, code, func(p *model.Plan) error {
		p.DeviceLimit = n
		return nil
	})
}

// setPlanStrategy ставит стратегию сброса трафика.
func (a *App) setPlanStrategy(ctx context.Context, code, strat string) error {
	return a.editPlanPricing(ctx, code, func(p *model.Plan) error {
		p.Strategy = strat // Normalize приведёт к допустимому набору
		return nil
	})
}

// setPlanCurrency ставит валюту тарифа.
func (a *App) setPlanCurrency(ctx context.Context, code, cur string) error {
	return a.editPlanPricing(ctx, code, func(p *model.Plan) error {
		p.Currency = strings.TrimSpace(cur)
		return nil
	})
}

// togglePlanSquad переключает сквад. months == 0 — набор уровня тарифа
// (глобальные сквады старого экрана), months > 0 — переопределение
// длительности. external выбирает внешний сквад (он один) вместо внутренних.
func (a *App) togglePlanSquad(ctx context.Context, code string, months int, uuid string, external bool) error {
	if uuid == "" {
		return nil
	}
	return a.editPlanPricing(ctx, code, func(p *model.Plan) error {
		if months == 0 {
			if external {
				if p.ExtSquad == uuid {
					p.ExtSquad = ""
				} else {
					p.ExtSquad = uuid
				}
				return nil
			}
			p.IntSquads = toggleString(p.IntSquads, uuid)
			return nil
		}
		d := durationFor(p, months, true)
		if external {
			if d.ExtSquad != nil && *d.ExtSquad == uuid {
				d.ExtSquad = nil
			} else {
				v := uuid
				d.ExtSquad = &v
			}
			return nil
		}
		var cur []string
		if d.IntSquads != nil {
			cur = *d.IntSquads
		}
		next := toggleString(cur, uuid)
		if len(next) == 0 {
			d.IntSquads = nil
		} else {
			d.IntSquads = &next
		}
		return nil
	})
}

// clearPlanSquadOverride снимает переопределение сквадов у длительности —
// действуют наборы уровня тарифа.
func (a *App) clearPlanSquadOverride(ctx context.Context, code string, months int) error {
	if months <= 0 {
		return nil
	}
	return a.editPlanPricing(ctx, code, func(p *model.Plan) error {
		if d := durationFor(p, months, false); d != nil {
			d.IntSquads = nil
			d.ExtSquad = nil
		}
		return nil
	})
}

func toggleString(cur []string, v string) []string {
	for i, u := range cur {
		if u == v {
			return append(append([]string(nil), cur[:i]...), cur[i+1:]...)
		}
	}
	return append(append([]string(nil), cur...), v)
}

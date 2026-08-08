package app

import (
	"context"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

const (
	// Первый проход — вскоре после старта: именно старт нового образа следует
	// за откатом, во время которого лимиты могли разъехаться.
	subRepairFirstDelay = 5 * time.Minute
	subRepairInterval   = 12 * time.Hour
	// Пауза между пользователями: сверка ходит в панель по одному и не должна
	// конкурировать с обычной работой бота.
	subRepairPause = 300 * time.Millisecond
)

// RunSubRepair периодически сверяет выданные в панели лимиты с проданными
// условиями (снимком сделки) и доправляет расхождения.
//
// Зачем: снимок делает сделку самодостаточной, но применяет её тот код, что
// был в момент оплаты. Если платёж провёл предыдущий образ бота (откат на
// старую версию), лимиты пришли из тогдашнего конфига, а не из проданного.
// То же бывает после ручных правок в панели. Сверка превращает «поехало
// молча и навсегда» в «поехало и починилось само».
func (a *App) RunSubRepair(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(subRepairFirstDelay):
	}
	for {
		a.repairSubscriptions(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(subRepairInterval):
		}
	}
}

type subRepairStats struct {
	checked int
	fixed   int
}

func (a *App) repairSubscriptions(ctx context.Context) subRepairStats {
	a.mu.Lock()
	st := a.store
	panel := a.panel
	a.mu.Unlock()
	var stats subRepairStats
	if st == nil || panel == nil {
		return stats
	}
	targets, err := st.ListSubRepairTargets(ctx)
	if err != nil {
		a.log.Warn("сверка лимитов: список", "err", err)
		return stats
	}
	now := time.Now().UTC()
	for i := range targets {
		if ctx.Err() != nil {
			return stats
		}
		t := targets[i]
		// Истёкшая подписка не чинится: лимиты ей всё равно применят при
		// следующей оплате, а лишний PATCH сдвинул бы срок.
		if exp, e := time.Parse(time.RFC3339, t.SubExpireAt); e != nil || !exp.After(now) {
			continue
		}
		stats.checked++
		full := !t.LastPaidHadSnapshot
		if a.repairTarget(ctx, panel, t.TelegramID, t.Snapshot, full) {
			stats.fixed++
			if full && t.LastPaidID != 0 {
				// Условия применены — платёж больше не «непонятного
				// происхождения», иначе сверка ходила бы по нему вечно.
				_ = st.SetPaymentSnapshot(ctx, t.LastPaidID, t.Snapshot)
			}
		}
		select {
		case <-ctx.Done():
			return stats
		case <-time.After(subRepairPause):
		}
	}
	if stats.fixed > 0 {
		a.log.Info("сверка лимитов завершена", "checked", stats.checked, "fixed", stats.fixed)
	}
	return stats
}

// repairTarget возвращает true, если пользователю что-то доправили.
func (a *App) repairTarget(ctx context.Context, panel *remnawave.Client, tgID int64, snap *model.PlanSnapshot, full bool) bool {
	if snap == nil {
		return false
	}
	pu, err := panel.FindByTelegramID(ctx, tgID)
	if err != nil || pu == nil {
		return false
	}
	limits := remnawave.UserLimits{}
	need := false
	reason := ""

	// В обычном режиме сверка только ВОЗВРАЩАЕТ недоданное и никогда не
	// урезает: админ мог осознанно выдать человеку больше, и отбирать это
	// фоновой задачей нельзя.
	if snap.DeviceLimit > 0 && pu.DeviceLimit > 0 && pu.DeviceLimit < snap.DeviceLimit {
		limits.DeviceLimit = snap.DeviceLimit
		need = true
		reason += "устройства "
	}
	if tb := snap.TrafficBytes(); tb > 0 && pu.TrafficLimit > 0 && pu.TrafficLimit < tb {
		limits.TrafficBytes = tb
		need = true
		reason += "трафик "
	}

	// full — подписку последний раз оплачивал образ бота без снимков (то есть
	// предыдущий, во время отката). Тогда лимиты пришли из тогдашнего
	// конфига целиком, включая сквады, которых по панели не видно, — поэтому
	// проданные условия применяются полностью.
	if full {
		limits = remnawave.UserLimits{
			InternalSquads: snap.IntSquads,
			ExternalSquad:  snap.ExtSquad,
			TrafficBytes:   snap.TrafficBytes(),
			DeviceLimit:    snap.DeviceLimit,
			Strategy:       snap.Strategy,
		}
		need = true
		reason = "полная переприменка проданных условий"
	}
	if !need {
		return false
	}

	// days = 0 сохраняет текущий срок: CreateOrUpdateUserDays отсчитывает от
	// действующей даты окончания, если она в будущем. Отдельного «применить
	// только лимиты» в клиенте панели нет, а плодить его ради одного вызова
	// не стоит.
	if _, _, err := panel.CreateOrUpdateUserDays(ctx, tgID, 0, limits); err != nil {
		a.log.Warn("сверка лимитов: применение", "tg_id", tgID, "err", err)
		return false
	}
	a.invalidateSubCache(tgID)
	a.payLog(ctx, "repair", "", tgID, "limits_repaired", "%s(продано: %d устр., %d ГБ)", reason, snap.DeviceLimit, snap.TrafficGB)
	return true
}

package app

import (
	"context"
	"strconv"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
)

const (
	bonusSweepFirstDelay = 2 * time.Minute
	bonusSweepInterval   = time.Hour
	// Потолок на проход и случайный порядок — в SQL: подарок при стратегии
	// NO_RESET живёт до конца подписки, такие записи остаются надолго, и при
	// стабильном порядке первые же двести закрывали бы очередь собой.
	bonusSweepBatch = 200
	bonusSweepPace  = 200 * time.Millisecond
	// Записи давно ушедших людей чистим: подписки нет, потолок применят
	// заново при следующей оплате, а запись стоит запроса в панель каждый
	// час. Срок с запасом — вернувшийся через месяц-другой человек ещё
	// застанет свой подарок.
	bonusStaleAfter = 90 * 24 * time.Hour
)

// RunBonusTrafficSweep забирает разовые подарки трафика обратно.
//
// Панель умеет только потолок НА ПЕРИОД. Чтобы «+50 ГБ» остались разовыми, а
// не повторялись каждый месяц оплаченного года, бот помнит, что именно он
// накинул, и снимает прибавку, как только панель обнулит счётчик расхода.
func (a *App) RunBonusTrafficSweep(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(bonusSweepFirstDelay):
	}
	for {
		a.sweepBonusTrafficOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(bonusSweepInterval):
		}
	}
}

func (a *App) sweepBonusTrafficOnce(ctx context.Context) int {
	a.mu.Lock()
	st := a.store
	panel := a.panel
	a.mu.Unlock()
	if st == nil || panel == nil {
		return 0
	}
	targets, err := st.ListTrafficBonuses(ctx, bonusSweepBatch)
	if err != nil {
		a.log.Warn("подарочный трафик: список", "err", err)
		return 0
	}
	done := 0
	for i, t := range targets {
		if ctx.Err() != nil {
			return done
		}
		if i > 0 && !a.runInline {
			select {
			case <-ctx.Done():
				return done
			case <-time.After(bonusSweepPace):
			}
		}
		if a.sweepBonusOne(ctx, st, panel, t) {
			done++
		}
	}
	return done
}

// sweepBonusOne возвращает true, если запись о подарке закрыта — неважно,
// снятием прибавки в панели или потому, что снимать уже нечего.
func (a *App) sweepBonusOne(ctx context.Context, st storage.Storage, panel *remnawave.Client,
	t storage.TrafficBonusTarget) bool {
	lk := &a.finalizeUserLk[extLockIndex(strconv.FormatInt(t.TelegramID, 10))]
	lk.Lock()
	defer lk.Unlock()

	// Список брали БЕЗ замка и с паузами между людьми: пока очередь ползла,
	// человек мог активировать ещё один код. Судить по устаревшей копии
	// нельзя — она затрёт свежую запись, и оба подарка станут вечными.
	u, err := st.GetUser(ctx, t.TelegramID)
	if err != nil {
		return false
	}
	if u == nil || u.TrafficBonus == nil {
		return false
	}
	bonus := u.TrafficBonus

	pu, perr := panel.FindByTelegramID(ctx, t.TelegramID)
	if perr != nil {
		return false
	}
	// Учётки больше нет — забирать нечего и негде.
	if pu == nil {
		return a.clearTrafficBonus(ctx, st, t.TelegramID)
	}
	// Учётку тронул кто-то ещё: потолок переписала покупка (у неё свой лимит
	// тарифа) или рука админа, либо сменился срок подписки. Что в потолке
	// сейчас стоит — не наше число, и вычитать из него подарок значит
	// отобрать у человека оплаченное. Запись просто закрываем.
	if !bonus.SameAccount(pu.TrafficLimit, pu.ExpireAt) {
		return a.clearTrafficBonus(ctx, st, t.TelegramID)
	}
	// Период не сменился — подарок ещё работает. Но если подписка кончилась
	// давно, держать запись незачем: снимать прибавку с мёртвой учётки
	// бессмысленно, а потолок ей всё равно применят заново при оплате.
	if !bonus.Spent(pu.TrafficResetAt) {
		if exp, perr := time.Parse(time.RFC3339, pu.ExpireAt); perr == nil &&
			time.Since(exp) > bonusStaleAfter {
			return a.clearTrafficBonus(ctx, st, t.TelegramID)
		}
		return false
	}
	want := bonus.Limit - bonus.Bytes
	if want <= 0 {
		return a.clearTrafficBonus(ctx, st, t.TelegramID)
	}
	if err := panel.SetTrafficLimit(ctx, pu.Ref, want); err != nil {
		a.log.Warn("подарочный трафик: снятие прибавки", "tg_id", t.TelegramID, "err", err)
		return false
	}
	a.invalidateSubCache(t.TelegramID)
	return a.clearTrafficBonus(ctx, st, t.TelegramID)
}

func (a *App) clearTrafficBonus(ctx context.Context, st storage.Storage, tgID int64) bool {
	if err := st.SetTrafficBonus(ctx, tgID, (*model.TrafficBonus)(nil)); err != nil {
		a.log.Warn("подарочный трафик: очистка записи", "tg_id", tgID, "err", err)
		return false
	}
	return true
}

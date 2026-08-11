package app

import (
	"context"
	"strconv"
	"strings"
	"time"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// addSubParams snapshots the add-on config (and panel client) under the lock.
func (a *App) addSubParams() (panel *remnawave.Client, enabled bool, suffix string, trafficBytes int64, internal []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	panel = a.panel
	if a.botCfg == nil {
		return panel, false, remnawave.DefaultAddSubSuffix, 0, nil
	}
	c := a.botCfg.AddSub
	suffix = c.UsernameSuffix
	if suffix == "" {
		suffix = remnawave.DefaultAddSubSuffix
	}
	return panel, c.Enabled, suffix, int64(c.TrafficGB) * 1024 * 1024 * 1024, append([]string(nil), c.InternalSquads...)
}

// addSubOptions is addSubParams shaped for the panel client. Migration of a
// legacy-named add-on is never part of it: the automatic paths must not delete
// anything (see remnawave.AddSubOptions.MigrateLegacyName).
func (a *App) addSubOptions(resetTraffic bool) (*remnawave.Client, bool, remnawave.AddSubOptions) {
	panel, enabled, suffix, traffic, internal := a.addSubParams()
	return panel, enabled, remnawave.AddSubOptions{
		Suffix:         suffix,
		TrafficBytes:   traffic,
		InternalSquads: internal,
		ResetTraffic:   resetTraffic,
	}
}

// planAddSubOn — продаётся ли опция доп-подписки с этим тарифом. Глобальный
// переключатель — мастер-рубильник инфраструктуры: без него опции нет ни у
// кого. Режим тарифа: «выкл» снимает опцию, «вкл» и «наследовать» при
// включённой инфраструктуре дают её (наследование — поведение до появления
// поля: опция у всех).
func (a *App) planAddSubOn(p *model.Plan) bool {
	_, enabled, _, _, _ := a.addSubParams()
	if !enabled {
		return false
	}
	return p == nil || model.NormalizeAddSubMode(p.AddSub) != model.PlanAddSubOff
}

// addSubTexts — название и описание опции для тарифа: свои тексты тарифа →
// общие из настроек доп-подписки → стандартное название (без описания).
func (a *App) addSubTexts(lang string, p *model.Plan) (name, desc string) {
	a.mu.Lock()
	var g model.AddSubConfig
	if a.botCfg != nil {
		g = a.botCfg.AddSub
	}
	a.mu.Unlock()
	if p != nil {
		name, desc = strings.TrimSpace(p.AddSubName), strings.TrimSpace(p.AddSubDesc)
	}
	if name == "" {
		name = strings.TrimSpace(g.Name)
	}
	if desc == "" {
		desc = strings.TrimSpace(g.Description)
	}
	if name == "" {
		name = i18n.T(lang, "addsub.default_name")
	}
	return name, desc
}

// userAddSubName — название опции для экранов ЭТОГО пользователя: по тарифу
// его последней сделки, иначе общее.
func (a *App) userAddSubName(ctx context.Context, telegramID int64) string {
	lang := a.lang(telegramID)
	code := a.userPlanCode(ctx, telegramID)
	var p *model.Plan
	if code != "" {
		p, _ = a.planByCode(ctx, code)
	}
	name, _ := a.addSubTexts(lang, p)
	return name
}

// addSubSoldTo — продана ли доп-подписка этому пользователю: по снимку его
// последней сделки. Снимка или поля нет — «как раньше»: опция есть, пока
// включена глобально (так живут покупки до появления опции и триалы).
func (a *App) addSubSoldTo(ctx context.Context, telegramID int64) bool {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return true
	}
	u, err := st.GetUser(ctx, telegramID)
	if err != nil || u == nil {
		return true
	}
	return u.Snapshot.AddSubSold()
}

// syncAddSub upserts the add-on user B for telegramID (best-effort; a failure
// must never break the main purchase). No-op when the feature is disabled.
// resetTraffic must be true exactly when the main subscription's traffic was
// reset as well (paid renewal), so B doesn't stay exhausted after payment.
//
// Продан ли тариф с опцией, решает снимок сделки: snap, если он передан
// (финализация — свежий снимок ещё не записан в базу), иначе снимок
// пользователя из базы. Тариф без опции при продлении ВЫКЛЮЧАЕТ доп-подписку
// в панели: она больше не продана, но данные B не удаляются.
func (a *App) syncAddSub(ctx context.Context, telegramID int64, resetTraffic bool) {
	a.syncAddSubSnap(ctx, telegramID, resetTraffic, nil)
}

func (a *App) syncAddSubSnap(ctx context.Context, telegramID int64, resetTraffic bool, snap *model.PlanSnapshot) {
	panel, enabled, opt := a.addSubOptions(resetTraffic)
	if !enabled || panel == nil {
		return
	}
	sold := true
	if snap != nil {
		sold = snap.AddSubSold()
	} else {
		sold = a.addSubSoldTo(ctx, telegramID)
	}
	if !sold {
		if err := panel.SetAddSubEnabled(ctx, telegramID, opt.Suffix, false); err != nil {
			a.log.Warn("addsub: отключение по тарифу без опции", "tg_id", telegramID, "err", err)
		}
		return
	}
	res, err := panel.UpsertAddSub(ctx, telegramID, opt)
	if err != nil {
		a.log.Warn("addsub: upsert", "tg_id", telegramID, "err", err)
		return
	}
	// Продление на тариф с опцией обязано ОЖИВИТЬ доп-подписку, выключенную
	// прошлым тарифом без опции: upsert статус не трогает.
	if err := panel.SetAddSubEnabled(ctx, telegramID, opt.Suffix, true); err != nil {
		a.log.Warn("addsub: включение после upsert", "tg_id", telegramID, "err", err)
	}
	if res.Legacy != "" {
		a.log.Info("addsub: доп-подписка живёт под старым именем; «Синхронизировать всех» переведёт её на новое",
			"tg_id", telegramID, "legacy", res.Legacy)
	}
}

// removeAddSub deletes user B (runs regardless of the toggle, to clean up).
// Must run BEFORE the main user is deleted: B is resolved from A's username, so
// once A is gone only the legacy name can still be found.
func (a *App) removeAddSub(ctx context.Context, telegramID int64) {
	panel, _, suffix, _, _ := a.addSubParams()
	if panel == nil {
		return
	}
	if err := panel.DeleteAddSub(ctx, telegramID, suffix); err != nil {
		a.log.Warn("addsub: delete", "tg_id", telegramID, "err", err)
	}
}

// setAddSubEnabledPanel enables/disables user B alongside the main one.
// Disabling runs regardless of the toggle (a leftover B must not keep serving a
// blocked user); enabling only makes sense while the feature is on — и только
// если опция вообще продана этому пользователю (тариф без опции не должен
// оживлять B при разблокировке).
func (a *App) setAddSubEnabledPanel(ctx context.Context, telegramID int64, enable bool) {
	panel, enabled, suffix, _, _ := a.addSubParams()
	if panel == nil || (enable && !enabled) {
		return
	}
	if enable && !a.addSubSoldTo(ctx, telegramID) {
		return
	}
	if err := panel.SetAddSubEnabled(ctx, telegramID, suffix, enable); err != nil {
		a.log.Warn("addsub: set-enabled", "tg_id", telegramID, "err", err)
	}
}

// addSubStatus returns B's snapshot for the user-facing screens; ok=false when
// the feature is off, the plan was sold without the option, or the user has no
// add-on subscription — экраны тогда выглядят так, будто опции нет вовсе.
func (a *App) addSubStatus(ctx context.Context, telegramID int64) (remnawave.AddSubInfo, bool) {
	panel, enabled, suffix, _, _ := a.addSubParams()
	if !enabled || panel == nil {
		return remnawave.AddSubInfo{}, false
	}
	if !a.addSubSoldTo(ctx, telegramID) {
		return remnawave.AddSubInfo{}, false
	}
	return panel.AddSubStatus(ctx, telegramID, suffix)
}

// resetAddSubDevices extends the "reset devices" action onto B. The middleware
// forwards the client's HWID headers to the add-on subscription as well, so B
// accumulates registrations for the same devices; without this they keep
// occupying B's inherited device limit and keep working with the old
// credentials. Best-effort: the main reset has already succeeded by then.
func (a *App) resetAddSubDevices(ctx context.Context, telegramID int64) {
	panel, enabled, suffix, _, _ := a.addSubParams()
	if !enabled || panel == nil {
		return
	}
	res, found, err := panel.ResetAddSubDevices(ctx, telegramID, suffix)
	if err != nil {
		// The user has already been told the reset went through (it did, for
		// the main subscription), so the add-on has to catch up out of band
		// instead of quietly staying on the old credentials.
		a.log.Warn("addsub: reset devices, повторю в фоне", "tg_id", telegramID, "err", err)
		a.retryAddSubResetInBackground(telegramID)
		return
	}
	if !found {
		return
	}
	if res.HwidErr != nil {
		a.log.Warn("addsub: HWID delete-all failed; keys rotated, retrying in background",
			"tg_id", telegramID, "err", res.HwidErr)
		a.clearHwidInBackground(telegramID, res.Ref)
	}
}

// addSubResetRetryEvery is the pause between background attempts to finish an
// add-on device reset that failed while the user was waiting.
const addSubResetRetryEvery = 30 * time.Second

// retryAddSubResetInBackground keeps retrying the add-on device reset until it
// succeeds or the budget runs out. Deduped per user so repeated taps can't
// stack goroutines (shares the map with the HWID retry, under a distinct key).
func (a *App) retryAddSubResetInBackground(telegramID int64) {
	key := "addsub:" + strconv.FormatInt(telegramID, 10)
	a.hwidMu.Lock()
	if a.hwidRetrying == nil {
		a.hwidRetrying = map[string]bool{}
	}
	if a.hwidRetrying[key] {
		a.hwidMu.Unlock()
		return
	}
	a.hwidRetrying[key] = true
	a.hwidMu.Unlock()

	go func() {
		defer func() {
			a.hwidMu.Lock()
			delete(a.hwidRetrying, key)
			a.hwidMu.Unlock()
			if r := recover(); r != nil {
				a.log.Error("addsub reset background retry panicked", "tg_id", telegramID, "err", r)
			}
		}()
		ctx, cancel := context.WithTimeout(a.bgContext(), hwidBackgroundBudget)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				a.log.Warn("addsub: сброс устройств доп-подписки не удался и после фоновых попыток", "tg_id", telegramID)
				return
			case <-time.After(addSubResetRetryEvery):
			}
			panel, enabled, suffix, _, _ := a.addSubParams()
			if !enabled || panel == nil {
				return
			}
			res, found, err := panel.ResetAddSubDevices(ctx, telegramID, suffix)
			if err != nil {
				continue
			}
			if found && res.HwidErr != nil {
				a.clearHwidInBackground(telegramID, res.Ref)
			}
			if found {
				a.log.Info("addsub: сброс устройств доп-подписки добит в фоне", "tg_id", telegramID)
			}
			return
		}
	}()
}

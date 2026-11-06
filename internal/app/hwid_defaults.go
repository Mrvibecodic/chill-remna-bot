package app

import (
	"context"
	"strconv"
	"sync"
	"time"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// Лимит устройств по умолчанию панель меняет редко, а спрашивает его каждая
// витрина — держим в памяти. После сбоя повторяем не чаще hwidDefRetry
// (без скоупа subscription-settings:get панель так и будет отвечать 403), а
// последнее удачное значение показываем не дольше hwidDefStale.
const (
	hwidDefTTL   = 10 * time.Minute
	hwidDefRetry = 2 * time.Minute
	hwidDefStale = time.Hour
)

type hwidDefCache struct {
	mu       sync.Mutex
	def      remnawave.HwidDefaults
	at       time.Time // последнее удачное чтение
	failedAt time.Time // последний сбой; обнуляется удачным чтением
	// inflight — панель уже спрашивают: второй запрос не нужен. Кому нечего
	// показать, пока ответа нет, ждут его, а не рисуют экран без лимита.
	inflight chan struct{}
	lastErr  string
}

func (c *hwidDefCache) usable() bool {
	return !c.at.IsZero() && time.Since(c.at) < hwidDefStale
}

// hwidDefaults — лимит устройств по умолчанию из панели. ok=false — его не
// удалось прочитать, и тогда экраны показывают всё как без него.
func (a *App) hwidDefaults(ctx context.Context) (remnawave.HwidDefaults, bool) {
	c := &a.hwidDef
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()

	c.mu.Lock()
	if wait := c.inflight; wait != nil && !c.usable() {
		c.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
		}
		c.mu.Lock()
	}
	if panel == nil || c.inflight != nil ||
		!c.at.IsZero() && time.Since(c.at) < hwidDefTTL ||
		!c.failedAt.IsZero() && time.Since(c.failedAt) < hwidDefRetry {
		def, ok := c.def, c.usable()
		c.mu.Unlock()
		return def, ok
	}
	done := make(chan struct{})
	c.inflight = done
	c.mu.Unlock()

	// Ответ нужен не только этому запросу: его ждут и соседние, поэтому уход
	// клиента не обрывает чтение.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	nd, err := panel.HwidDefaults(rctx)
	cancel()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight = nil
	close(done)
	if err != nil {
		c.failedAt = time.Now()
		if msg := err.Error(); msg != c.lastErr {
			c.lastErr = msg
			a.log.Warn("лимит устройств по умолчанию из панели не прочитан (на панели 2.8+ токену нужен скоуп subscription-settings:get)", "err", err)
		}
		return c.def, c.usable()
	}
	c.lastErr, c.failedAt = "", time.Time{}
	c.def, c.at = nd, time.Now()
	return nd, true
}

// devShown — какой лимит устройств показать покупателю срока: свой у тарифа,
// а без него — по умолчанию панели. unlim — панель устройства не ограничивает;
// n=0 и !unlim — показать нечего.
type devShown struct {
	n     int
	unlim bool
}

func (v devShown) any() bool { return v.n > 0 || v.unlim }

// planDevShown — лимит устройств срока d тарифа p для витрины. Контроль
// устройств в панели выключен — лимит не применяется вовсе, и свой лимит
// тарифа покупателю не обещаем.
func planDevShown(p *model.Plan, d *model.PlanDuration, def remnawave.HwidDefaults, defOK bool) devShown {
	n := p.DeviceLimitFor(d)
	if !defOK {
		return devShown{n: max(n, 0)}
	}
	if hwidOff(def, p.ExtSquadFor(d)) {
		return devShown{unlim: true}
	}
	if n > 0 {
		return devShown{n: n}
	}
	return ruleShown(def, p.ExtSquadFor(d))
}

// hwidOff — контроль устройств для подписки со сквадом ext точно выключен.
func hwidOff(def remnawave.HwidDefaults, ext string) bool {
	r, ok := def.For(ext)
	return ok && !r.Enabled
}

func ruleShown(def remnawave.HwidDefaults, ext string) devShown {
	r, ok := def.For(ext)
	switch {
	case !ok:
		return devShown{}
	case !r.Enabled:
		return devShown{unlim: true}
	case r.Fallback > 0:
		return devShown{n: r.Fallback}
	}
	return devShown{}
}

// withHwidDefault дописывает лимит панели по умолчанию к устройствам
// пользователя без своего лимита: экран показывает «2 / 5», а не «2».
// Контроль устройств выключен — лимита нет, даже если он записан у самого
// пользователя: «2 / 5» было бы неправдой.
func (a *App) withHwidDefault(ctx context.Context, info remnawave.DeviceInfo) remnawave.DeviceInfo {
	if info.OwnLimit && !info.HasLimit {
		return info
	}
	def, ok := a.hwidDefaults(ctx)
	if !ok {
		return info
	}
	if hwidOff(def, info.ExtSquad) {
		info.Limit, info.HasLimit = 0, false
		return info
	}
	if info.HasLimit {
		return info
	}
	if v := ruleShown(def, info.ExtSquad); v.n > 0 {
		info.Limit, info.HasLimit = v.n, true
	}
	return info
}

// hwidOwnLabel — свой лимит тарифа для админки; при выключенном в панели
// контроле устройств — с пометкой, что он не применяется.
func (a *App) hwidOwnLabel(ctx context.Context, lang string, n int, ext string) string {
	label := strconv.Itoa(n)
	if def, ok := a.hwidDefaults(ctx); ok && hwidOff(def, ext) {
		label += " " + i18n.T(lang, "pricing.hwid_off")
	}
	return label
}

// hwidDefaultLabel — «по дефолту панели» для админки, с самим значением, если
// оно известно: «по дефолту панели (3)».
func (a *App) hwidDefaultLabel(ctx context.Context, lang, ext string) string {
	label := i18n.T(lang, "pricing.hwid_default")
	def, ok := a.hwidDefaults(ctx)
	if !ok {
		return label
	}
	switch v := ruleShown(def, ext); {
	case v.unlim:
		return label + " (" + i18n.T(lang, "buy.dev_unlim") + ")"
	case v.n > 0:
		return label + " (" + strconv.Itoa(v.n) + ")"
	}
	return label
}

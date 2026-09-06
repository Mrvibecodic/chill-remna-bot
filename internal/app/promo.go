package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func (a *App) showPromoUser(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.sendKBSection(ctx, chatID, assets.SectionPromoCode, i18n.T(lang, "promo.user_title"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "promo.btn_enter"), "pr:enter")},
		backHomeRow(lang),
	})
}

func (a *App) onPromoUser(ctx context.Context, chatID int64, val string) {
	if val == "enter" {
		a.getUI(chatID).awaitPromo = true
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "promo.ask"), "menu:promo")
	}
}

func (a *App) applyPromo(ctx context.Context, chatID int64, raw string) {
	msg, _ := a.redeemPromo(ctx, chatID, raw)
	a.notify(ctx, chatID, msg)
}

// redeemPromo validates and applies a promo code, returning a localized result
// message and whether it succeeded. Shared by the chat flow and the Mini App
// so the rules (expiry, max-uses, already-used, days-need-sub) stay identical.
func (a *App) redeemPromo(ctx context.Context, chatID int64, raw string) (string, bool) {
	lang := a.lang(chatID)
	code := strings.ToUpper(strings.TrimSpace(raw))
	if code == "" || a.store == nil {
		return i18n.T(lang, "promo.not_found"), false
	}
	p, _ := a.store.GetPromo(ctx, code)
	if p == nil {
		return i18n.T(lang, "promo.not_found"), false
	}
	if p.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, p.ExpiresAt); err == nil && time.Now().UTC().After(t) {
			return i18n.T(lang, "promo.expired"), false
		}
	}
	if p.MaxUses > 0 && p.Used >= p.MaxUses {
		return i18n.T(lang, "promo.exhausted"), false
	}
	if done, _ := a.store.PromoRedeemedBy(ctx, code, chatID); done {
		return i18n.T(lang, "promo.already"), false
	}
	// Код закрепляется за пользователем ДО начисления: одновременные
	// активации не пробьют лимит. Если начислить не удалось — закрепление
	// снимается и код остаётся доступным.
	reserved, err := a.store.RedeemPromo(ctx, code, chatID)
	if err != nil {
		a.log.Warn("промокод: закрепление", "tg_id", chatID, "err", err)
		return i18n.T(lang, "promo.grant_fail"), false
	}
	if !reserved {
		if done, _ := a.store.PromoRedeemedBy(ctx, code, chatID); done {
			return i18n.T(lang, "promo.already"), false
		}
		return i18n.T(lang, "promo.exhausted"), false
	}
	switch p.Kind {
	case model.PromoKindTraffic, model.PromoKindTrafficPeriod:
		oneTime := p.Kind == model.PromoKindTraffic
		ok, reason := a.addBonusTraffic(ctx, chatID, p.Value, oneTime)
		if !ok {
			a.releasePromo(code, chatID)
			switch reason {
			case promoNoSub:
				return i18n.T(lang, "promo.need_sub"), false
			case promoNoLimit:
				return i18n.T(lang, "promo.no_limit"), false
			}
			return i18n.T(lang, "promo.grant_fail"), false
		}
		if oneTime {
			return i18n.T(lang, "promo.ok_traffic", p.Value), true
		}
		return i18n.T(lang, "promo.ok_traffic_period", p.Value), true
	case model.PromoKindDays:
		ok, found := a.addReferralDays(ctx, chatID, p.Value)
		if !ok {
			a.releasePromo(code, chatID)
			if !found {
				return i18n.T(lang, "promo.need_sub"), false
			}
			return i18n.T(lang, "promo.grant_fail"), false
		}
		return i18n.T(lang, "promo.ok_days", p.Value), true
	default:
		if err := a.store.AddBalance(ctx, chatID, int64(p.Value)*100); err != nil {
			a.releasePromo(code, chatID)
			return i18n.T(lang, "promo.grant_fail"), false
		}
		return i18n.T(lang, "promo.ok_balance", p.Value), true
	}
}

func (a *App) showPromoAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	var promos []model.PromoCode
	if a.store != nil {
		promos, _ = a.store.ListPromos(ctx)
	}
	var lines []string
	rows := [][]models.InlineKeyboardButton{}
	for _, p := range promos {
		kind := i18n.T(lang, "promoadm.kind_balance")
		switch p.Kind {
		case model.PromoKindDays:
			kind = i18n.T(lang, "promoadm.kind_days")
		case model.PromoKindTraffic:
			kind = i18n.T(lang, "promoadm.kind_traffic")
		case model.PromoKindTrafficPeriod:
			kind = i18n.T(lang, "promoadm.kind_traffic_period")
		}
		limit := "∞"
		if p.MaxUses > 0 {
			limit = strconv.Itoa(p.MaxUses)
		}
		exp := i18n.T(lang, "promoadm.no_expiry")
		if p.ExpiresAt != "" {
			exp = formatExpire(p.ExpiresAt, lang)
		}
		lines = append(lines, fmt.Sprintf("<code>%s</code> — %s %d · %d/%s · %s", p.Code, kind, p.Value, p.Used, limit, exp))
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "promoadm.btn_del", p.Code), "pr:del:"+p.Code)})
	}
	body := i18n.T(lang, "promoadm.empty")
	if len(lines) > 0 {
		body = strings.Join(lines, "\n")
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "promoadm.btn_add"), "pr:add")})
	rows = append(rows, navBack(lang, "menu:marketing"))
	a.sendKBSection(ctx, chatID, assets.SectionPromoCode, i18n.T(lang, "promoadm.title", body), rows)
}

func (a *App) onPromoAdmin(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	switch {
	case val == "add":
		a.getUI(chatID).adminInput = "promo_create"
		a.askInput(ctx, chatID, i18n.T(lang, "promoadm.ask_create"), "menu:promoadmin")
	case strings.HasPrefix(val, "del:"):
		if a.store != nil {
			_ = a.store.DeletePromo(ctx, strings.TrimPrefix(val, "del:"))
		}
		a.showPromoAdmin(ctx, chatID)
	}
}

func (a *App) createPromoFromText(ctx context.Context, chatID int64, text string) {
	lang := a.lang(chatID)
	f := strings.Fields(text)
	if len(f) < 3 {
		a.sendHome(ctx, chatID, i18n.T(lang, "promoadm.bad_format"))
		return
	}
	kind := strings.ToLower(f[1])
	if kind != model.PromoKindBalance && kind != model.PromoKindDays &&
		kind != model.PromoKindTraffic && kind != model.PromoKindTrafficPeriod {
		a.sendHome(ctx, chatID, i18n.T(lang, "promoadm.bad_format"))
		return
	}
	value, _ := strconv.Atoi(f[2])
	if value <= 0 {
		a.sendHome(ctx, chatID, i18n.T(lang, "promoadm.bad_format"))
		return
	}
	// Гигабайты уезжают в панель байтами: без верхней границы опечатка в
	// значении переполняет int64 и превращает подарок в отрицательный потолок.
	if isTrafficKind(kind) && value > maxPromoTrafficGB {
		a.sendHome(ctx, chatID, i18n.T(lang, "promoadm.bad_format"))
		return
	}
	maxUses := 0
	if len(f) >= 4 {
		maxUses, _ = strconv.Atoi(f[3])
	}
	expires := ""
	if len(f) >= 5 {
		if d, _ := strconv.Atoi(f[4]); d > 0 {
			expires = time.Now().UTC().Add(time.Duration(d) * 24 * time.Hour).Format(time.RFC3339)
		}
	}
	if a.store != nil {
		_ = a.store.CreatePromo(ctx, &model.PromoCode{Code: strings.ToUpper(f[0]), Kind: kind, Value: value, MaxUses: maxUses, ExpiresAt: expires})
	}
	a.showPromoAdmin(ctx, chatID)
}

// releasePromo снимает закрепление кода за человеком, когда начислить бонус
// не удалось.
//
// Контекст ФОНОВЫЙ, а не вызывающего: отказ чаще всего и означает, что у
// запроса кончился дедлайн (мини-апп даёт 12 секунд, внутри — два похода в
// панель). На мёртвом контексте компенсация падала молча, и код списывался
// навсегда: человек получал «вы уже активировали этот промокод» без бонуса.
func (a *App) releasePromo(code string, chatID int64) {
	if a.store == nil {
		return
	}
	if err := a.store.ReleasePromo(a.bgContext(), code, chatID); err != nil {
		a.log.Warn("промокод: откат закрепления", "tg_id", chatID, "code", code, "err", err)
	}
}

// Причины отказа промокода на трафик — отдельно от «не получилось»: человеку
// нужно понимать, что делать дальше.
const (
	promoNoSub   = "no_sub"
	promoNoLimit = "no_limit"
)

// maxPromoTrafficGB — потолок значения кода на трафик (1 ПБ). Больше не бывает
// подарков, а меньше — не переполняет int64 при переводе в байты.
const maxPromoTrafficGB = 1024 * 1024

const bytesPerGB = int64(1024 * 1024 * 1024)

// addBonusTraffic поднимает потолок трафика в панели на gb гигабайт.
//
// Срок, сквады и лимит устройств не трогаются — ровно как у бонусных дней:
// подарок обязан быть подарком, а не переприменкой чужих условий. Бонус живёт
// до следующей оплаты: покупка перезаписывает потолок трафиком тарифа.
func (a *App) addBonusTraffic(ctx context.Context, tgID int64, gb int, oneTime bool) (bool, string) {
	if gb <= 0 || gb > maxPromoTrafficGB {
		return false, ""
	}
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if panel == nil {
		return false, ""
	}
	// Замок тот же, что сериализует выдачу подписки по человеку: потолок
	// читается и переписывается двумя запросами, и без него две активации
	// подряд (или активация в момент покупки) теряли бы одну из прибавок.
	lk := &a.finalizeUserLk[extLockIndex(strconv.FormatInt(tgID, 10))]
	lk.Lock()
	defer lk.Unlock()

	pu, err := panel.FindByTelegramID(ctx, tgID)
	if err != nil {
		a.log.Warn("бонусный трафик: поиск в панели", "tg_id", tgID, "err", err)
		return false, ""
	}
	// Учётки нет — это единственный случай, когда отвечаем «сначала оформите
	// подписку». Недоступная панель на этот ответ права не даёт.
	if pu == nil {
		return false, promoNoSub
	}
	// Истёкшая подписка: гигабайты уехали бы в мёртвую учётку и сгорели бы при
	// первой же покупке, а код при этом списался бы. Отказываем, как и при
	// отсутствии учётки, — человеку сначала нужна живая подписка.
	if exp, perr := time.Parse(time.RFC3339, pu.ExpireAt); perr == nil && !exp.After(time.Now().UTC()) {
		return false, promoNoSub
	}
	// Нулевой потолок в панели означает безлимит. Прибавка к нему не просто
	// бесполезна — она бы этот безлимит ОТОБРАЛА, выставив конечное число.
	if pu.TrafficLimit <= 0 {
		return false, promoNoLimit
	}
	add := int64(gb) * bytesPerGB
	// База — потолок без прежнего подарка, если тот уже отработал: иначе
	// отработавшая прибавка молча становилась бы постоянной, а следующая
	// запись про неё уже не помнила.
	base, carry := a.trafficBonusBase(ctx, tgID, pu)
	want := base + add
	if want < base {
		return false, ""
	}
	// Запись о разовом подарке готовится ДО похода в панель: она же нужна и в
	// ветке потерянного ответа, где патч мог примениться.
	bonus := nextTrafficBonus(pu, add, carry, want, oneTime)
	if err := panel.SetTrafficLimit(ctx, pu.Ref, want); err != nil {
		// Ошибка не означает, что панель НЕ применила патч: оборванный ответ,
		// таймаут запроса и 502 от прокси выглядят одинаково. Отдать код
		// обратно вслепую значит выдать бонус дважды, поэтому перечитываем
		// потолок — уже на фоновом контексте, дедлайн вызывающего к этому
		// моменту чаще всего и кончился.
		if a.bonusTrafficApplied(tgID, want) {
			a.log.Warn("бонусный трафик: ответ панели потерян, потолок применён", "tg_id", tgID, "err", err)
			a.saveTrafficBonus(tgID, bonus)
			a.invalidateSubCache(tgID)
			return true, ""
		}
		a.log.Warn("бонусный трафик: начисление", "tg_id", tgID, "err", err)
		return false, ""
	}
	a.saveTrafficBonus(tgID, bonus)
	a.invalidateSubCache(tgID)
	return true, ""
}

// bonusTrafficApplied перечитывает потолок в панели после неудачного ответа:
// true — прибавка на месте, начисление считать состоявшимся.
func (a *App) bonusTrafficApplied(tgID int64, want int64) bool {
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if panel == nil {
		return false
	}
	pu, err := panel.FindByTelegramID(a.bgContext(), tgID)
	if err != nil || pu == nil {
		return false
	}
	return pu.TrafficLimit >= want
}

func isTrafficKind(kind string) bool {
	return kind == model.PromoKindTraffic || kind == model.PromoKindTrafficPeriod
}

// trafficBonusBase — от какого потолка считать новую прибавку и сколько
// подарочных байтов переносится в новую запись.
//
// Прежний подарок бывает трёх видов. Учётку переписали (покупка, рука админа)
// — подарка в потолке нет, база это текущий потолок, переносить нечего.
// Подарок жив, период тот же — прибавки складываются. Период сменился, а
// потолок наш — подарок отработал: вычитаем его прямо сейчас, иначе он так и
// останется в потолке навсегда.
func (a *App) trafficBonusBase(ctx context.Context, tgID int64, pu *remnawave.PanelUser) (int64, int64) {
	prev := a.storedTrafficBonus(ctx, tgID)
	if prev == nil || !prev.SameAccount(pu.TrafficLimit, pu.ExpireAt) {
		return pu.TrafficLimit, 0
	}
	if prev.Spent(pu.TrafficResetAt) {
		base := pu.TrafficLimit - prev.Bytes
		if base <= 0 {
			return pu.TrafficLimit, 0
		}
		return base, 0
	}
	return pu.TrafficLimit, prev.Bytes
}

func (a *App) storedTrafficBonus(ctx context.Context, tgID int64) *model.TrafficBonus {
	if a.store == nil {
		return nil
	}
	u, err := a.store.GetUser(ctx, tgID)
	if err != nil || u == nil {
		return nil
	}
	return u.TrafficBonus
}

// nextTrafficBonus — что записать о подарке после этой выдачи.
//
// Постоянная прибавка своей записи не заводит, но потолок она меняет — и
// прежняя запись о разовом подарке обязана узнать про новый потолок, иначе
// проход решит, что подарка в панели больше нет, и тихо его забудет.
func nextTrafficBonus(pu *remnawave.PanelUser, add, carry, want int64, oneTime bool) *model.TrafficBonus {
	bytes := carry
	if oneTime {
		bytes += add
	}
	if bytes <= 0 {
		return nil
	}
	return &model.TrafficBonus{
		Bytes:   bytes,
		Limit:   want,
		Expire:  pu.ExpireAt,
		ResetAt: pu.TrafficResetAt,
	}
}

// saveTrafficBonus пишет запись о подарке. Контекст фоновый: выдача уже
// состоялась в панели, и дедлайн запроса тут ни при чём. Незаписанный подарок
// останется у человека навсегда — это подарок сверх обещанного, а не потеря,
// поэтому падать некуда, но в журнал сказать надо.
func (a *App) saveTrafficBonus(tgID int64, bonus *model.TrafficBonus) {
	if a.store == nil {
		return
	}
	if err := a.store.SetTrafficBonus(a.bgContext(), tgID, bonus); err != nil {
		a.log.Error("подарочный трафик не записан — останется постоянным", "tg_id", tgID, "err", err)
	}
}

package app

import (
	"context"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Режимы доступности тарифа.
//
// Фильтрация — ОДНА функция на все три поверхности (чат, мини-апп, личный
// кабинет) и на все три точки проверки: витрина → создание счёта →
// финализация. Гейты, написанные в каждом месте по-своему, разъезжаются
// первыми — поэтому все они зовут planAccessibleFor.

// planAccessibleFor отвечает, доступен ли тариф этому покупателю.
//
// Правила:
//   - «всем» — да; неизвестный режим (запись более новой версии) схлопывается
//     сюда же ещё в NormalizeAvailability;
//   - «по ссылке» — да: обладание кодом и есть допуск, код неперебираем, а
//     прячет такой тариф только витрина;
//   - «только новым» — тем, кто ещё ни разу не платил (триалы и пополнения
//     баланса платежами не считаются, см. HasPaidPayment). Купивший ЭТОТ тариф
//     остаётся допущенным: иначе первая же покупка делала человека «не новым» и
//     отрезала ему продление;
//   - «только действующим» — тем, кто платил хотя бы раз;
//   - «по списку» — записям списка допущенных; e-mail-аккаунты кабинета
//     (отрицательный синтетический ID) сопоставляются по почте. Списку
//     grandfathering не даётся: удаление из списка — явное решение админа.
//
// Ошибка хранилища — «недоступен» (fail-closed): гейт стережёт платные
// условия, и продать скрытый тариф из-за упавшей базы хуже, чем показать
// «недоступно» лишний раз. Витрина при недоступном хранилище не работает и
// без этого гейта.
//
// Включённость тарифа здесь НЕ проверяется: у «Базового» флаг пока ни на что
// не влияет (продажи идут по сетке конфига), а продажу остальных тарифов
// стережёт saleFor.
func (a *App) planAccessibleFor(ctx context.Context, p *model.Plan, tgID int64) bool {
	if p == nil {
		return true
	}
	switch model.NormalizeAvailability(p.Availability) {
	case model.PlanAvailNew:
		paid, err := a.hasPaidPayment(ctx, tgID)
		if err != nil {
			return false
		}
		return !paid || a.userPlanCode(ctx, tgID) == p.Code
	case model.PlanAvailExisting:
		paid, err := a.hasPaidPayment(ctx, tgID)
		if err != nil {
			return false
		}
		return paid || a.userPlanCode(ctx, tgID) == p.Code
	case model.PlanAvailList:
		a.mu.Lock()
		st := a.store
		a.mu.Unlock()
		if st == nil {
			return false
		}
		email := ""
		if tgID < 0 {
			// E-mail-аккаунт кабинета: его синтетический отрицательный ID админ
			// в список не положит — сопоставляем по почте.
			wu, err := st.GetWebUserByTgID(ctx, tgID)
			if err != nil {
				return false
			}
			if wu != nil {
				email = wu.Email
			}
		}
		ok, err := st.HasPlanAccess(ctx, p.Code, tgID, email)
		if err != nil {
			return false
		}
		return ok
	default:
		// «всем» и «по ссылке».
		return true
	}
}

// hasPaidPayment — «человек платил хотя бы раз». Ошибка возвращается наверх:
// fail-closed решает вызывающий гейт.
func (a *App) hasPaidPayment(ctx context.Context, tgID int64) (bool, error) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return false, errStorageUnavailable
	}
	return st.HasPaidPayment(ctx, tgID)
}

// userPlanCode — код тарифа последней покупки (пусто, если покупок с
// снимком не было). Это и есть grandfathering для режимов «новым» и
// «действующим»: продление своего тарифа не отрезается.
func (a *App) userPlanCode(ctx context.Context, tgID int64) string {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return ""
	}
	u, err := st.GetUser(ctx, tgID)
	if err != nil || u == nil || u.Snapshot == nil {
		return ""
	}
	return u.Snapshot.Code
}

// baseSaleAllowed — доступен ли покупателю «Базовый», по которому продают
// витрина, мини-апп и кабинет. Тариф «по ссылке» витрина прячет: базовая
// сетка в этом режиме продаётся только по прямой ссылке на тариф.
func (a *App) baseSaleAllowed(ctx context.Context, tgID int64) bool {
	p := a.basePlanRow(ctx)
	if p == nil {
		// Тарифа ещё нет (первый запуск до синхронизации) — продажи идут по
		// сетке, как и раньше.
		return true
	}
	// Выключенный «Базовый» с продажи снят целиком — как любой выключенный
	// тариф.
	if !p.Enabled {
		return false
	}
	if model.NormalizeAvailability(p.Availability) == model.PlanAvailLink {
		return false
	}
	return a.planAccessibleFor(ctx, p, tgID)
}

// basePlanRow читает строку «Базового». nil — тарифа нет или база недоступна.
func (a *App) basePlanRow(ctx context.Context) *model.Plan {
	p, err := a.planByCode(ctx, model.PlanCodeBase)
	if err != nil {
		// Fail-closed здесь невозможен без ложных отказов на первом запуске —
		// но ошибка базы валит витрину раньше нас, поэтому честно продолжаем
		// как «тарифа нет».
		a.log.Warn("тариф «Базовый» не прочитан", "err", err)
		return nil
	}
	return p
}

// showRenew — вход «Продлить». Три сценария (по плану этапа):
//  1. условия тарифа не изменились — карточка тарифа как есть;
//  2. тот же тариф, но условия срока изменились (цена, лимиты, сквады, опция)
//     — та же карточка с плашкой «условия изменились»;
//  3. тарифа больше нет или он закрыт покупателю — честное сообщение и выбор
//     нового тарифа, а не молчаливая продажа чужих условий.
func (a *App) showRenew(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	snap := a.userSnapshot(ctx, chatID)
	code := planCodeOf(snap)
	if snap == nil || code == "" {
		// Покупок со снимком не было — обычная витрина.
		a.showPlans(ctx, chatID)
		return
	}
	p, err := a.planByCode(ctx, code)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	if p == nil && code == model.PlanCodeBase {
		a.mu.Lock()
		p = basePlanFrom(a.botCfg, nil)
		a.mu.Unlock()
	}
	if p == nil || !p.Enabled || !planSellsAnything(p) || !a.planAccessibleFor(ctx, p, chatID) {
		// Сценарий 3: тариф удалён, выключен или закрыт для покупателя.
		a.sendKB(ctx, chatID, i18n.T(lang, "renew.plan_gone"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
			homeRow(lang),
		})
		return
	}
	view := offerView{switchPlan: true}
	if a.renewTermsChanged(p, snap) {
		view.note = i18n.T(lang, "renew.terms_changed")
	}
	a.showPlanOfferView(ctx, chatID, p, view)
}

// renewTermsChanged — изменились ли УСЛОВИЯ проданного срока с момента покупки:
// сравнивается снимок сделки с сегодняшним снимком того же срока (в отпечатке
// и цена, и лимиты, и сквады, и опция доп-подписки). Снятый с продажи срок —
// тоже изменение.
func (a *App) renewTermsChanged(p *model.Plan, snap *model.PlanSnapshot) bool {
	if snap == nil || snap.Months <= 0 {
		return false
	}
	var cur *model.PlanSnapshot
	if p.Code == model.PlanCodeBase {
		// «Базовый» продаётся по сетке конфига — сравниваем с ней же.
		cur = a.planSnapshot(snap.Months)
	} else {
		d := p.Duration(snap.Months)
		if d == nil || d.Base == "" {
			return true
		}
		cur = a.planSnapshotOf(p, d, snap.Months)
	}
	if cur == nil || cur.Price == "" {
		return true
	}
	return cur.Fingerprint() != snap.Fingerprint()
}

// userSnapshot — снимок последней сделки пользователя (nil — покупок не было).
func (a *App) userSnapshot(ctx context.Context, tgID int64) *model.PlanSnapshot {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return nil
	}
	u, err := st.GetUser(ctx, tgID)
	if err != nil || u == nil {
		return nil
	}
	return u.Snapshot
}

// trialLockNotice — активный триал с запасом больше суток блокирует покупку
// (общий гейт витрины и экрана тарифа по ссылке): дни триала не должны
// сгорать.
func (a *App) trialLockNotice(ctx context.Context, chatID int64) bool {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return false
	}
	u, _ := st.GetUser(ctx, chatID)
	if u == nil || u.NotifyKind != "trial" || u.SubExpireAt == "" {
		return false
	}
	exp, err := time.Parse(time.RFC3339, u.SubExpireAt)
	if err != nil || daysUntil(exp, time.Now().UTC()) <= 1 {
		return false
	}
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, i18n.T(lang, "buy.trial_locked", formatExpire(u.SubExpireAt, lang)),
		[][]models.InlineKeyboardButton{homeRow(lang)})
	return true
}

// usersOnPlan — сколько пользователей живёт на тарифе: снимок последней
// сделки с этим кодом и не истёкшая подписка. Ошибка хранилища здесь не
// повод блокировать экран — счётчик информационный, возвращаем ноль.
func (a *App) usersOnPlan(ctx context.Context, code string) int {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return 0
	}
	n, err := st.CountUsersOnPlan(ctx, code, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		a.log.Warn("подписчики тарифа не посчитаны", "err", err, "plan", code)
		return 0
	}
	return n
}

// prunePlanAccess убирает на старте записи списков без тарифа. Их оставляет
// предыдущий образ бота: его DeletePlan про таблицу списков не знает, и
// осиротевшая запись молча ожила бы, достанься код новому тарифу (импорт).
func (a *App) prunePlanAccess(ctx context.Context) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return
	}
	if err := st.PrunePlanAccess(ctx); err != nil {
		a.log.Warn("чистка списков допущенных", "err", err)
	}
}

// notifyPlanGateBreach — третья точка гейта: счёт выставлен, деньги приняты, а
// доступ к тарифу к моменту финализации отозван (режим сменили, из списка
// убрали, тариф выключили или удалили). Решение владельца: подписка ВЫДАЁТСЯ
// по снимку — деньги уже приняты, клиент получает то, за что платил, — а админ
// получает уведомление и разбирается сам.
func (a *App) notifyPlanGateBreach(ctx context.Context, tgID int64, snap *model.PlanSnapshot) {
	if snap == nil || snap.Code == "" {
		return
	}
	p, err := a.planByCode(ctx, snap.Code)
	if err != nil {
		// База только что финализировала платёж — недоступность строки тарифа
		// здесь маловероятна и не повод молчать о продаже.
		return
	}
	reason := ""
	switch {
	case p == nil:
		reason = i18n.T(a.lang(a.cfg.AdminID), "plans.breach_deleted")
	case !p.Enabled:
		// «Базовый» теперь тоже выключается по-настоящему: продажа, успевшая
		// проскочить до остановки, — такой же пробой гейта.
		reason = i18n.T(a.lang(a.cfg.AdminID), "plans.breach_disabled")
	case !a.planAccessibleFor(ctx, p, tgID):
		reason = i18n.T(a.lang(a.cfg.AdminID), "plans.breach_revoked")
	default:
		return
	}
	alang := a.lang(a.cfg.AdminID)
	name := snap.Name
	if name == "" {
		name = snap.Code
	}
	a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "plans.breach_admin",
		escapeName(name), a.userLabelByID(ctx, tgID), snap.Months, reason))
}

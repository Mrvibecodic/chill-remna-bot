package app

import (
	"context"
	"time"

	"remnabot/internal/i18n"
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
		// действительно что-то задано. Ноль в старой карте — это не
		// «переопределение нулём», а «как у тарифа»: трафик тарифа и так ноль
		// (админка называет его безлимитом), а лимит устройств ноль означает
		// «оставить дефолт панели».
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
	// Копия глубокая: слайсы, общие с тем, что лежит в базе или строится
	// заново, стали бы гонкой, как только под замком понадобится не только
	// код с именем.
	cp := *p
	cp.IntSquads = append([]string(nil), p.IntSquads...)
	cp.Durations = append([]model.PlanDuration(nil), p.Durations...)
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

// purchaseIntentTTL — сколько живёт выбор срока. Экран со способами оплаты
// остаётся в переписке навсегда, и без срока годности нажатие на нём через
// месяц выставляло бы счёт по давно забытому выбору. Сутки с запасом
// перекрывают и рестарт бота, и «оплачу вечером».
const purchaseIntentTTL = 24 * time.Hour

// buyIntent возвращает намерение покупки. nil без ошибки — человек ничего не
// выбирал (или выбор просрочен); ошибка означает недоступное хранилище, и
// путать её с «выбора нет» нельзя: во втором случае человека возвращают в
// витрину, а в первом это был бы бесконечный круг.
func (a *App) buyIntent(ctx context.Context, chatID int64) (*model.PurchaseIntent, error) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return nil, nil
	}
	in, err := st.PurchaseIntent(ctx, chatID)
	if err != nil {
		a.log.Warn("намерение покупки не прочитано", "err", err, "user", chatID)
		return nil, err
	}
	if in == nil {
		return nil, nil
	}
	if t, perr := time.Parse(time.RFC3339, in.CreatedAt); perr == nil &&
		time.Since(t) > purchaseIntentTTL {
		_ = st.DeletePurchaseIntent(ctx, chatID)
		return nil, nil
	}
	return in, nil
}

// buyMonths — выбранный срок в месяцах (0, если выбора нет или он недоступен).
func (a *App) buyMonths(ctx context.Context, chatID int64) int {
	in, err := a.buyIntent(ctx, chatID)
	if err != nil || in == nil {
		return 0
	}
	return in.Months
}

// forgetBuyIntentFor убирает выбор, по которому только что прошла покупка.
// Сверка со сроком обязательна: продление автосписанием или добитый
// реконсилятором старый счёт не должны стирать выбор, который человек делает
// прямо сейчас.
func (a *App) forgetBuyIntentFor(ctx context.Context, chatID int64, months int) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil || chatID == 0 {
		return
	}
	in, err := st.PurchaseIntent(ctx, chatID)
	if err != nil || in == nil || in.Months != months {
		return
	}
	if err := st.DeletePurchaseIntent(ctx, chatID); err != nil {
		a.log.Warn("намерение покупки не удалено", "err", err, "user", chatID)
	}
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
	// Намерение одно на человека, а счёт Stars умеет выставлять и мини-апп со
	// своим сроком. Перебивать им выбор, сделанный в чате, нельзя: экран
	// способов оплаты остаётся подписан прежней ценой, а счёт ушёл бы на
	// другой срок. Чужой выбор не трогаем — снимок тогда просто не сохраняем,
	// и оплата пройдёт по текущим условиям, как было до появления снимков.
	if in.Months != months {
		return
	}
	in.Snapshot = snap
	if err := st.SetPurchaseIntent(ctx, in); err != nil {
		a.log.Warn("снимок Stars не сохранён", "err", err, "user", chatID)
	}
}

// starsSnapshot достаёт условия сделки, снятые при отправке счёта Stars.
// Снимок берётся только если срок совпал с оплаченным: иначе человек успел
// выбрать другой срок и снимок уже не про эту покупку.
func (a *App) starsSnapshot(ctx context.Context, chatID int64, months int) *model.PlanSnapshot {
	in, err := a.buyIntent(ctx, chatID)
	if err != nil || in == nil || in.Snapshot == nil || in.Months != months {
		return nil
	}
	return in.Snapshot
}

// buyMonthsOrAsk возвращает выбранный срок. Если выбора нет — показывает
// витрину заново и возвращает 0.
//
// Раньше на этом месте стоял фолбэк «считаем, что месяц»: человек, нажавший
// способ оплаты на старом экране из истории чата, молча получал счёт на месяц
// вместо выбранного года. Угадывать срок за человека нельзя — ни в его пользу,
// ни в свою.
func (a *App) buyMonthsOrAsk(ctx context.Context, chatID int64) int {
	in, err := a.buyIntent(ctx, chatID)
	if err != nil {
		// Хранилище недоступно: витрина здесь замкнула бы человека в круг
		// «выберите срок → как оплатить → выберите срок» без единого слова о
		// причине.
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return 0
	}
	if in != nil && in.Months > 0 {
		return in.Months
	}
	a.showPlans(ctx, chatID)
	return 0
}

// noPeriodForPayment вызывается, когда деньги уже приняты, а срок подписки
// определить не удалось: ни payload, ни строка счёта, ни намерение покупки его
// не дали. Выдавать «срок по умолчанию» здесь нельзя — это выдача не того, за
// что заплатили. Поэтому пишем в журнал, зовём админа и говорим человеку, что
// им занимаются.
func (a *App) noPeriodForPayment(ctx context.Context, method, extID string, chatID int64) {
	a.payLog(ctx, method, extID, chatID, "error", "оплата принята, но срок подписки неизвестен — выдача не проводится")
	alang := a.lang(a.cfg.AdminID)
	a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "admin.pay_no_period", method+" "+extID))
	if chatID != 0 {
		a.notify(ctx, chatID, i18n.T(a.lang(chatID), "pay.no_period"))
	}
}

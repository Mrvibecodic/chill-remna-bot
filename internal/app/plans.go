package app

import (
	"context"
	"errors"
	"sort"
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

	// Переносится ВСЯ сетка, а не только сроки витрины. Карты сетки — map[int],
	// и в живых установках лежат месяцы вне стандартной четвёрки (старые версии,
	// импорт) и настройки сроков, снятых с продажи (переопределение цены без
	// базовой). Витрина такие сроки не показывает, но автосписание продлевает
	// покупателя по цене ИЗ СЕТКИ на его срок — тариф, потерявший месяц при
	// переносе, после разворота синхронизации стёр бы его из сетки, и продление
	// падало бы навсегда.
	for _, mo := range gridMonths(pr) {
		d := model.PlanDuration{
			Months:   mo,
			Base:     pr.Base[mo],
			P2P:      pr.P2P[mo],
			YooKassa: pr.YooKassa[mo],
			Stars:    pr.Stars[mo],
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
		// Месяц, у которого во всей сетке нет ничего, кроме пустых строк и
		// нулей, — мусорная запись, длительность из неё не делается.
		if d.Base == "" && d.P2P == "" && d.YooKassa == "" && d.Stars == 0 &&
			d.TrafficGB == nil && d.DeviceLimit == nil && d.IntSquads == nil && d.ExtSquad == nil {
			continue
		}
		p.Durations = append(p.Durations, d)
	}
	p.Normalize()
	return p
}

// gridMonths — все месяцы, упомянутые хоть одной картой сетки, по возрастанию.
func gridMonths(pr model.Pricing) []int {
	seen := map[int]bool{}
	for mo := range pr.Base {
		seen[mo] = true
	}
	for mo := range pr.P2P {
		seen[mo] = true
	}
	for mo := range pr.YooKassa {
		seen[mo] = true
	}
	for mo := range pr.Stars {
		seen[mo] = true
	}
	for mo := range pr.Traffic {
		seen[mo] = true
	}
	for mo := range pr.Devices {
		seen[mo] = true
	}
	for mo := range pr.SquadsInt {
		seen[mo] = true
	}
	for mo := range pr.SquadsExt {
		seen[mo] = true
	}
	out := make([]int, 0, len(seen))
	for mo := range seen {
		if mo > 0 {
			out = append(out, mo)
		}
	}
	sort.Ints(out)
	return out
}

// syncBasePlan держит «Базовый» в соответствии с сеткой цен из конфига.
// Вызывается при загрузке конфига и после каждого его сохранения.
//
// Ошибку возвращает, но вызывающие её только логируют: тариф пока никем не
// читается, и падать из-за него на старте бот не должен.
func (a *App) syncBasePlan(ctx context.Context) error {
	// Read-modify-write строки тарифа: читаем существующий, сравниваем и пишем
	// целиком. Тот же цикл выполняет админка тарифов, поэтому оба идут под одним
	// замком — иначе правка из карточки, начатая до синхронизации, вернула бы
	// цену, только что приехавшую из конфига.
	a.plansMu.Lock()
	defer a.plansMu.Unlock()
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
	// Под замком из этой копии читаются только код и имя (basePlanIdentLocked).
	// Слайсы копируются, чтобы не делить их с вызывающим; если под замком
	// понадобятся сами условия, клонировать придётся и то, на что смотрят
	// указатели внутри длительностей.
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

// periodOnSale сообщает, продаётся ли срок прямо сейчас. Признак ровно тот же,
// по которому срок рисует витрина, — непустая базовая цена: иначе кнопка из
// старого сообщения или запрос мимо витрины продавали бы срок, который админ с
// продажи снял (у P2P это ещё и заявка с пустой суммой).
//
// Проверять по списку model.PlanMonths мало: он захардкожен и на следующих
// этапах, где длительности станут произвольными, начнёт молча съедать нажатия.
func (a *App) periodOnSale(months int) bool {
	if months <= 0 {
		return false
	}
	return a.pricing().Base[months] != ""
}

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
		// Бот без хранилища продать ничего не может, и молчаливый круг
		// «витрина → способы → витрина» здесь ни к чему.
		return nil, errors.New("хранилище недоступно")
	}
	in, err := st.PurchaseIntent(ctx, chatID)
	if err != nil {
		a.log.Warn("намерение покупки не прочитано", "err", err, "user", chatID)
		return nil, err
	}
	if in == nil {
		return nil, nil
	}
	// Дата не разобралась — считаем выбор просроченным, а не вечным: строка
	// без времени могла приехать откуда угодно, а «живёт вечно» здесь опаснее.
	t, perr := time.Parse(time.RFC3339, in.CreatedAt)
	if perr != nil || time.Since(t) > purchaseIntentTTL {
		// Удаляем именно ту строку, которую прочитали: человек мог нажать
		// новый срок ровно между чтением и удалением.
		_ = st.DeletePurchaseIntentFor(ctx, chatID, in.Months, in.CreatedAt)
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
	if err != nil {
		a.log.Warn("намерение покупки не прочитано перед снятием", "err", err, "user", chatID)
		return
	}
	if in == nil || in.Months != months {
		return
	}
	if err := st.DeletePurchaseIntentFor(ctx, chatID, in.Months, in.CreatedAt); err != nil {
		a.log.Warn("намерение покупки не удалено", "err", err, "user", chatID)
	}
}

// rememberStarsSnapshot кладёт условия сделки в таблицу условий счетов. У
// Stars нет строки в очереди незакрытых счетов (её метод менять нельзя —
// реконсилятор гасит незнакомые), а payload трогать нельзя тем более, поэтому
// снимок живёт отдельной строкой с ключом «человек + способ + срок».
//
// Именно поэтому он НЕ лежит в намерении покупки: счёт из мини-аппа перебивал
// бы выбор, сделанный в чате, а снятие выбора после покупки стирало бы условия
// ещё не оплаченного счёта.
func (a *App) rememberStarsSnapshot(ctx context.Context, chatID int64, months int, snap *model.PlanSnapshot) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil || snap == nil || months <= 0 {
		return
	}
	if err := st.SetInvoiceSnapshot(ctx, chatID, model.PayMethodStars, months, snap); err != nil {
		a.log.Warn("условия счёта Stars не сохранены", "err", err, "user", chatID)
	}
}

// starsSnapshot достаёт условия сделки, снятые при отправке счёта Stars.
//
// Строку тут НЕ удаляем, хотя соблазн есть. Чтение идёт до финализации, и
// удаление при чтении теряло проданные условия сразу в трёх случаях: панель
// упала и оплату добивает повторная доставка апдейта; на один срок выставлено
// два счёта и оплачены оба; Telegram переприслал старую оплату, а в чате висит
// новый неоплаченный счёт. Строк на человека не больше, чем сроков в витрине,
// и каждый новый счёт перезаписывает свою — расти этой таблице некуда.
func (a *App) starsSnapshot(ctx context.Context, chatID int64, months int) *model.PlanSnapshot {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil || months <= 0 {
		return nil
	}
	snap, at, err := st.InvoiceSnapshot(ctx, chatID, model.PayMethodStars, months)
	if err != nil {
		a.log.Warn("условия счёта Stars не прочитаны", "err", err, "user", chatID)
		return nil
	}
	// Срока годности у условий счёта нет намеренно: счёт Stars в переписке
	// остаётся оплачиваемым, предпроверка пропускает его по цене в звёздах, и
	// применить к такой оплате «сегодняшние» условия вместо проданных значило
	// бы выдать не то, за что заплатили. Строки убирает фоновая чистка — с
	// запасом, который счёт заведомо не переживает.
	_ = at
	return snap
}

// buyMonthsOrAsk возвращает выбранный срок. Если выбора нет — показывает
// витрину заново и возвращает 0.
//
// Раньше на этом месте стоял фолбэк «считаем, что месяц»: человек, нажавший
// способ оплаты на старом экране из истории чата, молча получал счёт на месяц
// вместо выбранного года. Угадывать срок за человека нельзя — ни в его пользу,
// ни в свою. Теперь это обёртка над saleOrAsk: способы оплаты работают с
// продажей целиком (тариф + срок), а не с одним числом месяцев.
func (a *App) buyMonthsOrAsk(ctx context.Context, chatID int64) int {
	s := a.saleOrAsk(ctx, chatID)
	if s == nil {
		return 0
	}
	return s.Months
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

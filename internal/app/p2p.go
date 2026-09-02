package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
)

func (a *App) saveBotConfig(ctx context.Context) error {
	if err := a.saveConfigOnly(ctx); err != nil {
		return err
	}
	// Сохранение конфига — момент, когда тариф и сетка цен могли разойтись,
	// поэтому синхронизация висит здесь, а не на каждом из девяти десятков
	// вызывающих. Направление выбирает флаг тарифа: ведомый «Базовый»
	// пересобирается из сетки, ведущий — зеркалит сетку собой
	// (см. internal/app/plans_sync.go).
	if _, err := a.syncPlansConfig(ctx); err != nil {
		a.log.Warn("тариф «Базовый» не синхронизирован", "err", err)
	}
	return nil
}

// saveConfigOnly пишет конфиг в базу БЕЗ синхронизации тарифа. Отдельно от
// saveBotConfig, потому что зеркало (plans_sync.go) сохраняет конфиг, уже держа
// замок тарифов, — хвост с синхронизацией взял бы его повторно.
func (a *App) saveConfigOnly(ctx context.Context) error {
	// В хранилище едет копия, снятая под замком: раньше туда уходил живой
	// конфиг, и обход его карт (JSON) шёл уже без замка — одновременно с записью
	// в те же карты из админки, а это смерть процесса без возможности перехвата
	// (см. model.BotConfig.SnapshotJSON).
	//
	// Снимок и запись идут под cfgSaveMu: снимок без сериализации записи ничего
	// не гарантирует — два сохранения могли доехать до базы в обратном порядке, и
	// в базе оставалось состояние, снятое раньше. Сохранение конфига зовут и
	// фоновые пути (проверка обновлений, ротация карт при заявке на перевод),
	// так что «оба сохранения из одного обработчика» здесь неверно.
	a.cfgSaveMu.Lock()
	defer a.cfgSaveMu.Unlock()
	a.mu.Lock()
	st := a.store
	raw, err := a.botCfg.SnapshotJSON()
	a.mu.Unlock()
	// Разбор снимка обратно в конфиг — уже без общего замка бота: под ним же
	// лежат хранилище, панель и состояния экранов, и берут его на каждом
	// сообщении.
	cfg, derr := model.ConfigFromJSON(raw)
	if err == nil {
		err = derr
	}
	if err != nil {
		return fmt.Errorf("копия конфига: %w", err)
	}
	if cfg == nil || st == nil {
		return fmt.Errorf("бот не настроен")
	}
	return st.SaveConfig(ctx, cfg)
}

func (a *App) p2pConfig() model.P2PConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.P2PConfig{}
	}
	return a.botCfg.P2P
}

// p2pOpenForAll сообщает, выдаются ли реквизиты перевода всем без ручного
// одобрения админом (опция «включить всем» на экране настроек P2P).
func (a *App) p2pOpenForAll() bool {
	cfg := a.p2pConfig()
	return cfg.Enabled && cfg.OpenForAll
}

// p2pAllowed сообщает, можно ли этому пользователю выдать реквизиты: либо
// перевод открыт всем, либо админ одобрил конкретного пользователя.
func (a *App) p2pAllowed(u *model.User) bool {
	if a.p2pOpenForAll() {
		return true
	}
	return u != nil && u.P2PApproved
}

// showPlans — витрина: список доступных покупателю тарифов. Единственный
// видимый тариф открывается сразу карточкой (без лишнего клика — так живёт
// типичная установка с одним «Базовым»).
func (a *App) showPlans(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)

	if a.trialLockNotice(ctx, chatID) {
		return
	}

	if a.legalRequired(ctx, chatID) {
		a.askLegal(ctx, chatID)
		return
	}
	// Первая точка гейта доступности — сама витрина: она строится только из
	// тарифов, доступных этому покупателю. Создание счёта и финализация
	// проверяют то же самое ещё раз.
	plans, anySellable, err := a.storefrontPlans(ctx, chatID)
	if err != nil {
		a.log.Warn("витрина: тарифы не прочитаны", "err", err, "user", chatID)
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	switch {
	case len(plans) == 1:
		a.showPlanOfferView(ctx, chatID, &plans[0], offerView{})
		return
	case len(plans) == 0 && anySellable:
		// Продаваемые тарифы есть, но все закрыты от этого покупателя.
		a.sendKB(ctx, chatID, i18n.T(lang, "buy.not_available"), [][]models.InlineKeyboardButton{homeRow(lang)})
		return
	case len(plans) == 0:
		a.sendKB(ctx, chatID, i18n.T(lang, "buy.no_plans"), [][]models.InlineKeyboardButton{homeRow(lang)})
		return
	}

	var rows [][]models.InlineKeyboardButton
	for i := range plans {
		p := &plans[i]
		label := planTitle(lang, p)
		if from := planMinPrice(p); from != "" {
			label += " · " + i18n.T(lang, "buy.from_price", from+curSuffix(planCurrencyOr(p, a.pricing().Currency)))
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "plo:"+p.Code)})
	}
	rows = append(rows, homeRow(lang))
	a.sendKBSection(ctx, chatID, assets.SectionBuySubscription, i18n.T(lang, "buy.choose_tariff"), rows)
}

// storefrontPlans — тарифы, которые видит этот покупатель: включённые, с
// продаваемыми сроками, доступные ему и не скрытые «по ссылке». anySellable
// говорит, есть ли вообще продаваемые тарифы (чтобы отличать пустую витрину
// от закрытой).
func (a *App) storefrontPlans(ctx context.Context, tgID int64) (visible []model.Plan, anySellable bool, err error) {
	plans, err := a.planList(ctx)
	if err != nil {
		return nil, false, err
	}
	// Строки «Базового» может не быть только из-за сбоя стартовой синхронизации
	// (или в тестах с чистым хранилищем) — витрина при этом не имеет права
	// пустеть: собираем его из сетки конфига, как это делает editPlanPricing.
	hasBase := false
	for i := range plans {
		if plans[i].Code == model.PlanCodeBase {
			hasBase = true
			break
		}
	}
	if !hasBase {
		a.mu.Lock()
		syn := basePlanFrom(a.botCfg, nil)
		a.mu.Unlock()
		if syn != nil {
			plans = append([]model.Plan{*syn}, plans...)
		}
	}
	for i := range plans {
		p := &plans[i]
		if !p.Enabled || !planSellsAnything(p) {
			continue
		}
		anySellable = true
		if model.NormalizeAvailability(p.Availability) == model.PlanAvailLink {
			continue
		}
		if !a.planAccessibleFor(ctx, p, tgID) {
			continue
		}
		visible = append(visible, *p)
	}
	return visible, anySellable, nil
}

// planSellsAnything — есть ли у тарифа хоть один продаваемый срок.
func planSellsAnything(p *model.Plan) bool {
	for i := range p.Durations {
		if p.Durations[i].Months > 0 && p.Durations[i].Base != "" {
			return true
		}
	}
	return false
}

// planMinPrice — минимальная базовая цена тарифа для подписи «от …».
func planMinPrice(p *model.Plan) string {
	best := int64(-1)
	out := ""
	for i := range p.Durations {
		d := &p.Durations[i]
		if d.Months <= 0 || d.Base == "" {
			continue
		}
		k, ok := rubToKopecks(d.Base)
		if !ok {
			continue
		}
		if best < 0 || k < best {
			best = k
			out = d.Base
		}
	}
	return out
}

// planCurrencyOr — валюта тарифа или запасная (валюта сетки).
func planCurrencyOr(p *model.Plan, fallback string) string {
	if p.Currency != "" {
		return p.Currency
	}
	return fallback
}

const popularThreshold = 10

func (a *App) onBuyPlan(ctx context.Context, chatID int64, val string) {
	mo, err := strconv.Atoi(val)
	if err != nil || !a.periodOnSale(mo) || !a.baseSaleAllowed(ctx, chatID) {
		// Кнопка витрины присылает только сроки, которые продаются. Всё
		// остальное — либо подделанные callback-данные (на postgres такое ещё
		// и не влезает в колонку), либо нажатие на старом экране по сроку,
		// который админ снял с продажи, либо тариф, недоступный этому
		// покупателю (вторая точка гейта — витрина покажет отказ сама).
		a.showPlans(ctx, chatID)
		return
	}
	// Выбор срока пишем в базу: экран со способами оплаты переживает рестарт
	// бота, и память процесса тут не носитель (см. internal/app/plans.go).
	//
	// Не записалось — дальше не идём. Экран способов подписан ценами, и
	// показать его после несостоявшейся записи значит предложить оплату по
	// прошлому выбору: человек нажал «1 месяц», а счёт выставился бы на год.
	if err := a.setBuyIntent(ctx, chatID, model.PlanCodeBase, mo); err != nil {
		a.log.Warn("намерение покупки не сохранено", "err", err, "user", chatID)
		a.sendHome(ctx, chatID, i18n.T(a.lang(chatID), "err.storage"))
		return
	}
	a.showMethods(ctx, chatID, mo)
}

// showMethods рисует экран способов оплаты для ЯВНО переданного срока — тот же
// срок, который только что записан в намерение покупки. Перечитывать его из
// базы здесь нельзя: расхождение между подписью кнопок и тем, что уйдёт в счёт,
// — это молчаливая продажа не того срока.
func (a *App) showMethods(ctx context.Context, chatID int64, months int) {
	a.showMethodsSale(ctx, chatID, baseSale(months))
}

// showMethodsSale — тот же экран для явно переданной продажи: у тарифа по
// ссылке цены берутся из его строки, у «Базового» — из сетки конфига.
func (a *App) showMethodsSale(ctx context.Context, chatID int64, s *sale) {
	lang := a.lang(chatID)
	a.mu.Lock()
	var p2p model.P2PConfig
	var stars model.StarsConfig
	var yk model.YooKassaConfig
	var cb model.CryptoBotConfig
	if a.botCfg != nil {
		p2p = a.botCfg.P2P
		stars = a.botCfg.Stars
		yk = a.botCfg.YooKassa
		cb = a.botCfg.CryptoBot
	}
	a.mu.Unlock()
	base := a.saleBase(s)
	// Способы, считающие в валюте сетки (баланс, CryptoBot, Heleket, Platega),
	// тариф в другой валюте не продают — их кнопки прячутся, счёт всё равно не
	// выставится.
	gridCur := a.saleGridCurrency(s)

	var rows [][]models.InlineKeyboardButton
	// У каждой кнопки — своя цена: без проверки P2P выдавал бы реквизиты с
	// пустой суммой, а Stars вёл в тупик «оплата звёздами недоступна».
	if p2p.Enabled && base != "" && a.saleFiat(s, model.PayMethodP2P) != "" && gridCur {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "method.p2p_btn"), "method:p2p")})
	}
	if yk.Enabled && base != "" && a.saleFiat(s, model.PayMethodYooKassa) != "" && a.ykSaleCurrencyOK(s) {
		label := i18n.T(lang, "method.yk_btn", a.saleFiat(s, model.PayMethodYooKassa)+curSuffix(a.curFor(model.PayMethodYooKassa)))
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "method:yk")})
	}
	if stars.Enabled && a.saleStars(s) > 0 && base != "" {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "method.stars_btn", a.saleStars(s)), "method:stars")})
	}
	if cb.Enabled && base != "" && gridCur {
		label := i18n.T(lang, "method.cb_btn", base+curSuffix(curRUB))
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "method:cb")})
	}
	if a.plConfig().Enabled && a.saleFiat(s, model.PayMethodPlatega) != "" && gridCur {
		label := i18n.T(lang, "method.pl_btn", a.saleFiat(s, model.PayMethodPlatega)+curSuffix(curRUB))
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "method:pl")})
	}
	if a.hlConfig().Enabled && base != "" && gridCur {
		label := i18n.T(lang, "method.hl_btn", base+curSuffix(curRUB))
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "method:hl")})
	}
	// Tribute сам определяет период и цену и о выбранном тарифе не знает —
	// кнопка остаётся только у «Базового».
	if s.Plan == nil && a.tributeCfg().Enabled && a.tributeCfg().PayURL != "" {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "method.trb_btn"), "method:trb")})
	}

	bal := a.userBalance(ctx, chatID)
	if k, ok := rubToKopecks(base); ok && k > 0 && bal >= k && gridCur {
		payBtn := []models.InlineKeyboardButton{btn(i18n.T(lang, "balance.btn_pay", kopecksToRub(k)), "method:bal")}
		rows = append([][]models.InlineKeyboardButton{payBtn}, rows...)
	}
	if len(rows) == 0 {
		empty := [][]models.InlineKeyboardButton{}
		if a.topUpEnabled() {
			empty = append(empty, []models.InlineKeyboardButton{btn(i18n.T(lang, "balance.btn_topup"), "menu:topup")})
		}
		a.sendPayKB(ctx, chatID, i18n.T(lang, "buy.no_methods"), append(empty, homeRow(lang)))
		return
	}

	if a.topUpEnabled() {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "balance.btn_topup"), "menu:topup")})
	}
	rows = append(rows, homeRow(lang))
	caption := i18n.T(lang, "buy.choose_method", kopecksToRub(bal))
	// Смена тарифа: показываем зачёт остатка теми же цифрами, которые применит
	// финализация, — человек должен видеть сдвиг срока ДО оплаты.
	if cred := a.switchCreditFor(ctx, chatID, s); cred != 0 {
		caption = i18n.T(lang, "buy.switch_days", cred) + "\n\n" + caption
	}
	if line := a.saleCountriesLine(ctx, lang, s); line != "" {
		caption = line + "\n\n" + caption
	}
	// Документы сервиса на экране оплаты: платёжные провайдеры требуют, чтобы
	// покупатель видел их ДО оплаты, а не только один раз при согласии.
	if foot := a.legalPayFooter(lang); foot != "" {
		caption += "\n\n" + foot
		// У документа без ссылки в приписке остаётся одно название — открыть
		// его должно быть чем. Кнопка встаёт перед «На главную», а не поверх
		// способов оплаты.
		if row := a.legalPayRow(lang); row != nil {
			rows = append(rows[:len(rows)-1], row, rows[len(rows)-1])
		}
	}
	a.sendPayKB(ctx, chatID, caption, rows)
}

func (a *App) onMethod(ctx context.Context, chatID int64, val string) {
	// Вторая точка гейта документов: кнопка способа оплаты могла пролежать в
	// переписке с прошлого раза, а согласие за это время могли сбросить или
	// гейт включить (тот же порядок, что у гейта доступности тарифа).
	if a.legalRequired(ctx, chatID) {
		a.askLegal(ctx, chatID)
		return
	}
	switch val {
	case "bal":
		a.payFromBalance(ctx, chatID)
	case "p2p":
		a.startP2P(ctx, chatID)
	case "stars":
		a.startStars(ctx, chatID)
	case "yk":
		a.startYooKassa(ctx, chatID)
	case "cb":
		a.startCryptoBot(ctx, chatID)
	case "pl":
		a.startPlatega(ctx, chatID)
	case "hl":
		a.startHeleket(ctx, chatID)
	case "trb":
		a.startTribute(ctx, chatID)
	}
}

func (a *App) startP2P(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	_ = a.store.UpsertUser(ctx, chatID)
	u, err := a.store.GetUser(ctx, chatID)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	if !a.p2pAllowed(u) {
		a.sendHome(ctx, chatID, i18n.T(lang, "p2p.need_approval"))
		a.notifyAdminUserRequest(ctx, chatID)
		return
	}
	a.issueCard(ctx, chatID)
}

func (a *App) notifyAdminUserRequest(ctx context.Context, userID int64) {
	lang := a.lang(a.cfg.AdminID)
	id := strconv.FormatInt(userID, 10)
	a.notifyKB(ctx, a.cfg.AdminID, i18n.T(lang, "admin.user_request", a.userLabelByID(ctx, userID)), [][]models.InlineKeyboardButton{{
		btn(i18n.T(lang, "admin.btn_user_ok"), "adm:uok:"+id),
		btn(i18n.T(lang, "admin.btn_user_no"), "adm:uno:"+id),
	}})
}

func (a *App) issueCard(ctx context.Context, chatID int64) {
	s := a.saleOrAsk(ctx, chatID)
	if s == nil {
		return
	}
	a.issueCardSale(ctx, chatID, s)
}

func (a *App) issueCardSale(ctx context.Context, chatID int64, s *sale) {
	lang := a.lang(chatID)
	card, price, reqID, err := a.prepareP2PCardSale(ctx, chatID, s)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	idStr := strconv.FormatInt(reqID, 10)
	a.sendKB(ctx, chatID, i18n.T(lang, "p2p.card", s.Months, price+curSuffix(curRUB), card),
		[][]models.InlineKeyboardButton{{
			btn(i18n.T(lang, "p2p.paid_btn"), "p2p:paid:"+idStr),
			btn(i18n.T(lang, "btn.cancel"), "p2p:cancel:"+idStr),
		}})
}

// prepareP2PCard picks the next card, creates an awaiting P2P request and
// returns the card + price + request id, without messaging the user (shared by
// the chat flow and the web cabinet).
func (a *App) prepareP2PCard(ctx context.Context, chatID int64, months int) (card, price string, reqID int64, err error) {
	return a.prepareP2PCardSale(ctx, chatID, baseSale(months))
}

func (a *App) prepareP2PCardSale(ctx context.Context, chatID int64, s *sale) (card, price string, reqID int64, err error) {
	months := s.Months
	// Цена продажи считается до захвата замка: saleFiat сам берёт a.mu, когда
	// продаётся «Базовый» по сетке.
	price = a.saleFiat(s, model.PayMethodP2P)
	// Заявка и карточка P2P подписаны рублями: тариф в другой валюте переводом
	// не продаётся (кнопка спрятана, это защита от устаревшего экрана).
	if !a.saleGridCurrency(s) {
		return "", "", 0, errors.New("для этого срока не задана цена")
	}
	a.mu.Lock()
	a.botCfg.NormalizePricing()
	p2p := a.botCfg.P2P
	if len(p2p.Cards) == 0 {
		a.mu.Unlock()
		return "", "", 0, errors.New(i18n.T(a.lang(chatID), "p2p.no_cards"))
	}
	if price == "" {
		// Заявка с пустой суммой — это «переведите сколько-нибудь»: человек
		// платит наугад, а админ подтверждает выдачу полного срока. Проверка
		// до ротации карт: иначе отказ сдвигал бы очередь реквизитов впустую.
		a.mu.Unlock()
		return "", "", 0, errors.New("для этого срока не задана цена")
	}
	idx := 0
	if p2p.Rotate && len(p2p.Cards) > 1 {
		idx = p2p.RotateIdx % len(p2p.Cards)
		a.botCfg.P2P.RotateIdx = idx + 1
	}
	card = p2p.Cards[idx]
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)

	if a.store == nil {
		return "", "", 0, errors.New("storage unavailable")
	}
	req := &model.P2PRequest{TelegramID: chatID, Months: months, Price: price, Status: model.P2PAwaiting, Snapshot: a.saleSnapshot(s)}
	if err = a.store.CreateP2PRequest(ctx, req); err != nil {
		return "", "", 0, err
	}
	a.payLog(ctx, model.PayMethodP2P, p2pExt(req.ID), chatID, "request_created", "plan=%s months=%d price=%s", s.planCode(), months, price)
	return card, price, req.ID, nil
}

// sendAdminPhotoUpload forwards an uploaded image (bytes) to the admin chat.
func (a *App) sendAdminPhotoUpload(ctx context.Context, filename string, data []byte, caption string, rows [][]models.InlineKeyboardButton) {
	photo := &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(data)}
	_, _ = a.msg.SendBanner(ctx, a.cfg.AdminID, photo, caption, nil, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// sendAdminDocUpload уводит админу загруженный из кабинета чек-файл (PDF).
func (a *App) sendAdminDocUpload(ctx context.Context, filename string, data []byte, caption string, rows [][]models.InlineKeyboardButton) {
	doc := &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(data)}
	a.msg.SendDocumentKB(ctx, a.cfg.AdminID, doc, caption, &models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (a *App) onP2PUser(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	id, _ := strconv.ParseInt(arg, 10, 64)
	switch action {
	case "paid":
		if id == 0 {
			return
		}
		a.getUI(chatID).awaitShotReq = id
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "p2p.send_screenshot"))
	case "cancel":
		if id != 0 && a.store != nil {
			if r, e := a.store.GetP2PRequest(ctx, id); e == nil && r != nil && r.TelegramID == chatID &&
				(r.Status == model.P2PAwaiting || r.Status == model.P2PSubmitted) {
				r.Status = model.P2PRejected
				_ = a.store.UpdateP2PRequest(ctx, r)
				a.payLog(ctx, model.PayMethodP2P, p2pExt(id), chatID, "cancelled", "отменено пользователем")
			}
		}
		a.getUI(chatID).awaitShotReq = 0
		a.showMenu(ctx, chatID, chatID == a.cfg.AdminID, a.displayNameByID(ctx, chatID))
	}
}

func (a *App) handlePhoto(ctx context.Context, m *models.Message) {
	chatID := m.Chat.ID
	ui := a.getUI(chatID)
	if ui.awaitSectionBanner != "" {
		section := ui.awaitSectionBanner
		ui.awaitSectionBanner = ""
		a.setSectionBannerFile(ctx, chatID, section, m.Photo[len(m.Photo)-1].FileID)
		return
	}
	if ui.welcomeAwait == "img" {
		a.setWelcomeImageFile(ctx, chatID, m.Photo[len(m.Photo)-1].FileID)
		return
	}
	a.submitP2PReceipt(ctx, m, m.Photo[len(m.Photo)-1].FileID, false)
}

// handleP2PDoc принимает чек об оплате, присланный файлом, а не фотографией:
// PDF из банковского приложения или ту же картинку, отправленную «без сжатия».
// Возвращает true, если сообщение разобрано здесь и дальше его вести не нужно.
func (a *App) handleP2PDoc(ctx context.Context, m *models.Message) bool {
	if m.Document == nil {
		return false
	}
	chatID := m.Chat.ID
	if a.getUI(chatID).awaitShotReq == 0 {
		return false
	}
	mime := strings.ToLower(m.Document.MimeType)
	name := strings.ToLower(m.Document.FileName)
	ok := mime == "application/pdf" || strings.HasSuffix(name, ".pdf") ||
		strings.HasPrefix(mime, "image/") || hasImageExt(name)
	if !ok {
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "p2p.bad_receipt"))
		return true
	}
	// Даже картинку-файлом пересылаем админу документом: file_id документа
	// Telegram в sendPhoto не принимает.
	a.submitP2PReceipt(ctx, m, m.Document.FileID, true)
	return true
}

func hasImageExt(name string) bool {
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".heic", ".heif", ".bmp", ".gif"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// submitP2PReceipt закрывает ожидание чека: сохраняет file_id, помечает заявку
// отправленной и уводит её админу. asDoc — чек пришёл файлом (PDF или картинка
// без сжатия), такой отправляем админу документом.
func (a *App) submitP2PReceipt(ctx context.Context, m *models.Message, fileID string, asDoc bool) {
	chatID := m.Chat.ID
	ui := a.getUI(chatID)
	if ui.awaitShotReq == 0 || a.store == nil {
		return
	}
	reqID := ui.awaitShotReq
	req, err := a.store.GetP2PRequest(ctx, reqID)
	if err != nil || req == nil {
		return
	}
	req.Screenshot = fileID
	req.Status = model.P2PSubmitted
	if err := a.store.UpdateP2PRequest(ctx, req); err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	a.payLog(ctx, model.PayMethodP2P, p2pExt(req.ID), chatID, "screenshot_submitted", "ожидает проверки админом")
	ui.awaitShotReq = 0

	ui.p2pShotMsgID = m.ID

	lang := a.lang(chatID)
	ui.p2pSubmitMsgID = a.msg.SendKB(ctx, chatID,
		a.applyPremium(i18n.T(lang, "p2p.submitted")),
		[][]models.InlineKeyboardButton{backHomeRow(lang)})
	a.notifyAdminPayment(ctx, req, fileID, asDoc)
}

func (a *App) notifyAdminPayment(ctx context.Context, req *model.P2PRequest, fileID string, asDoc bool) {
	lang := a.lang(a.cfg.AdminID)
	caption := i18n.T(lang, "admin.payment_caption", a.userLabelByID(ctx, req.TelegramID), req.Months, req.Price+curSuffix(a.curFor(model.PayMethodP2P)), req.ID)
	id := strconv.FormatInt(req.ID, 10)
	rows := [][]models.InlineKeyboardButton{{
		btn(i18n.T(lang, "admin.btn_pay_ok"), "adm:pok:"+id),
		btn(i18n.T(lang, "admin.btn_pay_no"), "adm:pno:"+id),
	}}
	if asDoc {
		a.notifyDoc(ctx, a.cfg.AdminID, &models.InputFileString{Data: fileID}, caption, rows)
		return
	}
	a.notifyPhoto(ctx, a.cfg.AdminID, fileID, caption, rows)
}

func (a *App) showP2PAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	p2p := a.p2pConfig()
	status := i18n.T(lang, "admin.off")
	if p2p.Enabled {
		status = i18n.T(lang, "admin.on")
	}
	rot := i18n.T(lang, "admin.no")
	if p2p.Rotate {
		rot = i18n.T(lang, "admin.yes")
	}
	openAll := i18n.T(lang, "admin.no")
	if p2p.OpenForAll {
		openAll = i18n.T(lang, "admin.yes")
	}
	text := i18n.T(lang, "admin.p2p_title", status, len(p2p.Cards), rot, curRUB, a.formatFiatPrices(model.PayMethodP2P)) +
		i18n.T(lang, "admin.p2p_open_block", openAll)
	a.sendPayKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{toggleBtn(lang, p2p.Enabled, "adm:toggle"), btn(i18n.T(lang, "admin.btn_rotate"), "adm:rotate")},
		{btn(i18n.T(lang, "admin.btn_open_all"), "adm:openall")},
		{btn(i18n.T(lang, "admin.btn_cards"), "adm:cards"), btn(i18n.T(lang, "admin.btn_prices"), "adm:prices")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onAdmin(ctx context.Context, chatID int64, val string, srcMsgID int) {
	action, arg, _ := strings.Cut(val, ":")

	switch action {
	case "uok", "uno", "pok", "pno", "wok", "wno":
		if srcMsgID != 0 {
			a.msg.Delete(ctx, chatID, srcMsgID)
		}
	}
	switch action {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.P2P.Enabled = !a.botCfg.P2P.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showP2PAdmin(ctx, chatID)
	case "openall":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.P2P.OpenForAll = !a.botCfg.P2P.OpenForAll
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showP2PAdmin(ctx, chatID)
	case "rotate":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.P2P.Rotate = !a.botCfg.P2P.Rotate
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showP2PAdmin(ctx, chatID)
	case "cards":
		a.getUI(chatID).adminInput = "cards"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_cards"), "menu:p2p")
	case "cur":
		a.getUI(chatID).adminInput = "p2p_cur"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_currency"), "menu:p2p")
	case "prices":
		a.adminAskPriceMonth(ctx, chatID)
	case "price":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "price"
		ui.priceMonths = mo
		// Старый экран правит «Базовый»: контекст карточки тарифа здесь чужой.
		ui.planCode = ""
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_price", mo), "menu:p2p")
	case "uok":
		a.adminApproveUser(ctx, chatID, arg, true)
	case "uno":
		a.adminApproveUser(ctx, chatID, arg, false)
	case "wok":
		a.adminApproveWebUser(ctx, chatID, arg, true)
	case "wno":
		a.adminApproveWebUser(ctx, chatID, arg, false)
	case "pok":
		a.adminApprovePayment(ctx, chatID, arg)
	case "pno":
		id, _ := strconv.ParseInt(arg, 10, 64)
		a.getUI(chatID).rejectReq = id
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_reason"))
	}
}

func (a *App) adminAskPriceMonth(ctx context.Context, chatID int64) {
	var row []models.InlineKeyboardButton
	for _, mo := range model.PlanMonths {
		row = append(row, btn(strconv.Itoa(mo)+"м", "adm:price:"+strconv.Itoa(mo)))
	}
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, i18n.T(lang, "admin.ask_price_month"), [][]models.InlineKeyboardButton{row, navBack(lang, "menu:p2p")})
}

func (a *App) adminApproveUser(ctx context.Context, adminChat int64, arg string, ok bool) {
	uid, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return
	}
	alang := a.lang(adminChat)
	if !ok {
		a.sendHome(ctx, adminChat, i18n.T(alang, "admin.user_denied"))
		return
	}
	if err := a.store.SetP2PApproved(ctx, uid, true); err != nil {
		a.sendHome(ctx, adminChat, "❌ "+err.Error())
		return
	}
	a.notify(ctx, uid, i18n.T(a.lang(uid), "p2p.user_approved"))
	a.sendHome(ctx, adminChat, i18n.T(alang, "admin.user_ok_done"))
}

func (a *App) adminApprovePayment(ctx context.Context, adminChat int64, arg string) {
	alang := a.lang(adminChat)
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return
	}
	req, err := a.store.GetP2PRequest(ctx, id)
	if err != nil || req == nil || req.Status != model.P2PSubmitted {
		a.sendHome(ctx, adminChat, i18n.T(alang, "admin.not_found"))
		return
	}
	amount := req.Price + curSuffix(a.curFor(model.PayMethodP2P))
	req.Status = model.P2PApproved
	req.DecidedAt = time.Now().UTC().Format(time.RFC3339)
	if err := a.store.UpdateP2PRequest(ctx, req); err != nil {
		a.sendHome(ctx, adminChat, "❌ "+err.Error())
		return
	}
	a.payLog(ctx, model.PayMethodP2P, p2pExt(req.ID), req.TelegramID, "approved", "подтверждено администратором")
	link, expireAt, err := a.finalizePurchase(ctx, req.TelegramID, req.Months, model.PayMethodP2P, amount, p2pExt(req.ID), req.Snapshot)
	if err != nil {
		if errors.Is(err, storage.ErrDuplicateExtID) {
			a.sendHome(ctx, adminChat, i18n.T(alang, "admin.done"))
			return
		}
		req.Status = model.P2PSubmitted
		req.DecidedAt = ""
		_ = a.store.UpdateP2PRequest(ctx, req)
		a.sendHome(ctx, adminChat, i18n.T(alang, "admin.provision_fail", err.Error()))
		return
	}
	a.cleanupP2PUser(ctx, req.TelegramID)
	a.sendSubActive(ctx, req.TelegramID, link, expireAt)
	a.sendHome(ctx, adminChat, i18n.T(alang, "admin.done"))
}

const finalizeLockShards = 64

func extLockIndex(s string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32() % finalizeLockShards)
}

// planSnapshot фиксирует условия, на которых сейчас продаётся срок months:
// лимиты, сквады и цену. Снимок снимается в момент ВЫСТАВЛЕНИЯ счёта и потом
// применяется при финализации — иначе правка конфига между «нажал купить» и
// «оплатил» меняла бы человеку условия задним числом.
func (a *App) planSnapshot(months int) *model.PlanSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.planSnapshotLocked(months)
}

func (a *App) planSnapshotLocked(months int) *model.PlanSnapshot {
	s := &model.PlanSnapshot{Months: months}
	s.Code, s.Name = a.basePlanIdentLocked()
	if a.botCfg == nil {
		return s
	}
	pr := a.botCfg.Pricing
	// Цепочка сквадов повторяет исторический порядок: глобальный набор →
	// одиночный сквад P2P (легаси) → набор, заданный для конкретного срока.
	s.IntSquads = append([]string(nil), a.botCfg.Plan.ActiveInternalSquads...)
	s.ExtSquad = a.botCfg.Plan.ExternalSquadUUID
	if len(s.IntSquads) == 0 && a.botCfg.P2P.SquadUUID != "" {
		s.IntSquads = []string{a.botCfg.P2P.SquadUUID}
	}
	if sq := pr.SquadsInt[months]; len(sq) > 0 {
		s.IntSquads = append([]string(nil), sq...)
	}
	if e := pr.SquadsExt[months]; e != "" {
		s.ExtSquad = e
	}
	s.TrafficGB = pr.Traffic[months]
	s.DeviceLimit = pr.DeviceLimitFor(months)
	s.Strategy = pr.ResetStrategy()
	// Продана ли доп-подписка — часть условий сделки. Прямо из конфига и копии
	// «Базового», без addSubParams: мы под a.mu.
	sold := a.botCfg.AddSub.Enabled
	if a.basePlanRef != nil && model.NormalizeAddSubMode(a.basePlanRef.AddSub) == model.PlanAddSubOff {
		sold = false
	}
	s.AddSub = &sold
	s.Price = pr.Base[months]
	s.Currency = pr.Currency
	return s
}

// pendingSnapshot достаёт условия сделки из незакрытого счёта. Именно эта
// строка — носитель снимка для всех внешних платёжек: payload провайдера мы
// намеренно не меняем, чтобы предыдущий образ бота продолжал его понимать.
func (a *App) pendingSnapshot(ctx context.Context, extID string) *model.PlanSnapshot {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil || extID == "" {
		return nil
	}
	p, _ := st.PendingByExtID(ctx, extID)
	if p == nil {
		return nil
	}
	return p.Snapshot
}

// finalizePurchase выдаёт или продлевает подписку по оплаченному счёту. snap —
// условия сделки, снятые при выставлении счёта; nil означает «снять по
// текущему конфигу» (так ведут себя пути, где счёта не было, и строки,
// созданные до появления снимков).
func (a *App) finalizePurchase(ctx context.Context, telegramID int64, months int, method, amount, extID string, snap *model.PlanSnapshot) (string, string, error) {
	link, expireAt, applied, err := a.finalizePurchaseCore(ctx, telegramID, months, method, amount, extID, snap)
	if err == nil {
		// Доп-подписка применяется ПОСЛЕ ядра: это ещё один поход в панель, а
		// ядро держит шардовый замок финализации — лишний HTTP-раунд под ним
		// тормозил бы доставку соседних оплат (у предпроверки Stars дедлайн).
		// Upsert идемпотентен, повторная доставка платежа безопасна. Оплата
		// уже продлила подписку и сбросила трафик A — трафик B идёт следом.
		a.syncAddSubSnap(ctx, telegramID, true, applied)
		// Автосписание следует за последней сделкой: купил другой тариф любым
		// способом — продлеваться должен ОН, а не молча возвращаться прежний.
		a.autoPayFollowPurchase(ctx, telegramID, applied)
		// Третья точка гейта доступности: счёт оплачен, а тариф к этому моменту
		// стал покупателю недоступен. Подписка уже выдана по снимку (решение
		// владельца: деньги приняты — клиент получает то, за что платил), админ
		// узнаёт и разбирается. Вызов ПОСЛЕ ядра — по той же причине, что и
		// доп-подписка: чтения базы и поход в Telegram под замком не живут.
		a.notifyPlanGateBreach(ctx, telegramID, applied)
	}
	return link, expireAt, err
}

func (a *App) finalizePurchaseCore(ctx context.Context, telegramID int64, months int, method, amount, extID string, snap *model.PlanSnapshot) (string, string, *model.PlanSnapshot, error) {
	// Serialize duplicate deliveries of the same payment and bail before we touch
	// the panel if it's already been finalized (the panel extend happens below,
	// before the AddPayment idempotency barrier, so without this two concurrent
	// deliveries would each extend the subscription).
	if extID != "" {
		lk := &a.finalizeLk[extLockIndex(extID)]
		lk.Lock()
		defer lk.Unlock()
		if a.store != nil {
			if done, _ := a.store.PaymentByExtID(ctx, extID); done {
				a.payLog(ctx, method, extID, telegramID, "duplicate", "платёж уже финализирован — пропуск")
				return "", "", nil, storage.ErrDuplicateExtID
			}
		}
	}
	// Пер-пользовательская сериализация (строго ПОСЛЕ шардов ext_id): два
	// разных платежа одного человека, финализируясь параллельно, считали бы
	// зачёт остатка от одного снимка — и конвертировали бы его дважды.
	ulk := &a.finalizeUserLk[extLockIndex(strconv.FormatInt(telegramID, 10))]
	ulk.Lock()
	defer ulk.Unlock()
	a.mu.Lock()
	panel := a.panel
	if snap == nil {
		snap = a.planSnapshotLocked(months)
	}
	a.mu.Unlock()
	// Срок из счёта главнее того, что записано в снимке: снимок мог быть снят
	// на другой срок только из-за ошибки, и лишний рассинхрон здесь опаснее,
	// чем расхождение внутри снимка.
	snap.Months = months
	// Фактически уплаченная цена — в снимок: по ней считается зачёт остатка
	// при БУДУЩЕЙ смене тарифа (скидочные переопределения способа не должны
	// зачитываться по полной цене).
	if paid := paidRub(amount); paid != "" {
		snap.Paid = paid
	}
	limits := remnawave.UserLimits{
		InternalSquads: snap.IntSquads,
		ExternalSquad:  snap.ExtSquad,
		TrafficBytes:   snap.TrafficBytes(),
		// Покупка всегда задаёт трафик: ноль здесь означает «безлимит», как и
		// написано в админке, и обязан уехать в панель нулём.
		TrafficSet:  true,
		DeviceLimit: snap.DeviceLimit,
		Strategy:    snap.Strategy,
	}
	a.payLog(ctx, method, extID, telegramID, "finalize", "months=%d amount=%s", months, amount)
	if panel == nil {
		a.payLog(ctx, method, extID, telegramID, "error", "панель не подключена")
		return "", "", nil, fmt.Errorf("панель не подключена")
	}
	// Смена тарифа: остаток старой сделки зачитывается днями по соотношению
	// цен (см. plans_switch.go). Обычное продление того же тарифа — ноль.
	// Снимок и конец срока читаются один раз: из них же считается оплаченное
	// окно нового снимка (BoughtDays).
	var prevSnap *model.PlanSnapshot
	prevExpire := ""
	if a.store != nil {
		if u, _ := a.store.GetUser(ctx, telegramID); u != nil {
			prevSnap, prevExpire = u.Snapshot, u.SubExpireAt
		}
	}
	extraDays := switchCredit(prevSnap, prevExpire, snap)
	if extraDays != 0 {
		a.payLog(ctx, method, extID, telegramID, "switch_credit", "plan=%s days=%+d", snap.Code, extraDays)
	}
	// Оплаченное окно: остаток прежнего окна (при смене тарифа —
	// конвертированный) плюс купленные месяцы. Бонусные дни сюда не входят.
	snap.BoughtDays = boughtDaysAfter(prevSnap, prevExpire, snap, months, extraDays)
	link, expireAt, err := panel.CreateOrUpdateUser(ctx, telegramID, months, extraDays, limits)
	if err != nil {
		a.payLog(ctx, method, extID, telegramID, "panel_error", "%v", err)
		return "", "", nil, err
	}
	a.payLog(ctx, method, extID, telegramID, "panel_ok", "expire=%s", expireAt)
	link = a.rewriteSub(link)
	a.invalidateSubCache(telegramID)
	if a.store != nil {
		err := a.store.AddPayment(ctx, &model.Payment{
			TelegramID: telegramID, Method: method, Months: months, Amount: amount, Status: model.PaymentPaid, ExtID: extID,
			Snapshot: snap,
		})
		// Запись платежа — барьер идемпотентности: по ней PaymentByExtID решает,
		// финализировать ли повторно. Транзиентный сбой (database is locked,
		// обрыв соединения) пробуем пережить повтором.
		for i := 0; i < 2 && err != nil && !errors.Is(err, storage.ErrDuplicateExtID); i++ {
			time.Sleep(200 * time.Millisecond)
			err = a.store.AddPayment(ctx, &model.Payment{
				TelegramID: telegramID, Method: method, Months: months, Amount: amount, Status: model.PaymentPaid, ExtID: extID,
				Snapshot: snap,
			})
		}
		if err != nil {
			if errors.Is(err, storage.ErrDuplicateExtID) && extID != "" {
				a.payLog(ctx, method, extID, telegramID, "duplicate", "платёж с этим ext_id уже записан")
				return "", "", nil, err
			}
			// Панель уже продлила подписку, а барьер записать не удалось. Раньше
			// это молча глоталось — и реконсилятор, не видя платежа, продлевал бы
			// подписку СНОВА каждые две минуты до суточного предела. Гасим pending,
			// чтобы остановить цикл, и зовём админа сверить платёж вручную.
			a.payLog(ctx, method, extID, telegramID, "error", "подписка выдана, но платёж не записался (идемпотентность нарушена): %v", err)
			a.log.Error("add payment failed after retries", "err", err, "ext", extID)
			if extID != "" {
				if p, _ := a.store.PendingByExtID(ctx, extID); p != nil {
					_ = a.store.ResolvePending(ctx, p.ID)
				}
			}
			alang := a.lang(a.cfg.AdminID)
			a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "admin.payment_unrecorded", methodLabel(method), extID, a.userLabelByID(ctx, telegramID), amount))
		}
		_ = a.store.SetSubExpiry(ctx, telegramID, expireAt, "paid")
		// Снимок действующей подписки: локальной сущности подписки у бота нет,
		// а знать проданные условия нужно и сверке лимитов, и бонусным дням.
		_ = a.store.SetUserSnapshot(ctx, telegramID, snap)
	}
	a.payLog(ctx, method, extID, telegramID, "done", "подписка выдана, ссылка отправляется")
	// Выбор срока отработал — убираем его, иначе кнопка способа оплаты на
	// старом экране продаст тот же срок ещё раз, минуя витрину.
	a.forgetBuyIntentFor(ctx, telegramID, months)
	a.grantReferralBonus(ctx, telegramID)
	a.creditReferralPercent(ctx, telegramID, amount)
	// Чек «Мой налог» — только по платежам ЮKassa: крипта и P2P в чек
	// самозанятого не идут, а оплата с баланса учтена при пополнении.
	if method == model.PayMethodYooKassa {
		a.fiscalize(parseAmountRub(amount), fmt.Sprintf("Подписка %d мес.", months))
	}
	return link, expireAt, snap, nil
}

func (a *App) handleAdminText(ctx context.Context, chatID int64, text string) {
	ui := a.getUI(chatID)
	lang := a.lang(chatID)

	if ui.rejectReq != 0 {
		id := ui.rejectReq
		ui.rejectReq = 0
		req, err := a.store.GetP2PRequest(ctx, id)
		if err != nil || req == nil {
			a.sendHome(ctx, chatID, i18n.T(lang, "admin.not_found"))
			return
		}
		req.Status = model.P2PRejected
		req.Comment = text
		req.DecidedAt = time.Now().UTC().Format(time.RFC3339)
		_ = a.store.UpdateP2PRequest(ctx, req)
		a.payLog(ctx, model.PayMethodP2P, p2pExt(req.ID), req.TelegramID, "rejected", "%s", text)
		_ = a.store.AddPayment(ctx, &model.Payment{
			TelegramID: req.TelegramID, Method: model.PayMethodP2P, Months: req.Months,
			Amount: req.Price + curSuffix(a.curFor(model.PayMethodP2P)), Status: model.PaymentRejected, Comment: text,
		})
		a.cleanupP2PUser(ctx, req.TelegramID)
		a.notify(ctx, req.TelegramID, i18n.T(a.lang(req.TelegramID), "p2p.user_paid_rejected", text))
		a.sendHome(ctx, chatID, i18n.T(lang, "admin.done"))
		return
	}

	switch ui.adminInput {
	case "plan_name", "plan_desc", "plan_icon", "plan_addsub_name", "plan_addsub_desc":
		a.applyPlanText(ctx, chatID, ui.adminInput, text)
	case "plan_access":
		a.applyPlanAccessInput(ctx, chatID, text)
	case "cards":
		ui.adminInput = ""
		cards := splitTrim(text, ";")
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.P2P.Cards = cards
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showP2PAdmin(ctx, chatID)
	case "price":
		mo := ui.priceMonths
		ui.adminInput = ""
		ui.priceMonths = 0
		code := ui.planCode
		ui.planCode = ""
		if err := a.setPlanPrice(ctx, code, mo, "p2p", text); err != nil {
			a.planInputFailed(ctx, chatID, err)
			return
		}
		if code != "" {
			a.showPlanMonth(ctx, chatID, code, mo)
			return
		}
		a.showP2PAdmin(ctx, chatID)
	case "starprice":
		mo := ui.priceMonths
		code := ui.planCode
		ui.adminInput = ""
		ui.priceMonths = 0
		ui.planCode = ""
		// Строгий разбор: раньше «100 ⭐» молча превращалось в 0 и стирало цену.
		v, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || v < 0 {
			a.sendPayKB(ctx, chatID, i18n.T(a.lang(chatID), "stars.bad_price", text),
				[][]models.InlineKeyboardButton{navBack(a.lang(chatID), "menu:stars")})
			return
		}
		if err := a.setPlanStars(ctx, code, mo, v); err != nil {
			a.planInputFailed(ctx, chatID, err)
			return
		}
		if code != "" {
			a.showPlanMonth(ctx, chatID, code, mo)
			return
		}
		a.showStarsAdmin(ctx, chatID)
	case "baseprice":
		mo := ui.priceMonths
		code := ui.planCode
		ui.adminInput = ""
		ui.priceMonths = 0
		ui.planCode = ""
		if err := a.setPlanPrice(ctx, code, mo, "base", text); err != nil {
			a.planInputFailed(ctx, chatID, err)
			return
		}
		a.afterPlanPriceEdit(ctx, chatID, code, mo)
	case "currency":
		code := ui.planCode
		ui.adminInput = ""
		ui.planCode = ""
		if err := a.setPlanCurrency(ctx, code, text); err != nil {
			a.planInputFailed(ctx, chatID, err)
			return
		}
		a.afterPlanPriceEdit(ctx, chatID, code, 0)
	case "p2p_cur":
		ui.adminInput = ""
		v := strings.TrimSpace(text)
		if v == "-" {
			v = ""
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.P2P.Currency = v
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showP2PAdmin(ctx, chatID)
	case "yk_cur":
		ui.adminInput = ""
		v := strings.TrimSpace(text)
		if v == "-" {
			v = ""
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.YooKassa.Currency = v
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooKassaAdmin(ctx, chatID)
	case "ykprice":
		mo := ui.priceMonths
		ui.adminInput = ""
		ui.priceMonths = 0
		code := ui.planCode
		ui.planCode = ""
		if err := a.setPlanPrice(ctx, code, mo, "yk", text); err != nil {
			a.planInputFailed(ctx, chatID, err)
			return
		}
		if code != "" {
			a.showPlanMonth(ctx, chatID, code, mo)
			return
		}
		a.showYooKassaAdmin(ctx, chatID)
	case "yk_shop":
		ui.adminInput = ""
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.YooKassa.ShopID = strings.TrimSpace(text)
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooKassaAdmin(ctx, chatID)
	case "yk_secret":
		ui.adminInput = ""
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.YooKassa.SecretKey = strings.TrimSpace(text)
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooKassaAdmin(ctx, chatID)
	case "yk_return":
		ui.adminInput = ""
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.YooKassa.ReturnURL = strings.TrimSpace(text)
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooKassaAdmin(ctx, chatID)
	case "yk_autodays":
		ui.adminInput = ""
		d, _ := strconv.Atoi(strings.TrimSpace(text))
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.YooKassa.AutoPayDays = d
			a.botCfg.NormalizeYooKassa()
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooKassaAdmin(ctx, chatID)
	case "inv_days":
		ui.adminInput = ""
		a.createInviteDays(ctx, chatID, text)
	case "inv_uses":
		ui.adminInput = ""
		a.createInviteUses(ctx, chatID, text)
	case "subdomain":
		a.setSubdomain(ctx, chatID, text)
	case "wh_addr":
		text = strings.TrimSpace(text)
		// Accept a bare port ("18080") or a full bind addr (":18080",
		// "0.0.0.0:18080"); normalize a bare number to ":port".
		if text != "" && !strings.Contains(text, ":") {
			text = ":" + text
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Webhook.ListenAddr = text
		}
		a.mu.Unlock()
		ui.adminInput = ""
		_ = a.saveBotConfig(ctx)
		// Apply the new port itself: rewrite compose (127.0.0.1:port:port) and
		// recreate the container, so the admin doesn't touch compose by hand.
		a.applyBotPort(ctx, chatID)
	case "wh_base":
		text = normalizeBaseURL(text)
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Webhook.PublicBaseURL = text
		}
		a.mu.Unlock()
		ui.adminInput = ""
		_ = a.saveBotConfig(ctx)
		a.showWebhooksAdmin(ctx, chatID)
	case "wh_secret":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Webhook.RemnawaveSecret = strings.TrimSpace(text)
		}
		a.mu.Unlock()
		ui.adminInput = ""
		_ = a.saveBotConfig(ctx)
		a.showWebhooksAdmin(ctx, chatID)
	case "cb_token":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.CryptoBot.Token = strings.TrimSpace(text)
		}
		a.mu.Unlock()
		ui.adminInput = ""
		_ = a.saveBotConfig(ctx)
		a.showCryptoBotAdmin(ctx, chatID)
	case "cb_asset":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.CryptoBot.Asset = strings.ToUpper(strings.TrimSpace(text))
		}
		a.mu.Unlock()
		ui.adminInput = ""
		_ = a.saveBotConfig(ctx)
		a.showCryptoBotAdmin(ctx, chatID)
	case "ctc_group":
		a.setContact(ctx, chatID, "group", text)
	case "ctc_support":
		a.setContact(ctx, chatID, "support", text)
	case "leg_terms_text":
		a.setLegalDoc(ctx, chatID, model.LegalTerms, "text", text)
	case "leg_terms_url":
		a.setLegalDoc(ctx, chatID, model.LegalTerms, "url", text)
	case "leg_privacy_text":
		a.setLegalDoc(ctx, chatID, model.LegalPrivacy, "text", text)
	case "leg_privacy_url":
		a.setLegalDoc(ctx, chatID, model.LegalPrivacy, "url", text)
	case "traffic_gb":
		mo := ui.priceMonths
		code := ui.planCode
		ui.adminInput = ""
		ui.priceMonths = 0
		ui.planCode = ""
		gb, _ := strconv.Atoi(strings.TrimSpace(text))
		if err := a.setPlanTraffic(ctx, code, mo, gb); err != nil {
			a.planInputFailed(ctx, chatID, err)
			return
		}
		a.afterPlanPriceEdit(ctx, chatID, code, mo)
	case "device_limit":
		code := ui.planCode
		ui.adminInput = ""
		ui.priceMonths = 0
		ui.planCode = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if err := a.setPlanDeviceLimit(ctx, code, n); err != nil {
			a.planInputFailed(ctx, chatID, err)
			return
		}
		a.afterPlanPriceEdit(ctx, chatID, code, 0)
	case "bcast":
		ui.adminInput = ""
		a.previewBroadcast(ctx, chatID, text)
	case "promo_create":
		ui.adminInput = ""
		a.createPromoFromText(ctx, chatID, text)
	case "mn_login", "mn_pass", "mn_name":
		field := ui.adminInput
		ui.adminInput = ""
		a.setMoyNalogField(ctx, chatID, field, text)
	case "pl_merchant", "pl_secret", "pl_return":
		field := ui.adminInput
		ui.adminInput = ""
		a.setPlategaField(ctx, chatID, field, text)
	case "hl_merchant", "hl_key", "hl_tocur", "hl_subtract", "hl_lifetime", "hl_return":
		field := ui.adminInput
		ui.adminInput = ""
		a.setHeleketField(ctx, chatID, field, text)
	case "trb_key", "trb_url":
		field := ui.adminInput
		ui.adminInput = ""
		a.setTributeField(ctx, chatID, field, text)
	case "panel_apikey", "panel_cookie":
		field := ui.adminInput
		ui.adminInput = ""
		a.setPanelSecret(ctx, chatID, field, text)
	case "paylog":
		ui.adminInput = ""
		a.adminSendPayLog(ctx, chatID, text)
	case "link_panel":
		uid := ui.linkUID
		ui.adminInput = ""
		ui.linkUID = 0
		a.adminLinkPanel(ctx, chatID, uid, text)
	case "user_find":
		a.applyUserSearch(ctx, chatID, text)
	case "wl_add":
		ui.adminInput = ""
		raw := strings.NewReplacer(",", " ", "\n", " ", ";", " ").Replace(text)
		for _, f := range strings.Fields(raw) {
			id, err := strconv.ParseInt(f, 10, 64)
			if err != nil || id == 0 {
				continue
			}
			if a.store != nil {
				if u, _ := a.store.GetUser(ctx, id); u != nil {
					_ = a.store.SetWhitelisted(ctx, id, true)
				} else {
					_ = a.store.AddWhitelistID(ctx, id)
				}
			}
		}
		a.showWhitelist(ctx, chatID, 0)
	case "wh_domain":
		ui.adminInput = ""
		d := strings.TrimSpace(text)
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Webhook.Domain = d
			if d != "" {
				a.botCfg.Webhook.PublicBaseURL = "https://" + d
			}
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showWebhooksAdmin(ctx, chatID)
	case "ref_value":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if n < 0 {
			n = 0
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.NormalizeReferral()
			a.botCfg.Referral.BonusValue = n
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showReferralAdmin(ctx, chatID)
	case "ref_invitee_value":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if n < 0 {
			n = 0
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.NormalizeReferral()
			a.botCfg.Referral.InviteeValue = n
			if n > 0 && a.botCfg.Referral.InviteeKind == "" {
				a.botCfg.Referral.InviteeKind = model.ReferralBonusBalance
			}
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showReferralAdmin(ctx, chatID)
	case "ref_percent":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if n < 0 {
			n = 0
		}
		if n > 100 {
			n = 100
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.NormalizeReferral()
			a.botCfg.Referral.Percent = n
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showReferralAdmin(ctx, chatID)
	case "cab_path":
		ui.adminInput = ""
		a.setCabinetPath(ctx, chatID, text)
	case "cab_title":
		ui.adminInput = ""
		a.setCabinetField(ctx, chatID, "title", text)
	case "cab_desc":
		ui.adminInput = ""
		a.setCabinetField(ctx, chatID, "desc", text)
	case "cab_favicon":
		ui.adminInput = ""
		a.setCabinetField(ctx, chatID, "favicon", text)
	case "device_per":
		mo := ui.priceMonths
		code := ui.planCode
		ui.adminInput = ""
		ui.priceMonths = 0
		ui.planCode = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if err := a.setPlanDevices(ctx, code, mo, n); err != nil {
			a.planInputFailed(ctx, chatID, err)
			return
		}
		a.afterPlanPriceEdit(ctx, chatID, code, mo)
	case "ntf_trial_days":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if n < 0 {
			n = 0
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Reminders.TrialDaysBefore = n
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showNotifyAdmin(ctx, chatID)
	case "trial_days":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrialDays(n)
		_ = a.saveBotConfig(ctx)
		a.showTrialAdmin(ctx, chatID)
	case "trial_gb":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrialGB(n)
		_ = a.saveBotConfig(ctx)
		a.showTrialAdmin(ctx, chatID)
	case "addsub_gb":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if n < 0 {
			n = 0
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.AddSub.TrafficGB = n
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showAddSubAdmin(ctx, chatID)
	case "addsub_name", "addsub_desc":
		field := ui.adminInput
		ui.adminInput = ""
		v := strings.TrimSpace(text)
		if field == "addsub_name" {
			v = firstLine(v)
		}
		if limit := planFieldLimit("plan_" + field); len([]rune(v)) > limit {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.too_long", limit),
				[][]models.InlineKeyboardButton{navBack(lang, "menu:addsub")})
			return
		}
		a.mu.Lock()
		if a.botCfg != nil {
			// Прочерк стирает (возврат к стандартному тексту), пустой ввод не
			// трогает — как в полях тарифа.
			switch {
			case v == "-" && field == "addsub_name":
				a.botCfg.AddSub.Name = ""
			case v == "-":
				a.botCfg.AddSub.Description = ""
			case v == "":
			case field == "addsub_name":
				a.botCfg.AddSub.Name = v
			default:
				a.botCfg.AddSub.Description = v
			}
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showAddSubAdmin(ctx, chatID)
	case "trial_hwid":
		ui.adminInput = ""
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrialHWID(n)
		_ = a.saveBotConfig(ctx)
		a.showTrialAdmin(ctx, chatID)
	case "trial_q_days":
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrialDays(n)
		ui.adminInput = "trial_q_gb"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "trial.q_gb"), "menu:trial")
	case "trial_q_gb":
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrialGB(n)
		ui.adminInput = "trial_q_hwid"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "trial.q_hwid"), "menu:trial")
	case "plan_q_price":
		mo := ui.priceMonths
		if err := a.setPlanPrice(ctx, "", mo, "base", text); err != nil {
			ui.adminInput = ""
			ui.priceMonths = 0
			a.planInputFailed(ctx, chatID, err)
			return
		}
		ui.adminInput = "plan_q_traffic"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "pricing.q_traffic", mo), "menu:pricing")
	case "plan_q_traffic":
		mo := ui.priceMonths
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		if err := a.setPlanTraffic(ctx, "", mo, n); err != nil {
			ui.adminInput = ""
			ui.priceMonths = 0
			a.planInputFailed(ctx, chatID, err)
			return
		}
		ui.adminInput = "plan_q_hwid"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "pricing.q_hwid", mo), "menu:pricing")
	case "plan_q_hwid":
		mo := ui.priceMonths
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		ui.adminInput = ""
		ui.priceMonths = 0
		if err := a.setPlanDevices(ctx, "", mo, n); err != nil {
			a.planInputFailed(ctx, chatID, err)
			return
		}
		a.showPlanSquads(ctx, chatID, mo)
	case "torrent_strike":
		a.setTorrentStrikeLimit(ctx, chatID, text)
	case "trial_q_hwid":
		n, _ := strconv.Atoi(strings.TrimSpace(text))
		a.setTrialHWID(n)
		ui.adminInput = ""
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Trial.Enabled = true
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showTrialAdmin(ctx, chatID)
	}
}

func curSuffix(cur string) string {
	if cur == "" {
		return ""
	}
	return " " + cur
}

const curRUB = "₽"

func p2pExt(id int64) string { return "p2p:" + strconv.FormatInt(id, 10) }

func (a *App) curFor(string) string { return curRUB }

func splitTrim(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (a *App) formatFiatPrices(method string) string {
	pr := a.pricing()
	var parts []string
	for _, mo := range model.PlanMonths {
		if v := pr.Fiat(method, mo); v != "" {
			parts = append(parts, strconv.Itoa(mo)+"м="+v)
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

func (a *App) cleanupP2PUser(ctx context.Context, userChatID int64) {
	a.mu.Lock()
	ui, ok := a.ui[userChatID]
	a.mu.Unlock()
	if !ok || ui == nil {
		return
	}
	if ui.p2pShotMsgID != 0 {
		a.msg.Delete(ctx, userChatID, ui.p2pShotMsgID)
		ui.p2pShotMsgID = 0
	}
	if ui.p2pSubmitMsgID != 0 {
		a.msg.Delete(ctx, userChatID, ui.p2pSubmitMsgID)
		ui.p2pSubmitMsgID = 0
	}
}

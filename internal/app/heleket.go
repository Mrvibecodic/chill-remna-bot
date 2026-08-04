package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/heleket"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/storage"
)

const (
	// hlExtPrefix — префикс ExtID платежей Heleket в хранилище.
	hlExtPrefix = "hl:"
	// purposeTopUp — значение PendingInvoice.Purpose для пополнения баланса.
	purposeTopUp = "topup"
)

func (a *App) hlConfig() model.HeleketConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.HeleketConfig{}
	}
	return a.botCfg.Heleket
}

func (a *App) hlClient() *heleket.Client {
	cfg := a.hlConfig()
	if !cfg.Enabled || cfg.MerchantID == "" || cfg.APIKey == "" {
		return nil
	}
	c, err := heleket.New(cfg.MerchantID, cfg.APIKey)
	if err != nil {
		a.log.Error("heleket: bad credentials", "err", err)
		return nil
	}
	return c
}

// hlCurrency — валюта счёта. Берётся из прайса; Heleket принимает фиатные коды
// (в том числе RUB) и сам пересчитывает сумму в выбранную клиентом крипту.
// В прайсе валюта хранится СИМВОЛОМ («₽», «руб»), а не кодом — админку так и
// спрашивают. Поэтому берём значение только если это настоящий трёхбуквенный
// код (currencyCode отсекает в том числе «₽», в котором тоже три байта).
func (a *App) hlCurrency() string {
	cur := strings.TrimSpace(a.pricing().Currency)
	if !currencyCode(cur) {
		return "RUB"
	}
	return strings.ToUpper(cur)
}

// hlCallbackURL — адрес вебхука. Пусто, если публичный адрес не настроен: тогда
// счёт всё равно создастся, но оплату добьёт ручная проверка или реконсилятор.
func (a *App) hlCallbackURL() string {
	a.mu.Lock()
	base, domain, tls := "", "", false
	if a.botCfg != nil {
		base = a.botCfg.Webhook.PublicBaseURL
		domain = a.botCfg.Webhook.Domain
		tls = a.botCfg.Webhook.TLS
	}
	a.mu.Unlock()
	if tls && domain != "" {
		base = "https://" + domain
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	return base + "/webhook/heleket"
}

func hlNonce() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Фолбэк не должен быть константой: одинаковый order_id вернул бы
		// СТАРЫЙ (возможно, уже оплаченный) счёт вместо нового.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// hlOrderID собирает order_id: только буквы, цифры, «-» и «_» (минус в
// отрицательном telegram-id e-mail-аккаунтов кабинета допустим).
//
// Nonce обязателен: на повторный order_id Heleket возвращает СТАРЫЙ счёт, и
// вторая покупка того же тарифа молча уехала бы в уже оплаченный инвойс.
func hlOrderID(chatID int64, n int64, purpose string) string {
	kind := "hl"
	if purpose == purposeTopUp {
		kind = "hlt"
	}
	return fmt.Sprintf("%s-%d-%d-%s", kind, chatID, n, hlNonce())
}

// hlData — полезная нагрузка в штатном поле additional_data (≤255 символов),
// которое Heleket возвращает и в вебхуке, и в информации о счёте.
func hlData(chatID int64, months int, kopecks int64) string {
	if kopecks > 0 {
		return fmt.Sprintf("tg=%d&kp=%d", chatID, kopecks)
	}
	return fmt.Sprintf("tg=%d&mo=%d", chatID, months)
}

func parseHLData(raw string) (chatID int64, months int, kopecks int64) {
	for _, part := range strings.Split(raw, "&") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch k {
		case "tg":
			chatID, _ = strconv.ParseInt(v, 10, 64)
		case "mo":
			months, _ = strconv.Atoi(v)
		case "kp":
			kopecks, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return
}

// parseHLOrderID — резервный путь восстановления получателя, если
// additional_data пусто, а pending-записи уже нет. topup=true означает, что
// n — это копейки пополнения, а не месяцы подписки.
func parseHLOrderID(orderID string) (chatID int64, n int64, topup bool) {
	parts := strings.Split(orderID, "-")
	if len(parts) == 0 || (parts[0] != "hl" && parts[0] != "hlt") {
		return
	}
	topup = parts[0] == "hlt"
	rest := parts[1:]
	if len(rest) >= 3 && rest[0] == "" {
		// отрицательный telegram-id: «hl--100-1-abcd»
		rest = append([]string{"-" + rest[1]}, rest[2:]...)
	}
	if len(rest) >= 2 {
		chatID, _ = strconv.ParseInt(rest[0], 10, 64)
		n, _ = strconv.ParseInt(rest[1], 10, 64)
	}
	return
}

// hlCreateInvoice — общее ядро выставления счёта для чата, мини-аппа и
// веб-кабинета: создаёт счёт, пишет журнал и pending-запись, возвращает ссылку
// на оплату и uuid счёта.
func (a *App) hlCreateInvoice(ctx context.Context, chatID int64, months int, amount string, purpose string, kopecks int64) (payURL, uuid string, err error) {
	client := a.hlClient()
	if client == nil {
		return "", "", errors.New(i18n.T(a.lang(chatID), "hl.not_configured"))
	}
	cfg := a.hlConfig()
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	// Строгий разбор: parseAmountRub на «1 000» вернул бы 1.0 и счёт ушёл бы
	// на рубль при выдаче полной подписки. rubToKopecks такие цены отвергает.
	valueK, okV := rubToKopecks(amount)
	if !okV || valueK <= 0 {
		a.payLog(ctx, model.PayMethodHeleket, "", chatID, "invoice_error", "%s: цена %q не разобрана — счёт не выставлен", purposeOrBuy(purpose), amount)
		return "", "", errors.New(i18n.T(a.lang(chatID), "hl.no_price"))
	}
	n := int64(months)
	if purpose == purposeTopUp {
		n = kopecks
	}
	// Топ-ап всегда в рублях: баланс бота ведётся в копейках ₽, и ЮKassa-топап
	// жёстко шлёт RUB. Валюта прайса применима только к покупке подписки.
	currency := a.hlCurrency()
	if purpose == purposeTopUp {
		currency = "RUB"
	}
	inv, err := client.CreateInvoice(ctx, heleket.InvoiceRequest{
		Amount:         fmt.Sprintf("%d.%02d", valueK/100, valueK%100),
		Currency:       currency,
		OrderID:        hlOrderID(chatID, n, purpose),
		ToCurrency:     strings.ToUpper(strings.TrimSpace(cfg.ToCurrency)),
		Subtract:       cfg.SubtractOrDefault(),
		Lifetime:       cfg.LifetimeOrDefault(),
		CallbackURL:    a.hlCallbackURL(),
		ReturnURL:      cfg.ReturnURL,
		SuccessURL:     cfg.ReturnURL,
		AdditionalData: hlData(chatID, months, kopecks),
	})
	if err != nil {
		a.payLog(ctx, model.PayMethodHeleket, "", chatID, "invoice_error", "%s months=%d kopecks=%d: %v", purposeOrBuy(purpose), months, kopecks, err)
		return "", "", err
	}
	extID := hlExtPrefix + inv.UUID
	a.payLog(ctx, model.PayMethodHeleket, extID, chatID, "invoice_created",
		"%s months=%d kopecks=%d amount=%s %s subtract=%d", purposeOrBuy(purpose), months, kopecks, inv.Amount, inv.Currency, cfg.SubtractOrDefault())
	if a.store != nil {
		_ = a.store.AddPendingInvoice(ctx, &model.PendingInvoice{
			Method: model.PayMethodHeleket, ExtID: extID, TelegramID: chatID,
			Months: months, Purpose: purpose, Kopecks: kopecks,
		})
	}
	return inv.URL, inv.UUID, nil
}

func purposeOrBuy(p string) string {
	if p == purposeTopUp {
		return "topup"
	}
	return "purchase"
}

func (a *App) startHeleket(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	months := a.getUI(chatID).buyMonths
	if months == 0 {
		months = model.PlanMonths[0]
	}
	price := a.pricing().Base[months]
	if !a.hlConfig().Enabled || price == "" {
		a.sendHome(ctx, chatID, i18n.T(lang, "hl.no_price"))
		return
	}
	payURL, uuid, err := a.hlCreateInvoice(ctx, chatID, months, price, "", 0)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "hl.fail", err.Error()))
		return
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "hl.pay_prompt", months, price+curSuffix(curRUB)), [][]models.InlineKeyboardButton{
		{{Text: i18n.T(lang, "hl.btn_pay"), URL: payURL}},
		{btn(i18n.T(lang, "hl.btn_check"), "hlc:"+uuid)},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// onHLCheck — кнопка «Проверить оплату».
func (a *App) onHLCheck(ctx context.Context, chatID int64, uuid string) {
	lang := a.lang(chatID)
	client := a.hlClient()
	if client == nil || uuid == "" {
		return
	}
	extID := hlExtPrefix + uuid
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, extID); done {
			a.showMySubs(ctx, chatID)
			return
		}
	}
	inv, err := client.Info(ctx, uuid)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "hl.fail", err.Error()))
		return
	}
	a.payLog(ctx, model.PayMethodHeleket, extID, chatID, "manual_check", "status=%s", inv.Status)
	if !heleket.Successful(inv.Status) {
		a.sendKB(ctx, chatID, a.hlPendingText(ctx, lang, inv), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "hl.btn_check"), "hlc:"+uuid)},
			{btn(i18n.T(lang, "btn.home"), "menu:home")},
		})
		return
	}
	a.finalizeHeleket(ctx, inv)
}

// hlPendingText объясняет пользователю, чего именно ждём.
func (a *App) hlPendingText(ctx context.Context, lang string, inv *heleket.Invoice) string { //nolint:unparam // lang симметричен остальным экранам
	switch inv.Status {
	case heleket.StatusWrongAmount:
		// Финальная недоплата: текст обещает уведомление админа — выполняем
		// обещание и здесь, а не только в вебхуке (дедуп не даст задвоить).
		a.hlNotifyAdmin(ctx, inv, "underpaid")
		return i18n.T(lang, "hl.underpaid")
	case heleket.StatusWrongAmountWaiting:
		// Доплата ещё возможна — админа не дёргаем, текст без ложных обещаний.
		return i18n.T(lang, "hl.underpaid_wait")
	case heleket.StatusLocked:
		a.hlNotifyAdmin(ctx, inv, "locked")
		return i18n.T(lang, "hl.locked")
	}
	if heleket.Final(inv.Status) {
		return i18n.T(lang, "hl.failed")
	}
	return i18n.T(lang, "hl.pending")
}

// hlNotifyAdmin зовёт админа туда, где деньги пришли, но подписку выдавать
// нельзя: недоплата и AML-заморозка разбираются вручную. Уведомление уходит
// ОДИН раз на счёт и вид события: вебхук, ручная проверка и реконсилятор
// пересекаются, а кнопкой «Проверить оплату» админа иначе можно заспамить.
func (a *App) hlNotifyAdmin(ctx context.Context, inv *heleket.Invoice, kind string) {
	key := inv.UUID + ":" + kind
	a.thrMu.Lock()
	if a.hlNotified == nil {
		a.hlNotified = map[string]bool{}
	}
	dup := a.hlNotified[key]
	a.hlNotified[key] = true
	a.thrMu.Unlock()
	if dup {
		return
	}
	alang := a.lang(a.cfg.AdminID)
	a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "hl.admin_"+kind, inv.UUID, inv.Amount+" "+inv.Currency))
}

// finalizeHeleket выдаёт подписку либо зачисляет баланс по подтверждённому
// счёту. Вызывается из вебхука, ручной проверки и реконсилятора.
func (a *App) finalizeHeleket(ctx context.Context, inv *heleket.Invoice) {
	if a.store == nil || inv == nil {
		return
	}
	extID := hlExtPrefix + inv.UUID
	amount := a.hlAmountLabel(inv)

	if p, _ := a.store.PendingByExtID(ctx, extID); p != nil && p.Purpose == purposeTopUp {
		if err := a.finalizeTopUp(ctx, p.TelegramID, p.Kopecks, model.PayMethodHeleket, amount, extID); err != nil {
			a.log.Error("heleket topup finalize", "err", err, "uuid", inv.UUID)
			return
		}
		_ = a.store.ResolvePending(ctx, p.ID)
		return
	}

	chatID, months, kopecks := parseHLData(inv.AdditionalData)
	if chatID == 0 || (months == 0 && kopecks == 0) {
		if p, _ := a.store.PendingByExtID(ctx, extID); p != nil {
			chatID, months, kopecks = p.TelegramID, p.Months, p.Kopecks
			if p.Purpose == purposeTopUp {
				if err := a.finalizeTopUp(ctx, chatID, kopecks, model.PayMethodHeleket, amount, extID); err != nil {
					a.log.Error("heleket topup finalize", "err", err, "uuid", inv.UUID)
					return
				}
				_ = a.store.ResolvePending(ctx, p.ID)
				return
			}
		}
	}
	if chatID == 0 {
		if id, n, topup := parseHLOrderID(inv.OrderID); id != 0 {
			chatID = id
			if topup {
				// Для «hlt-» n — копейки. Без этой ветки пополнение с потерянной
				// pending-записью и пустым additional_data превратилось бы в
				// подписку на срок по умолчанию.
				if kopecks == 0 {
					kopecks = n
				}
			} else if months == 0 {
				months = int(n)
			}
		}
	}
	if chatID == 0 {
		a.payLog(ctx, model.PayMethodHeleket, extID, 0, "error", "оплата подтверждена, но получатель неизвестен: нет additional_data, pending-счёта и разбираемого order_id")
		return
	}
	// Пополнение, у которого не осталось pending-записи, опознаётся по kp в
	// additional_data. Без этой ветки деньги за пополнение превратились бы в
	// подписку на срок по умолчанию.
	if kopecks > 0 && months == 0 {
		if err := a.finalizeTopUp(ctx, chatID, kopecks, model.PayMethodHeleket, amount, extID); err != nil {
			a.log.Error("heleket topup finalize", "err", err, "uuid", inv.UUID)
		}
		return
	}
	if months == 0 {
		months = model.PlanMonths[0]
	}

	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodHeleket, amount, extID)
	if err != nil {
		if errors.Is(err, storage.ErrDuplicateExtID) {
			return
		}
		a.log.Error("heleket finalize", "err", err, "uuid", inv.UUID)
		return
	}
	a.sendSubActive(ctx, chatID, link, expireAt)
}

// hlAmountLabel — что показать в журнале и уведомлениях: сколько заплатил
// клиент в крипте, а если шлюз этого не вернул — сумму счёта.
func (a *App) hlAmountLabel(inv *heleket.Invoice) string {
	if inv.PayerAmount != "" && inv.PayerCurrency != "" {
		return inv.PayerAmount + " " + inv.PayerCurrency
	}
	if inv.Amount != "" {
		return inv.Amount + " " + inv.Currency
	}
	return ""
}

func (a *App) showHeleketAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.hlConfig()
	status := i18n.T(lang, "admin.off")
	if cfg.Enabled {
		status = i18n.T(lang, "admin.on")
	}
	merchant := cfg.MerchantID
	if merchant == "" {
		merchant = i18n.T(lang, "admin.none")
	}
	key := i18n.T(lang, "admin.no")
	if cfg.APIKey != "" {
		key = i18n.T(lang, "admin.yes")
	}
	toCur := cfg.ToCurrency
	if toCur == "" {
		toCur = i18n.T(lang, "hl.tocur_auto")
	}
	ret := cfg.ReturnURL
	if ret == "" {
		ret = i18n.T(lang, "admin.none")
	}
	text := i18n.T(lang, "hl.title", status, merchant, key, a.hlCurrency(), toCur,
		cfg.SubtractOrDefault(), cfg.LifetimeOrDefault()/60, ret)
	a.sendPayKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{toggleBtn(lang, cfg.Enabled, "hl:toggle"), btn(i18n.T(lang, "hl.btn_probe"), "hl:probe")},
		{btn(i18n.T(lang, "hl.btn_merchant"), "hl:merchant"), btn(i18n.T(lang, "hl.btn_key"), "hl:key")},
		{btn(i18n.T(lang, "hl.btn_tocur"), "hl:tocur"), btn(i18n.T(lang, "hl.btn_subtract"), "hl:subtract")},
		{btn(i18n.T(lang, "hl.btn_lifetime"), "hl:lifetime"), btn(i18n.T(lang, "hl.btn_return"), "hl:return")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onHeleketAdmin(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	action, _, _ := strings.Cut(val, ":")
	switch action {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Heleket.Enabled = !a.botCfg.Heleket.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showHeleketAdmin(ctx, chatID)
	case "probe":
		a.hlProbe(ctx, chatID)
	case "merchant":
		a.getUI(chatID).adminInput = "hl_merchant"
		a.askInput(ctx, chatID, i18n.T(lang, "hl.ask_merchant"), "menu:heleket")
	case "key":
		a.getUI(chatID).adminInput = "hl_key"
		a.askInput(ctx, chatID, i18n.T(lang, "hl.ask_key"), "menu:heleket")
	case "tocur":
		a.getUI(chatID).adminInput = "hl_tocur"
		a.askInput(ctx, chatID, i18n.T(lang, "hl.ask_tocur"), "menu:heleket")
	case "subtract":
		a.getUI(chatID).adminInput = "hl_subtract"
		a.askInput(ctx, chatID, i18n.T(lang, "hl.ask_subtract"), "menu:heleket")
	case "lifetime":
		a.getUI(chatID).adminInput = "hl_lifetime"
		a.askInput(ctx, chatID, i18n.T(lang, "hl.ask_lifetime"), "menu:heleket")
	case "return":
		a.getUI(chatID).adminInput = "hl_return"
		a.askInput(ctx, chatID, i18n.T(lang, "hl.ask_return"), "menu:heleket")
	}
}

// hlCryptoCode проверяет по списку услуг шлюза, что код — существующая
// криптовалюта: официальная документация требует в to_currency только крипту,
// и фиатный код валил бы КАЖДОЕ создание счёта. Если список недоступен
// (ключи ещё не введены, сеть), даём сохранить — проверится кнопкой «Проверить
// ключи» и первым же счётом.
func (a *App) hlCryptoCode(ctx context.Context, code string) bool {
	client := a.hlClient()
	if client == nil {
		return true
	}
	list, err := client.Services(ctx)
	if err != nil || len(list) == 0 {
		return true
	}
	for _, s := range list {
		if strings.EqualFold(s.Currency, code) {
			return true
		}
	}
	return false
}

// hlProbe дёргает список услуг — так проверяются ключи, не выставляя счёт.
// Неверный merchant, чужой или выплатной ключ отваливаются сразу.
func (a *App) hlProbe(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	client := a.hlClient()
	if client == nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "hl.not_configured"),
			[][]models.InlineKeyboardButton{navBack(lang, "menu:heleket")})
		return
	}
	list, err := client.Services(ctx)
	if err != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "hl.probe_fail", err.Error()),
			[][]models.InlineKeyboardButton{navBack(lang, "menu:heleket")})
		return
	}
	avail := 0
	seen := map[string]bool{}
	var names []string
	for _, s := range list {
		if !s.IsAvailable {
			continue
		}
		avail++
		if !seen[s.Currency] {
			seen[s.Currency] = true
			names = append(names, s.Currency)
		}
	}
	if len(names) > 12 {
		names = append(names[:12], "…")
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "hl.probe_ok", avail, strings.Join(names, ", ")),
		[][]models.InlineKeyboardButton{navBack(lang, "menu:heleket")})
}

func (a *App) setHeleketField(ctx context.Context, chatID int64, field, text string) {
	text = strings.TrimSpace(text)
	a.mu.Lock()
	if a.botCfg != nil {
		switch field {
		case "hl_merchant":
			a.botCfg.Heleket.MerchantID = text
		case "hl_key":
			a.botCfg.Heleket.APIKey = text
		case "hl_tocur":
			cur := strings.ToUpper(text)
			if cur != "" && !a.hlCryptoCode(ctx, cur) {
				a.mu.Unlock()
				a.sendPayKB(ctx, chatID, i18n.T(a.lang(chatID), "hl.tocur_bad", cur),
					[][]models.InlineKeyboardButton{navBack(a.lang(chatID), "menu:heleket")})
				return
			}
			a.botCfg.Heleket.ToCurrency = cur
		case "hl_subtract":
			if n, err := strconv.Atoi(text); err == nil && n >= 0 && n <= 100 {
				a.botCfg.Heleket.Subtract = &n
			}
		case "hl_lifetime":
			// Вводится в минутах — так понятнее, чем секунды.
			if n, err := strconv.Atoi(text); err == nil {
				sec := n * 60
				if sec >= model.HeleketMinLifetime && sec <= model.HeleketMaxLifetime {
					a.botCfg.Heleket.Lifetime = sec
				}
			}
		case "hl_return":
			a.botCfg.Heleket.ReturnURL = text
		}
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showHeleketAdmin(ctx, chatID)
}

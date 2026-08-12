package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/web"
)

func (a *App) tributeCfg() model.TributeConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.TributeConfig{}
	}
	return a.botCfg.Tribute
}

func (a *App) startTribute(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.tributeCfg()
	if !cfg.Enabled || cfg.PayURL == "" {
		a.sendHome(ctx, chatID, i18n.T(lang, "trb.not_configured"))
		return
	}
	// Tribute продаёт только «Базовый», и счёт живёт на стороне Tribute —
	// вторая точка гейта здесь, при выдаче ссылки.
	if !a.baseSaleAllowed(ctx, chatID) {
		a.showPlans(ctx, chatID)
		return
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "trb.pay_prompt"), [][]models.InlineKeyboardButton{
		{{Text: i18n.T(lang, "trb.btn_pay"), URL: cfg.PayURL}},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func tributePeriodToMonths(period string) int {
	p := strings.ToLower(strings.TrimSpace(period))
	// Сначала точные значения официального enum Tribute (trial, onetime,
	// weekly, monthly, quarterly, halfyearly, yearly): подстрочный разбор
	// отдавал halfyearly ветке "year" и выдавал 12 месяцев вместо 6.
	switch p {
	case "yearly", "annual":
		return 12
	case "halfyearly":
		return 6
	case "quarterly":
		return 3
	case "monthly", "weekly", "trial", "onetime":
		return 1
	}
	// Фолбэк для нестандартных строк — «half» строго до «year».
	switch {
	case strings.Contains(p, "half"):
		return 6
	case strings.Contains(p, "year") || strings.Contains(p, "annual") || strings.Contains(p, "12"):
		return 12
	case strings.Contains(p, "6"):
		return 6
	case strings.Contains(p, "3") || strings.Contains(p, "quart"):
		return 3
	default:
		return 1
	}
}

// tributeWebhook — часть полезной нагрузки вебхука Tribute, которая нам нужна.
// Получателя ищем по telegram_user_id; устаревшее user_id (и пришедший ему на
// смену trb_user_id вида «T-31326»/«W-31326») не читаем — привязка у нас идёт
// по Telegram ID.
type tributeWebhook struct {
	Name    string `json:"name"`
	Payload struct {
		SubscriptionID int    `json:"subscription_id"`
		Period         string `json:"period"`
		// Price — сколько заплатил клиент, Amount — сколько осталось после
		// комиссии Tribute. Обе суммы в минимальных единицах валюты.
		Price    int64  `json:"price"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
		// Type — regular/gift/trial. У gift и trial оплата нулевая, а их
		// продление Tribute присылает уже как regular.
		Type string `json:"type"`
		// TrbUserID — ID пользователя в Tribute («T-31326» — вход через
		// Telegram, «W-31326» — через почту). Пишем его в журнал, чтобы платёж
		// нашёлся в кабинете Tribute; для выдачи подписки он не нужен.
		TrbUserID        string    `json:"trb_user_id"`
		TelegramUserID   int64     `json:"telegram_user_id"`
		TelegramUsername string    `json:"telegram_username"`
		ExpiresAt        time.Time `json:"expires_at"`
	} `json:"payload"`
}

// who — приписка к журналу, по которой платёж сопоставляется с кабинетом
// Tribute, даже если Telegram ID не пришёл.
func (wh tributeWebhook) who() string {
	parts := make([]string, 0, 3)
	if wh.Payload.TrbUserID != "" {
		parts = append(parts, "trb="+wh.Payload.TrbUserID)
	}
	if wh.Payload.TelegramUsername != "" {
		parts = append(parts, "@"+wh.Payload.TelegramUsername)
	}
	if wh.Payload.Type != "" {
		parts = append(parts, "тип="+wh.Payload.Type)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// tributeAmount печатает сумму так же, как остальные способы оплаты. Tribute
// присылает её в минимальных единицах валюты (700 eur — это 7.00 EUR), а
// строка платежа потом разбирается обратно в числа: из неё считаются процент
// рефереру, выручка в статистике и чек «Мой налог».
func tributeAmount(minor int64, cur string) string {
	return fmt.Sprintf("%.2f %s", float64(minor)/100, curSymbol(cur))
}

func (a *App) HandleTributeWebhook(ctx context.Context, signatureHex string, body []byte) (bool, error) {
	cfg := a.tributeCfg()
	if cfg.APIKey == "" {
		// Без ключа подпись не проверить. Отвечаем 401, а не 200: Tribute будет
		// ретраить доставку до суток, и настоящая оплата не потеряется, если
		// админ успеет вписать ключ.
		a.payLogThrottled(ctx, "trb-webhook-off", model.PayMethodTribute, "", 0, "error", "вебхук отброшен: не задан API-ключ Tribute — оплату нельзя верифицировать")
		a.log.Warn("tribute webhook: ignored — api key not set")
		return false, fmt.Errorf("tribute webhook: %w", web.ErrUnauthorized)
	}
	mac := hmac.New(sha256.New, []byte(cfg.APIKey))
	mac.Write(body)
	got, err := hex.DecodeString(strings.TrimSpace(signatureHex))
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		a.payLogThrottled(ctx, "trb-webhook-sign", model.PayMethodTribute, "", 0, "sign_error", "подпись вебхука не сошлась (проверьте API-ключ Tribute)")
		a.log.Warn("tribute webhook: bad signature")
		// web отдаёт на это 401: с 500 Tribute сутки долбил бы ретраями чужой
		// мусор и запросы с неверным ключом.
		return false, fmt.Errorf("tribute webhook: %w", web.ErrUnauthorized)
	}
	var wh tributeWebhook
	if err := json.Unmarshal(body, &wh); err != nil {
		a.payLogThrottled(ctx, "trb-webhook-json", model.PayMethodTribute, "", 0, "error", "тело вебхука не разобрано: %v", err)
		return false, fmt.Errorf("tribute webhook: bad json: %w", err)
	}
	chatID := wh.Payload.TelegramUserID
	switch wh.Name {
	case "new_subscription", "renewed_subscription":
	case "":
		// Кнопка «Тест» в кабинете Tribute шлёт событие без имени. Отмечаем его
		// в журнале: это единственное подтверждение, что адрес и ключ сошлись.
		a.payLog(ctx, model.PayMethodTribute, "", 0, "test", "тестовый вебхук принят, подпись верна")
		return true, nil
	case "cancelled_subscription":
		// Доступ не трогаем: в Tribute отмена выключает автопродление, а
		// оплаченный период дорабатывает до expires_at.
		a.payLog(ctx, model.PayMethodTribute, "", chatID, "cancelled",
			"подписка отменена в Tribute: продления не будет, оплаченный период до %s%s",
			wh.Payload.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"), wh.who())
		return true, nil
	default:
		a.log.Info("tribute webhook: ignored", "event", wh.Name)
		return true, nil
	}
	if chatID == 0 {
		a.payLog(ctx, model.PayMethodTribute, "", 0, "error", "в вебхуке нет telegram_user_id — получатель неизвестен (событие %s)%s", wh.Name, wh.who())
		a.log.Warn("tribute webhook: no telegram_user_id")
		return true, nil
	}
	months := tributePeriodToMonths(wh.Payload.Period)
	// В ключе дедупликации обязателен telegram_user_id: subscription_id — это
	// идентификатор тарифа автора, общий для всех подписчиков, и двое купивших
	// один тариф в одну секунду иначе получили бы одинаковый ext_id (второму —
	// «duplicate» без подписки при принятых деньгах).
	extID := fmt.Sprintf("trb_%d_%d_%d", chatID, wh.Payload.SubscriptionID, wh.Payload.ExpiresAt.Unix())
	paid := wh.Payload.Price
	if paid == 0 {
		paid = wh.Payload.Amount
	}
	amount := tributeAmount(paid, wh.Payload.Currency)
	if !cfg.Enabled {
		// Тумблер выключен, но деньги уже приняты Tribute — оплату обрабатываем,
		// а факт отмечаем: терять её с ответом 200 нельзя.
		a.payLog(ctx, model.PayMethodTribute, extID, chatID, "warning", "Tribute выключен в админке, но оплата пришла — обрабатываем")
	}
	a.payLog(ctx, model.PayMethodTribute, extID, chatID, "webhook", "%s period=%s amount=%s%s", wh.Name, wh.Payload.Period, amount, wh.who())
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, extID); done {
			a.payLog(ctx, model.PayMethodTribute, extID, chatID, "duplicate", "уже финализирован, вебхук пропущен")
			return true, nil
		}
	}
	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodTribute, amount, extID, nil)
	if err != nil {
		a.payLog(ctx, model.PayMethodTribute, extID, chatID, "finalize_error", "%v", err)
		return false, fmt.Errorf("tribute finalize %s: %w", extID, err)
	}
	a.sendSubActive(ctx, chatID, link, expireAt)
	a.log.Info("tribute webhook: finalized", "chat_id", chatID, "months", months)
	return true, nil
}

func (a *App) showTributeAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.tributeCfg()
	status := i18n.T(lang, "admin.off")
	if cfg.Enabled {
		status = i18n.T(lang, "admin.on")
	}
	key := i18n.T(lang, "admin.no")
	if cfg.APIKey != "" {
		key = i18n.T(lang, "admin.yes")
	}
	url := cfg.PayURL
	if url == "" {
		url = i18n.T(lang, "admin.none")
	}
	text := i18n.T(lang, "trb.title", status, key, url)
	a.sendPayKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{toggleBtn(lang, cfg.Enabled, "trb:toggle")},
		{btn(i18n.T(lang, "trb.btn_key"), "trb:key"), btn(i18n.T(lang, "trb.btn_url"), "trb:url")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onTributeAdmin(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	switch val {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Tribute.Enabled = !a.botCfg.Tribute.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showTributeAdmin(ctx, chatID)
	case "key":
		a.getUI(chatID).adminInput = "trb_key"
		a.askInput(ctx, chatID, i18n.T(lang, "trb.ask_key"), "menu:tribute")
	case "url":
		a.getUI(chatID).adminInput = "trb_url"
		a.askInput(ctx, chatID, i18n.T(lang, "trb.ask_url"), "menu:tribute")
	}
}

func (a *App) setTributeField(ctx context.Context, chatID int64, field, text string) {
	text = strings.TrimSpace(text)
	a.mu.Lock()
	if a.botCfg != nil {
		switch field {
		case "trb_key":
			a.botCfg.Tribute.APIKey = text
		case "trb_url":
			a.botCfg.Tribute.PayURL = text
		}
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showTributeAdmin(ctx, chatID)
}

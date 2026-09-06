package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

type rwWebhookEvent struct {
	Scope string          `json:"scope"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
	// Meta держим сырым: типизированный разбор ронял бы весь конверт (а с ним
	// и все прочие события) на любом неожиданном типе поля.
	Meta json.RawMessage `json:"meta"`
}

// meta приходит рядом с data, а не внутри неё (см. RemnawaveWebhookUserEvents).
// Expiration — интервал в ЧАСАХ со знаком: отрицательный = столько часов до
// истечения, положительный = столько часов после. Задаётся на панели в
// EXPIRATION_NOTIFICATIONS (диапазон -744..744).
type rwWebhookMeta struct {
	Expiration             *int `json:"expiration"`
	NotConnectedAfterHours *int `json:"notConnectedAfterHours"`
}

// rwUserPayload is the subset of the panel's user object the bot needs. Panel
// 3.0.0 dropped uuid and made the id numeric, so both identifier fields are
// read as raw JSON: they are only informational here — everything the bot acts
// on comes from telegramId/username, which both generations still send.
type rwUserPayload struct {
	UUID       json.RawMessage `json:"uuid"`
	UserID     json.RawMessage `json:"id"`
	Username   string          `json:"username"`
	TelegramID int64           `json:"telegramId"`
	ExpireAt   string          `json:"expireAt"`
	ExpireTime string          `json:"expireTime"`
	Status     string          `json:"status"`
}

func verifyRemnawaveSignature(signatureHex, secret string, body []byte) error {
	if secret == "" {
		return nil
	}
	if signatureHex == "" {
		return errors.New("remnawave webhook: signature header missing")
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signatureHex, "sha256="))
	if err != nil {
		return fmt.Errorf("remnawave webhook: bad signature hex: %w", err)
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	if !hmac.Equal(got, m.Sum(nil)) {
		return errors.New("remnawave webhook: signature mismatch")
	}
	return nil
}

func (a *App) HandleRemnawaveWebhook(ctx context.Context, signature string, body []byte) (bool, error) {
	a.mu.Lock()
	secret := ""
	if a.botCfg != nil {
		secret = strings.TrimSpace(a.botCfg.Webhook.RemnawaveSecret)
	}
	a.mu.Unlock()
	if err := verifyRemnawaveSignature(signature, secret, body); err != nil {
		return false, err
	}

	var ev rwWebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return false, fmt.Errorf("remnawave webhook: bad json: %w", err)
	}
	if ev.Event == "" {
		return false, errors.New("remnawave webhook: missing event field")
	}

	var u rwUserPayload
	_ = json.Unmarshal(ev.Data, &u)

	// Панель штатно переотправляет событие, если не получила ответ вовремя, и
	// тело повтора совпадает байт в байт. Три доставки давали человеку три
	// одинаковых «подписка истекла».
	//
	// Только для user.*: у torrent_blocker.report своя защита, завязанная на
	// содержимое отчёта.
	// Место занимается СРАЗУ: панель шлёт повтор, не дождавшись ответа, то
	// есть обе доставки идут одновременно, каждая в своей горутине. Проверка
	// «видели?» и отметка по разные стороны отправки в Telegram дали бы
	// человеку два сообщения.
	//
	// Но если доставка не удалась, место освобождается: иначе дедуп выключал
	// бы ровно тот механизм, ради которого нужен, — повтор панели после
	// отказа Telegram.
	dedup := strings.HasPrefix(ev.Event, "user.")
	if dedup && !a.rwEventClaim(body) {
		a.log.Info("remnawave webhook: повтор события пропущен", "event", ev.Event, "tg_id", u.TelegramID)
		return true, nil
	}
	delivered := false
	defer func() {
		if dedup && !delivered {
			a.rwEventRelease(body)
		}
	}()

	switch {
	case ev.Event == "user.expiration":
		// Панель с 2.8.0 (контракт 2.8.20): одно событие вместо user.expires_in_*/user.expired_*_ago,
		// конкретный интервал лежит в meta.expiration.
		hours := 0
		var meta rwWebhookMeta
		if len(ev.Meta) > 0 {
			if err := json.Unmarshal(ev.Meta, &meta); err != nil {
				// Интервал не прочитан — событие всё равно обрабатываем, но
				// текст будет без числа: молчать хуже.
				a.log.Warn("remnawave webhook: meta не разобрана", "event", ev.Event, "err", err)
			} else if meta.Expiration != nil {
				hours = *meta.Expiration
			}
		}
		if hours > 0 {
			// Подписка истекла hours часов назад — напоминание продлить.
			delivered = a.pushExpired(ctx, u)
			return true, nil
		}
		delivered = a.pushExpiryWarning(ctx, u, ev.Event, -hours)
		return true, nil
	case strings.HasPrefix(ev.Event, "user.expires_in"):
		// Панели 2.7.x (контракты до 2.8.19).
		delivered = a.pushExpiryWarning(ctx, u, ev.Event, expiresInHours(ev.Event))
		return true, nil
	case ev.Event == "user.expired":
		delivered = a.pushExpired(ctx, u)
		return true, nil
	case ev.Event == "user.disabled":
		delivered = a.pushAccessChanged(ctx, u, false)
		return true, nil
	case ev.Event == "user.enabled":
		delivered = a.pushAccessChanged(ctx, u, true)
		return true, nil
	case ev.Event == "user.limited" || ev.Event == "user.bandwidth_usage_threshold_reached":
		delivered = a.pushTrafficLimited(ctx, u)
		return true, nil
	case ev.Event == "torrent_blocker.report":
		a.pushTorrentReport(ctx, ev.Data)
		return true, nil
	default:
		a.log.Info("remnawave webhook: event ignored", "scope", ev.Scope, "event", ev.Event, "tg_id", u.TelegramID)
		return true, nil
	}
}

func (a *App) pushExpiryWarning(ctx context.Context, u rwUserPayload, event string, hours int) bool {
	if u.TelegramID == 0 {
		return true
	}
	lang := a.lang(u.TelegramID)
	text := i18n.T(lang, "rw.warn_expiring")
	switch {
	case hours >= 48 && hours%24 == 0:
		text = i18n.T(lang, "rw.warn_expiring_days", hours/24)
	case hours > 0:
		text = i18n.T(lang, "rw.warn_expiring_hours", hours)
	}
	ok := a.notifyKB(ctx, u.TelegramID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
	}) != 0
	a.log.Info("remnawave webhook: warn sent", "event", event, "tg_id", u.TelegramID, "ok", ok)
	return ok
}

func expiresInHours(event string) int {
	s := strings.TrimSuffix(strings.TrimPrefix(event, "user.expires_in_"), "_hours")
	n, _ := strconv.Atoi(s)
	return n
}

func (a *App) pushExpired(ctx context.Context, u rwUserPayload) bool {
	if u.TelegramID == 0 {
		return true
	}
	a.invalidateSubCache(u.TelegramID)
	lang := a.lang(u.TelegramID)
	ok := a.notifyKB(ctx, u.TelegramID, i18n.T(lang, "rw.expired"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
	}) != 0
	a.log.Info("remnawave webhook: expired notified", "tg_id", u.TelegramID, "ok", ok)
	return ok
}

func (a *App) pushTrafficLimited(ctx context.Context, u rwUserPayload) bool {
	if u.TelegramID == 0 {
		return true
	}
	lang := a.lang(u.TelegramID)
	ok := a.notifyKB(ctx, u.TelegramID, i18n.T(lang, "rw.limited"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.buy"), "menu:buy")},
	}) != 0
	a.log.Info("remnawave webhook: limit notified", "tg_id", u.TelegramID, "ok", ok)
	return ok
}

// pushAccessChanged — доступ выключили или включили в панели. Раньше событие
// молча игнорировалось: человек терял связь и не понимал почему, а кэш
// «есть подписка» ещё полминуты говорил обратное.
func (a *App) pushAccessChanged(ctx context.Context, u rwUserPayload, enabled bool) bool {
	if u.TelegramID == 0 {
		return true
	}
	a.invalidateSubCache(u.TelegramID)
	lang := a.lang(u.TelegramID)
	ok := false
	if enabled {
		ok = a.notifyKB(ctx, u.TelegramID, i18n.T(lang, "rw.enabled"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.mysubs"), "menu:mysubs")},
		}) != 0
	} else {
		var rows [][]models.InlineKeyboardButton
		if sup := a.supportURL(); sup != "" {
			rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(lang, "btn.support"), URL: sup}})
		}
		ok = a.notifyKB(ctx, u.TelegramID, i18n.T(lang, "rw.disabled"), rows) != 0
	}
	a.log.Info("remnawave webhook: access change notified", "tg_id", u.TelegramID, "enabled", enabled, "ok", ok)
	return ok
}

// rwEventTTL — окно, в котором повторная доставка считается повтором. Панель
// исчерпывает попытки за минуты. Окно намеренно короткое: ключ — хэш тела, а
// гарантии, что панель кладёт в конверт уникальный timestamp, у нас нет, и
// длинное окно склеило бы два РАЗНЫХ одинаковых события (отключили — включили
// — снова отключили).
const rwEventTTL = 10 * time.Minute

// rwSeenMax — потолок карты дедупа. Поток разных событий не должен раздувать
// её без конца: окно всего десять минут, и сброс переполненной карты стоит
// дешевле, чем неограниченный рост под общим замком.
const rwSeenMax = 4096

// rwSeenSweep — не чаще этого перебираем карту целиком. Полный проход на
// каждом событии — плохой обмен: замок общий с торрент-блокером.
const rwSeenSweep = time.Minute

// rwEventClaim занимает место под событие. false — это тело уже в работе или
// недавно обработано.
func (a *App) rwEventClaim(body []byte) bool {
	key := rwEventKey(body)
	now := time.Now()
	a.thrMu.Lock()
	defer a.thrMu.Unlock()
	if a.rwSeen == nil {
		a.rwSeen = map[string]time.Time{}
	}
	if now.Sub(a.rwSweptAt) > rwSeenSweep {
		a.rwSweptAt = now
		for k, t := range a.rwSeen {
			if now.Sub(t) > rwEventTTL {
				delete(a.rwSeen, k)
			}
		}
		if len(a.rwSeen) > rwSeenMax {
			a.log.Warn("remnawave webhook: карта дедупа переполнена, сброшена", "size", len(a.rwSeen))
			a.rwSeen = map[string]time.Time{}
		}
	}
	if t, ok := a.rwSeen[key]; ok && now.Sub(t) <= rwEventTTL {
		return false
	}
	a.rwSeen[key] = now
	return true
}

// rwEventRelease освобождает место: доставить не удалось, повтор панели нужен.
func (a *App) rwEventRelease(body []byte) {
	key := rwEventKey(body)
	a.thrMu.Lock()
	delete(a.rwSeen, key)
	a.thrMu.Unlock()
}

func rwEventKey(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

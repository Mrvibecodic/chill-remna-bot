package app

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
)

const (
	// Первый проход не на старте: панель и конфиг к этому моменту уже подняты,
	// а рассылка сообщений в первые секунды жизни бота мешала бы установке.
	trialResetFirstDelay = 3 * time.Minute
	trialResetInterval   = time.Hour
	// Потолок на проход: каждый кандидат стоит запроса в панель, а кандидатом
	// человек остаётся, пока не выберет повторы. Без потолка бот с тысячами
	// брошенных триалов долбил бы панель тысячей запросов каждый час.
	// Отсечка живёт в SQL вместе со случайным порядком — см.
	// ListTrialResetTargets: срезать первые N уже полученных строк значит
	// навсегда закрыть очередь теми, кому возврат не положен.
	trialResetBatch = 200
	// Пауза между кандидатами — той же природы: проход фоновый и никуда не
	// торопится, а панель обслуживает живых людей.
	trialResetPace = 200 * time.Millisecond
)

// RunTrialReset возвращает пробный период тем, кто взял его и не тронул.
//
// Смысл: человек нажал «пробный период», не настроил клиент и забыл. Отметка
// «триал использован» закрывает ему вход навсегда, хотя услугой он не
// пользовался ни минуты. Проход снимает отметку и зовёт его обратно.
func (a *App) RunTrialReset(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(trialResetFirstDelay):
	}
	for {
		a.resetTrialsOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(trialResetInterval):
		}
	}
}

func (a *App) resetTrialsOnce(ctx context.Context) int {
	a.mu.Lock()
	st := a.store
	panel := a.panel
	var tr model.TrialConfig
	if a.botCfg != nil {
		tr = a.botCfg.Trial
	}
	a.mu.Unlock()
	// Выключенный триал возвращать нечего: человек нажмёт кнопку и получит
	// отказ. Нулевой потолок повторов — тоже выключено.
	if st == nil || panel == nil || !tr.Enabled || tr.Days <= 0 || !tr.ResetUnused || tr.ResetUnusedMax <= 0 {
		return 0
	}
	targets, err := st.ListTrialResetTargets(ctx, tr.ResetUnusedMax, trialResetBatch)
	if err != nil {
		a.log.Warn("возврат триала: список", "err", err)
		return 0
	}
	now := time.Now().UTC()
	done := 0
	for i, t := range targets {
		if ctx.Err() != nil {
			return done
		}
		if i > 0 && !a.runInline {
			select {
			case <-ctx.Done():
				return done
			case <-time.After(trialResetPace):
			}
		}
		if a.resetTrialOne(ctx, st, panel, tr, t, now) {
			done++
		}
	}
	if done > 0 {
		a.log.Info("возврат триала", "людей", done)
	}
	return done
}

func (a *App) resetTrialOne(ctx context.Context, st storage.Storage, panel *remnawave.Client,
	tr model.TrialConfig, t storage.TrialResetTarget, now time.Time) bool {
	exp, err := time.Parse(time.RFC3339, t.SubExpireAt)
	if err != nil || exp.After(now) {
		return false
	}
	// Тот же замок, что сериализует выдачу подписки и триала по человеку:
	// иначе возврат мог бы лечь ровно посреди оплаты. Отбор кандидатов шёл
	// без замка, поэтому состояние ниже перепроверяется по панели, а запись
	// сверяется с тем сроком, который проход видел.
	lk := &a.finalizeUserLk[extLockIndex(strconv.FormatInt(t.TelegramID, 10))]
	lk.Lock()
	defer func() {
		if lk != nil {
			lk.Unlock()
		}
	}()
	pu, perr := panel.FindByTelegramID(ctx, t.TelegramID)
	if perr != nil {
		return false
	}
	// Учётки в панели нет — сколько трафика человек потратил, узнать не у
	// кого. Возвращать триал вслепую нельзя: под это попал бы и тот, кому
	// аккаунт удалили за злоупотребление.
	if pu == nil {
		return false
	}
	// Срок в панели главнее зеркала бота: админ мог продлить руками, и тогда
	// подписка у человека ЖИВА — снимать отметку рано.
	if pe, xerr := time.Parse(time.RFC3339, pu.ExpireAt); xerr == nil && pe.After(now) {
		return false
	}
	// Отключённая в панели учётка — это наказание: так бот блокирует
	// подписку по решению админа и по торрент-блокеру. Возвращать такому
	// человеку триал значит позвать его письмом на нерабочую ссылку:
	// повторная выдача статус не чинит.
	if strings.EqualFold(pu.Status, remnawave.StatusDisabled) {
		return false
	}
	// Счётчик израсходованного в панели — ПЕРИОДНЫЙ: при стратегии MONTH/WEEK/
	// DAY панель обнуляет его на границе периода. После такой границы «ноль
	// потрачено» перестаёт означать «не пользовался»: человек, выкачавший весь
	// триал, выглядит точно как тот, кто ни разу не подключился. Поэтому
	// счётчику верим только внутри его собственного периода после окончания
	// подписки, а тех, кто провисел дольше, не трогаем вовсе.
	if window := trafficWindow(pu.Strategy); window > 0 && now.Sub(exp) > window {
		return false
	}
	if !trialUnused(pu.TrafficLimit, pu.TrafficUsed, tr.ResetUnusedPct) {
		return false
	}
	// Сначала запись со сверкой срока, и только потом панель. Обратный
	// порядок обнулял бы счётчик трафика ЖИВОЙ подписки в тех гонках, где
	// сверка не даст снять отметку: срок пишут и пути, которые этот замок не
	// берут (реферальные дни, привязка панели, импорт). Неудача обнуления
	// после успешной записи стоит человеку максимум процента с порога — это
	// на порядок дешевле.
	ok, rerr := st.ResetTrialForRepeat(ctx, t.TelegramID, t.SubExpireAt)
	if rerr != nil {
		a.log.Warn("возврат триала: снятие отметки", "tg_id", t.TelegramID, "err", rerr)
		return false
	}
	if !ok {
		return false
	}
	if err := panel.ResetTraffic(ctx, pu.Ref); err != nil {
		a.log.Warn("возврат триала: счётчик трафика не обнулён", "tg_id", t.TelegramID, "err", err)
	}
	a.invalidateSubCache(t.TelegramID)
	// Дальше идёт отправка в Telegram — снаружи замка: держать его на время
	// сетевого запроса значит подвесить человеку кнопку «взять триал» ровно
	// в тот момент, когда мы его этой кнопкой и зовём.
	lk.Unlock()
	lk = nil
	lang := a.lang(t.TelegramID)
	rows := [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.trial_user"), "menu:trial")}}
	a.notifyKB(ctx, t.TelegramID, i18n.T(lang, "trial.reset_notice", tr.Days), rows)
	return true
}

// trialUnused — потрачено не больше pct процентов лимита.
//
// Безлимитный триал (limit = 0) засчитывается только нетронутым: доли от
// «сколько угодно» не существует, и любой порог в процентах на нём означал бы
// «возвращать всегда».
func trialUnused(limit, used int64, pct int) bool {
	if used <= 0 {
		return true
	}
	if limit <= 0 || pct <= 0 {
		return false
	}
	if pct >= 100 {
		return true
	}
	if limit > math.MaxInt64/100 {
		return false
	}
	return used*100 <= limit*int64(pct)
}

// trafficWindow — как долго после окончания подписки счётчику израсходованного
// ещё можно верить. Ноль — верить можно всегда (панель его не обнуляет).
func trafficWindow(strategy string) time.Duration {
	switch strings.ToUpper(strings.TrimSpace(strategy)) {
	case "NO_RESET":
		return 0
	case "DAY":
		return 24 * time.Hour
	case "WEEK":
		return 7 * 24 * time.Hour
	}
	// MONTH, MONTH_ROLLING и пустое значение (панель применила своё
	// умолчание): берём самый длинный месяц.
	return 31 * 24 * time.Hour
}

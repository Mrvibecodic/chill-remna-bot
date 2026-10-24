package app

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"remnabot/internal/model"
	"remnabot/internal/web"
	"remnabot/internal/yoomoney"
)

// HandleYooMoneyWebhook обрабатывает HTTP-уведомление ЮMoney о переводе.
// Два рубежа: подпись sign (HMAC-SHA256 с секретом из настроек кошелька;
// чужому запросу — 401 без похода в API) и перепроверка операции по API
// кошелька по метке — решение о выдаче принимается по ответу API. Ошибка
// (не 200) заставляет ЮMoney повторить доставку через 10 минут и через час.
func (a *App) HandleYooMoneyWebhook(ctx context.Context, form url.Values) (bool, error) {
	cfg := a.ymConfig()
	if cfg.NotifySecret == "" {
		a.payLogThrottled(ctx, "ym-webhook-off", model.PayMethodYooMoney, "", 0, "error", "уведомление отброшено: секрет ЮMoney не задан")
		return false, fmt.Errorf("yoomoney webhook: %w", web.ErrUnauthorized)
	}
	if !yoomoney.VerifySign(form, cfg.NotifySecret) {
		a.payLogThrottled(ctx, "ym-webhook-sign", model.PayMethodYooMoney, "", 0, "sign_error", "подпись уведомления не сошлась — запрос не от ЮMoney или секрет другой")
		a.log.Warn("yoomoney webhook: bad sign")
		return false, fmt.Errorf("yoomoney webhook: %w", web.ErrUnauthorized)
	}
	n, err := yoomoney.ParseNotification(form)
	if err != nil {
		a.payLog(ctx, model.PayMethodYooMoney, n.Label, ymLabelHint(n.Label), "error", "уведомление с подписью, но суммы не разобраны: %v", err)
		// Подпись верна, повтор того же тела ничего не изменит — 200.
		return true, nil
	}
	hint := ymLabelHint(n.Label)
	if n.Test {
		a.payLog(ctx, model.PayMethodYooMoney, n.Label, hint, "webhook", "тестовое уведомление из настроек кошелька — подпись сошлась")
		return true, nil
	}
	if strings.HasPrefix(n.Label, ymExtPrefix) {
		a.payLog(ctx, model.PayMethodYooMoney, n.Label, hint, "webhook", "операция %s: заплачено %s, зачислено %s RUB, тип %s",
			n.OperationID, yoomoney.FormatSum(n.WithdrawK), yoomoney.FormatSum(n.AmountK), n.Type)
	}
	if !strings.HasPrefix(n.Label, ymExtPrefix) {
		// Перевод на кошелёк мимо бота (без нашей метки) — не наш платёж.
		// Личные переводы владельцу журнал не засоряют: запись не чаще раза
		// в десять минут.
		a.payLogThrottled(ctx, "ym-webhook-nolabel", model.PayMethodYooMoney, "", 0, "webhook", "перевод без метки бота (операция %s, %s RUB) — пропущен", n.OperationID, yoomoney.FormatSum(n.AmountK))
		return true, nil
	}
	if n.CodePro || n.Unaccepted {
		// По доке оба флага всегда false; если всё же пришёл удержанный или
		// защищённый кодом перевод — деньги ещё не на кошельке, не выдаём.
		a.payLog(ctx, model.PayMethodYooMoney, n.Label, hint, "held", "перевод удержан (codepro=%v unaccepted=%v) — выдачи нет", n.CodePro, n.Unaccepted)
		a.ymNotifyAdmin(ctx, n.Label+":held", "ym.admin_held", escapeName(n.OperationID), yoomoney.FormatSum(n.WithdrawK), a.userLabelByID(ctx, hint))
		return true, nil
	}
	if n.Currency != "" && n.Currency != yoomoney.CurrencyRUB {
		a.payLog(ctx, model.PayMethodYooMoney, n.Label, hint, "error", "валюта уведомления %s — ждём только 643", n.Currency)
		return true, nil
	}
	client := a.ymClient()
	if client == nil {
		a.payLog(ctx, model.PayMethodYooMoney, n.Label, hint, "error", "токен API не задан — оплату нельзя перепроверить")
		a.log.Error("yoomoney webhook: no token")
		return true, nil
	}
	ops, err := client.OperationsByLabel(ctx, n.Label)
	if err != nil {
		a.payLog(ctx, model.PayMethodYooMoney, n.Label, hint, "verify_error", "%v", err)
		a.ymTokenAlert(ctx, err)
		return false, fmt.Errorf("yoomoney webhook: verify %s: %w", n.Label, err)
	}
	paid := ymPaidOps(ops, n.Label)
	var found *yoomoney.Operation
	for i := range paid {
		if paid[i].ID == n.OperationID {
			found = &paid[i]
			break
		}
	}
	if found == nil {
		// Уведомление пришло раньше, чем операция появилась в истории, —
		// пусть ЮMoney повторит через 10 минут.
		a.payLog(ctx, model.PayMethodYooMoney, n.Label, hint, "verify_pending", "операция %s ещё не в истории кошелька (оплаченных по метке: %d)", n.OperationID, len(paid))
		return false, fmt.Errorf("yoomoney webhook: operation %s not in history yet: %w", n.OperationID, web.ErrRetryLater)
	}
	if found.AmountK() != n.AmountK {
		a.payLog(ctx, model.PayMethodYooMoney, n.Label, hint, "amount_mismatch", "в уведомлении зачислено %s RUB, в истории кошелька %s — берём историю", yoomoney.FormatSum(n.AmountK), yoomoney.FormatSum(found.AmountK()))
		n.AmountK = found.AmountK()
	}
	if a.store == nil {
		return true, nil
	}
	p, _ := a.store.PendingByExtID(ctx, n.Label)
	if done, _ := a.store.PaymentByExtID(ctx, n.Label); done {
		// Платёж уже записан (этим уведомлением раньше, кнопкой или сверкой).
		// Зачтённую операцию все пути выбирают одним правилом (ymPickOp);
		// всё остальное — лишние оплаты той же ссылки.
		chosen, extra := a.ymPickOp(paid, p)
		if chosen == nil || chosen.ID != n.OperationID {
			for _, o := range extra {
				if o.ID == n.OperationID {
					a.ymReportExtra(ctx, n.Label, []yoomoney.Operation{o})
				}
			}
		}
		return true, nil
	}
	a.finalizeYooMoney(ctx, n.Label, n.OperationID, n.WithdrawK, n.AmountK)
	if _, extra := a.ymPickOp(paid, p); len(extra) > 0 {
		var others []yoomoney.Operation
		for _, o := range extra {
			if o.ID != n.OperationID {
				others = append(others, o)
			}
		}
		a.ymReportExtra(ctx, n.Label, others)
	}
	a.log.Info("yoomoney webhook: finalized", "label", n.Label)
	return true, nil
}

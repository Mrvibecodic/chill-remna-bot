package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
)

// Сверка оплат звёздами по истории транзакций бота.
//
// Зачем. У всех остальных способов есть строка в очереди счетов и внешний
// идентификатор, по которому счёт можно переспросить у провайдера. У звёзд ни
// того, ни другого: идентификатор появляется только в момент оплаты, а метода
// «оплачен ли этот счёт» в Bot API нет вовсе. При этом Telegram подтверждает
// получение апдейта СРАЗУ, не дожидаясь, пока бот его обработает, — а выдача
// ходит в панель до трёх запросов по 15 секунд. Перезапуск в это окно убивал
// оплату навсегда: звёзды списаны, подписки нет, следа в журнале нет.
//
// Единственный способ догнать такую оплату — постфактум, по getStarTransactions.
// Там есть всё нужное: идентификатор (он же ключ идемпотентности и он же
// telegram_payment_charge_id), плательщик, payload счёта и сумма.

// starsReconLimit — размер страницы истории. Больше 100 Telegram не отдаёт.
const starsReconLimit = 100

// starsReconPages — сколько страниц смотрим за проход. Хватает с запасом:
// проход идёт каждые две минуты, а незакрытых оплат в норме нет вовсе.
const starsReconPages = 3

// reconcileStars догоняет оплаты звёздами, апдейт о которых не дожил до выдачи,
// и подхватывает возвраты, о которых бот не узнал.
func (a *App) reconcileStars(ctx context.Context) {
	a.mu.Lock()
	st := a.store
	msg := a.msg
	a.mu.Unlock()
	if st == nil || msg == nil || !a.starsConfig().Enabled {
		return
	}
	for page := 0; page < starsReconPages; page++ {
		txs, err := msg.StarTransactions(ctx, page*starsReconLimit, starsReconLimit)
		if err != nil {
			// Разбор ответа ломается целиком, если Telegram добавит новый тип
			// партнёра. Это не повод шуметь каждые две минуты — просто выходим.
			a.log.Warn("сверка звёзд: история не прочитана", "err", err)
			return
		}
		if len(txs) == 0 {
			return
		}
		for i := range txs {
			a.reconcileStarTx(ctx, &txs[i])
		}
		if len(txs) < starsReconLimit {
			return
		}
	}
}

// reconcileStarTx разбирает одну строку истории.
func (a *App) reconcileStarTx(ctx context.Context, tx *models.StarTransaction) {
	if tx == nil || tx.ID == "" {
		return
	}
	// Возврат: у исходящей операции заполнен получатель, а идентификатор тот
	// же, что у оплаты. Подстраховка на случай пропущенного апдейта о возврате.
	if tx.Receiver != nil {
		if a.store != nil {
			if done, _ := a.store.PaymentByExtID(ctx, tx.ID); done {
				if err := a.store.SetPaymentStatus(ctx, tx.ID, model.PaymentRefunded); err != nil {
					a.log.Warn("сверка звёзд: платёж не помечен возвращённым", "err", err, "id", tx.ID)
				}
			}
		}
		return
	}
	if tx.Source == nil || tx.Source.User == nil {
		return
	}
	u := tx.Source.User
	if u.TransactionType != "" && u.TransactionType != "invoice_payment" {
		return
	}
	if !strings.HasPrefix(u.InvoicePayload, "stars:") {
		return
	}
	months, _ := starsPayload(u.InvoicePayload)
	if months <= 0 {
		return
	}
	payer := u.User.ID
	if payer == 0 {
		return
	}
	if done, _ := a.store.PaymentByExtID(ctx, tx.ID); done {
		return
	}
	snap, ok := a.starsSnapshotForAmount(ctx, payer, months, tx.Amount)
	if !ok {
		// Условия счёта не нашлись (снимок мог протухнуть за 30 дней) — это
		// разбор для человека, а не для автоматики.
		a.payLogThrottled(ctx, "stars-recon-"+tx.ID, model.PayMethodStars, tx.ID, payer, "error",
			"сверка: оплата %d⭐ не сопоставлена с условиями счёта", tx.Amount)
		return
	}
	amount := strconv.Itoa(tx.Amount) + " ⭐"
	a.payLog(ctx, model.PayMethodStars, tx.ID, payer, "reconcile", "оплата найдена сверкой: %s months=%d", amount, months)
	link, expireAt, err := a.finalizePurchase(ctx, payer, months, model.PayMethodStars, amount, tx.ID, snap)
	if err != nil {
		a.payLog(ctx, model.PayMethodStars, tx.ID, payer, "finalize_error", "сверка: %v", err)
		return
	}
	a.sendSubActive(ctx, payer, link, expireAt)
	a.log.Info("сверка звёзд: оплата догнана", "chat_id", payer, "months", months, "id", tx.ID)
}

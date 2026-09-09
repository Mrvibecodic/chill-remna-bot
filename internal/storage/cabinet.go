package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"remnabot/internal/model"
)

// Хранилище кабинетных аккаунтов: подтверждение почты, одноразовые ссылки из
// писем, поколение пропусков и перенос аккаунта на телеграмный идентификатор.

func (b *base) SetWebUserVerified(ctx context.Context, tgID int64, at string) error {
	if at == "" {
		at = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		"UPDATE web_users SET email_verified_at = "+b.ph(1)+" WHERE tg_id = "+b.ph(2), at, tgID)
	return err
}

func (b *base) SetWebUserPassword(ctx context.Context, tgID int64, hash string) error {
	res, err := b.db.ExecContext(ctx,
		"UPDATE web_users SET pass_hash = "+b.ph(1)+" WHERE tg_id = "+b.ph(2), hash, tgID)
	if err != nil {
		return err
	}
	// Отсутствие строки — не «успешно ничего не сделали»: вызывающий сообщит
	// человеку «пароль изменён», а пароль остался бы прежним.
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UserSessEpoch — поколение пропусков аккаунта. Нет строки — ноль: пропуск
// такого аккаунта всё равно не пройдёт дальше по общим проверкам.
func (b *base) UserSessEpoch(ctx context.Context, tgID int64) (int, error) {
	var n int
	err := b.db.QueryRowContext(ctx,
		"SELECT sess_epoch FROM users WHERE telegram_id = "+b.ph(1), tgID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// BumpSessEpoch поднимает поколение и возвращает новое значение: выданные
// ранее пропуска этого аккаунта перестают приниматься немедленно.
func (b *base) BumpSessEpoch(ctx context.Context, tgID int64) (int, error) {
	if _, err := b.db.ExecContext(ctx,
		"UPDATE users SET sess_epoch = sess_epoch + 1 WHERE telegram_id = "+b.ph(1), tgID); err != nil {
		return 0, err
	}
	return b.UserSessEpoch(ctx, tgID)
}

func (b *base) PutEmailToken(ctx context.Context, t *model.EmailToken) error {
	if t == nil || t.Hash == "" {
		return errors.New("пустая ссылка")
	}
	if t.CreatedAt == "" {
		t.CreatedAt = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO email_tokens (hash, tg_id, purpose, email, created_at, expires_at, used_at) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", '')",
		t.Hash, t.TgID, t.Purpose, t.Email, t.CreatedAt, t.ExpiresAt)
	return err
}

// TakeEmailToken гасит ссылку и возвращает её содержимое. Гашение и проверка —
// один запрос: две параллельные вкладки с одной ссылкой не должны обе получить
// «годится», иначе однократность существует только на бумаге.
func (b *base) TakeEmailToken(ctx context.Context, hash, purpose string) (*model.EmailToken, error) {
	now := nowStr()
	res, err := b.db.ExecContext(ctx,
		"UPDATE email_tokens SET used_at = "+b.ph(1)+" WHERE hash = "+b.ph(2)+" AND purpose = "+b.ph(3)+
			" AND used_at = '' AND expires_at > "+b.ph(4), now, hash, purpose, now)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	t := &model.EmailToken{}
	err = b.db.QueryRowContext(ctx,
		"SELECT hash, tg_id, purpose, email, created_at, expires_at, used_at FROM email_tokens WHERE hash = "+b.ph(1), hash).
		Scan(&t.Hash, &t.TgID, &t.Purpose, &t.Email, &t.CreatedAt, &t.ExpiresAt, &t.UsedAt)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// RevokeEmailTokens гасит прежние ссылки того же назначения. Зовётся при выдаче
// новой: иначе старое письмо остаётся рабочим ключом ещё сутки.
//
// Строки именно гасятся, а не удаляются: по ним считается, сколько писем
// аккаунту уже отправлено, и удаление обнуляло бы порог отправки при каждом
// новом письме — то есть отменяло бы его.
func (b *base) RevokeEmailTokens(ctx context.Context, tgID int64, purpose string) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE email_tokens SET used_at = "+b.ph(1)+" WHERE tg_id = "+b.ph(2)+" AND purpose = "+b.ph(3)+" AND used_at = ''",
		nowStr(), tgID, purpose)
	return err
}

// CountEmailTokensSince — сколько ссылок этого назначения выдано аккаунту с
// указанного момента. По нему считается порог отправки писем.
func (b *base) CountEmailTokensSince(ctx context.Context, tgID int64, purpose, since string) (int, error) {
	var n int
	err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM email_tokens WHERE tg_id = "+b.ph(1)+" AND purpose = "+b.ph(2)+" AND created_at > "+b.ph(3),
		tgID, purpose, since).Scan(&n)
	return n, err
}

// PurgeEmailTokens убирает давно просроченные записи. Порог берётся с большим
// запасом: свежие строки нужны счётчику отправленных писем.
func (b *base) PurgeEmailTokens(ctx context.Context, before string) error {
	_, err := b.db.ExecContext(ctx,
		"DELETE FROM email_tokens WHERE expires_at < "+b.ph(1), before)
	return err
}

// AccountFootprint — следы аккаунта в боте. По нему решается, свободен ли
// телеграмный идентификатор для привязки кабинетного аккаунта.
type AccountFootprint struct {
	// HasRow — строка в users есть (её заводит первый же /start).
	HasRow bool
	// Active — за строкой что-то стоит: деньги, подписка, история, согласие.
	// Пустая строка от одного /start к таким не относится: переносить нечего.
	Active bool
}

// AccountFootprint отвечает, есть ли у телеграмного идентификатора аккаунт в
// боте. Привязка кабинетного аккаунта к занятому идентификатору не делается:
// слить две истории покупок автоматически нельзя, не рискуя оплаченным.
func (b *base) AccountFootprint(ctx context.Context, tgID int64) (AccountFootprint, error) {
	var fp AccountFootprint
	var balance, referredBy, refEarned int64
	var subExp, trial, terms sql.NullString
	var whitelisted, approved, denied, p2pApproved, trialResets int
	err := b.db.QueryRowContext(ctx,
		"SELECT balance, sub_expire_at, trial_used_at, terms_accepted_at, referred_by, ref_earned, "+
			"whitelisted, web_approved, web_denied, p2p_approved, trial_resets FROM users WHERE telegram_id = "+b.ph(1), tgID).
		Scan(&balance, &subExp, &trial, &terms, &referredBy, &refEarned, &whitelisted, &approved, &denied, &p2pApproved, &trialResets)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Строки нет — идентификатор свободен, но историю всё равно проверяем:
		// платежи и заявки переживают удаление строки пользователя.
	case err != nil:
		return fp, err
	default:
		fp.HasRow = true
		fp.Active = balance != 0 || subExp.String != "" || trial.String != "" || terms.String != "" ||
			referredBy != 0 || refEarned != 0 || whitelisted != 0 || approved != 0 || denied != 0 ||
			p2pApproved != 0 || trialResets != 0
	}
	if fp.Active {
		return fp, nil
	}
	for _, q := range []string{
		"SELECT COUNT(*) FROM payments WHERE telegram_id = ",
		"SELECT COUNT(*) FROM p2p_requests WHERE telegram_id = ",
		"SELECT COUNT(*) FROM promo_redemptions WHERE telegram_id = ",
		"SELECT COUNT(*) FROM autopay WHERE telegram_id = ",
		"SELECT COUNT(*) FROM web_users WHERE tg_id = ",
	} {
		var n int
		if err := b.db.QueryRowContext(ctx, q+b.ph(1), tgID).Scan(&n); err != nil {
			return fp, err
		}
		if n > 0 {
			fp.Active = true
			return fp, nil
		}
	}
	return fp, nil
}

// accountTables — где живут строки, привязанные к идентификатору аккаунта.
// Список ровно один на весь перенос: разъехавшись, он оставил бы часть истории
// на исчезнувшем идентификаторе.
var accountTables = []struct{ table, col string }{
	{"users", "telegram_id"},
	{"web_users", "tg_id"},
	{"payments", "telegram_id"},
	{"payment_log", "telegram_id"},
	{"p2p_requests", "telegram_id"},
	{"pending_invoices", "telegram_id"},
	{"purchase_intents", "telegram_id"},
	{"invoice_snapshots", "telegram_id"},
	{"promo_redemptions", "telegram_id"},
	{"autopay", "telegram_id"},
	{"plan_access", "telegram_id"},
	{"torrent_reports", "telegram_id"},
	{"torrent_strikes", "tg_id"},
}

// MoveAccount переносит аккаунт с одного идентификатора на другой одной
// транзакцией: либо переезжает всё, либо не переезжает ничего.
//
// Целевой идентификатор обязан быть свободен (см. AccountFootprint) — слияние
// двух историй покупок здесь не делается намеренно.
func (b *base) MoveAccount(ctx context.Context, from, to int64) error {
	if from == to || from == 0 || to == 0 {
		return errors.New("перенос аккаунта: неверные идентификаторы")
	}
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Пустую строку от одного /start уносим с дороги: она ничего не хранит, а
	// первичный ключ users занимает.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM users WHERE telegram_id = "+b.ph(1), to); err != nil {
		return err
	}
	for _, t := range accountTables {
		// #nosec G202 -- имена таблиц и колонок берутся из константного списка выше, ввода пользователя здесь нет
		if _, err := tx.ExecContext(ctx,
			"UPDATE "+t.table+" SET "+t.col+" = "+b.ph(1)+" WHERE "+t.col+" = "+b.ph(2), to, from); err != nil {
			return fmt.Errorf("перенос %s: %w", t.table, err)
		}
	}
	return tx.Commit()
}

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"remnabot/internal/crypto"
	"remnabot/internal/model"
)

var ErrDuplicateExtID = errors.New("storage: payment with this ext_id already exists")

type Storage interface {
	Migrate(ctx context.Context) error

	LoadConfig(ctx context.Context) (*model.BotConfig, bool, error)
	SaveConfig(ctx context.Context, cfg *model.BotConfig) error

	GetScreenMsg(ctx context.Context, chatID int64) (int, error)
	SetScreenMsg(ctx context.Context, chatID int64, msgID int) error

	UpsertUser(ctx context.Context, telegramID int64) error

	SetUserInfo(ctx context.Context, telegramID int64, username, firstName string) error
	GetUser(ctx context.Context, telegramID int64) (*model.User, error)
	SetP2PApproved(ctx context.Context, telegramID int64, approved bool) error

	HasApprovedPurchase(ctx context.Context, telegramID int64) (bool, error)

	ListUsers(ctx context.Context, limit, offset int) ([]model.User, int, error)
	SetBlocked(ctx context.Context, telegramID int64, blocked bool) error
	SetWhitelisted(ctx context.Context, telegramID int64, on bool) error
	AddWhitelistID(ctx context.Context, telegramID int64) error
	RemoveWhitelistID(ctx context.Context, telegramID int64) error
	IsWhitelistID(ctx context.Context, telegramID int64) (bool, error)
	ListWhitelistIDs(ctx context.Context) ([]int64, error)
	DeleteUser(ctx context.Context, telegramID int64) error
	AllUserIDs(ctx context.Context) ([]int64, error)

	CreatePromo(ctx context.Context, p *model.PromoCode) error
	CreateWebUser(ctx context.Context, u *model.WebUser) error
	GetWebUserByEmail(ctx context.Context, email string) (*model.WebUser, error)
	GetWebUserByTgID(ctx context.Context, tgID int64) (*model.WebUser, error)
	SetWebApproved(ctx context.Context, tgID int64, approved bool) error
	SetWebDenied(ctx context.Context, tgID int64, denied bool) error
	GetPromo(ctx context.Context, code string) (*model.PromoCode, error)
	ListPromos(ctx context.Context) ([]model.PromoCode, error)
	DeletePromo(ctx context.Context, code string) error
	PromoRedeemedBy(ctx context.Context, code string, telegramID int64) (bool, error)
	RedeemPromo(ctx context.Context, code string, telegramID int64) error

	DeletePaymentsByUser(ctx context.Context, telegramID int64) error
	DeleteP2PRequestsByUser(ctx context.Context, telegramID int64) error

	SetTermsAccepted(ctx context.Context, telegramID int64, ts string) error

	SetTrialUsed(ctx context.Context, telegramID int64, ts string) error

	SetSubExpiry(ctx context.Context, telegramID int64, expireAt, kind string) error

	MarkNotified(ctx context.Context, telegramID int64, sentCSV string) error

	UsersForNotify(ctx context.Context) ([]model.User, error)

	AddBalance(ctx context.Context, telegramID int64, kopecks int64) error

	DeductBalance(ctx context.Context, telegramID int64, kopecks int64) (bool, error)

	SetReferredBy(ctx context.Context, telegramID, referrerID int64) error
	SetRefBonusPaid(ctx context.Context, telegramID int64) error
	AddRefEarned(ctx context.Context, telegramID int64, kopecks int64) error
	CountReferrals(ctx context.Context, referrerID int64) (int, error)

	CreateP2PRequest(ctx context.Context, r *model.P2PRequest) error
	GetP2PRequest(ctx context.Context, id int64) (*model.P2PRequest, error)
	UpdateP2PRequest(ctx context.Context, r *model.P2PRequest) error

	AddPayment(ctx context.Context, p *model.Payment) error
	ListPayments(ctx context.Context, limit, offset int) ([]model.Payment, int, error)
	HasPaidPayment(ctx context.Context, telegramID int64) (bool, error)

	PaidPayments(ctx context.Context) ([]model.Payment, error)
	PaymentByExtID(ctx context.Context, extID string) (bool, error)

	MostPopularPlan(ctx context.Context) (months int, total int, err error)

	LoadMediaFileID(ctx context.Context, section string) (id string, ok bool, err error)
	SaveMediaFileID(ctx context.Context, section, fileID string) error

	DeleteMediaFileID(ctx context.Context, section string) error

	Export(ctx context.Context) (*Snapshot, error)
	Import(ctx context.Context, s *Snapshot) error

	AddPendingInvoice(ctx context.Context, p *model.PendingInvoice) error

	ListUnresolvedPending(ctx context.Context, createdBefore string, limit int) ([]model.PendingInvoice, error)
	ResolvePending(ctx context.Context, id int64) error

	PendingByExtID(ctx context.Context, extID string) (*model.PendingInvoice, error)

	AddPayLog(ctx context.Context, e *model.PayLogEntry) error
	PayLogs(ctx context.Context, extID string, telegramID int64, limit int) ([]model.PayLogEntry, error)
	AllPayLogs(ctx context.Context, limit int) ([]model.PayLogEntry, error)
	PurgePayLogs(ctx context.Context, before string) error

	Kind() string
	Close() error
}

func Open(kind, dsn string, crypter *crypto.Crypter) (Storage, error) {
	switch kind {
	case model.DBSQLite:
		return openSQLite(dsn, crypter)
	case model.DBPostgres:
		return openPostgres(dsn, crypter)
	default:
		return nil, fmt.Errorf("неизвестный движок БД: %q", kind)
	}
}

type base struct {
	db      *sql.DB
	kind    string
	ph      placeholderFunc
	crypter *crypto.Crypter
}

type placeholderFunc func(n int) string

func (b *base) Kind() string { return b.kind }
func (b *base) Close() error { return b.db.Close() }

// GetScreenMsg returns the persisted id of the last screen message for a chat
// (0 if none). Lets the bot delete the previous screen even after a restart,
// when the in-memory tracking map has been wiped.
func (b *base) GetScreenMsg(ctx context.Context, chatID int64) (int, error) {
	var id int
	err := b.db.QueryRowContext(ctx,
		"SELECT msg_id FROM screen_state WHERE chat_id = "+b.ph(1), chatID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// SetScreenMsg persists the id of the last screen message shown to a chat.
func (b *base) SetScreenMsg(ctx context.Context, chatID int64, msgID int) error {
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO screen_state (chat_id, msg_id) VALUES ("+b.ph(1)+", "+b.ph(2)+") "+
			"ON CONFLICT(chat_id) DO UPDATE SET msg_id = excluded.msg_id",
		chatID, msgID)
	return err
}

func (b *base) loadConfig(ctx context.Context) (*model.BotConfig, bool, error) {
	var enc string
	err := b.db.QueryRowContext(ctx, "SELECT config FROM settings WHERE id = 1").Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	plain, err := b.crypter.Decrypt(enc)
	if err != nil {
		return nil, false, fmt.Errorf("расшифровка конфига: %w", err)
	}
	var cfg model.BotConfig
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return nil, false, fmt.Errorf("разбор конфига: %w", err)
	}
	return &cfg, true, nil
}

func (b *base) saveConfig(ctx context.Context, cfg *model.BotConfig, upsertSQL string) error {
	plain, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	enc, err := b.crypter.Encrypt(plain)
	if err != nil {
		return err
	}
	_, err = b.db.ExecContext(ctx, upsertSQL, enc)
	return err
}

func Transfer(ctx context.Context, src, dst Storage) error {
	snap, err := src.Export(ctx)
	if err != nil {
		return err
	}
	if snap.Config != nil {
		if err := dst.SaveConfig(ctx, snap.Config); err != nil {
			return err
		}
	}
	return dst.Import(ctx, snap)
}

func nowStr() string { return time.Now().UTC().Format(time.RFC3339) }
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (b *base) UpsertUser(ctx context.Context, telegramID int64) error {
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO users (telegram_id, p2p_approved, created_at) VALUES ("+b.ph(1)+", 0, "+b.ph(2)+") "+
			"ON CONFLICT (telegram_id) DO NOTHING",
		telegramID, nowStr())
	return err
}

func (b *base) SetUserInfo(ctx context.Context, telegramID int64, username, firstName string) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET username = "+b.ph(1)+", first_name = "+b.ph(2)+" WHERE telegram_id = "+b.ph(3),
		username, firstName, telegramID)
	return err
}

func (b *base) HasApprovedPurchase(ctx context.Context, telegramID int64) (bool, error) {
	var n int
	err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM p2p_requests WHERE telegram_id = "+b.ph(1)+" AND status = "+b.ph(2),
		telegramID, model.P2PApproved).Scan(&n)
	return n > 0, err
}

func (b *base) GetUser(ctx context.Context, telegramID int64) (*model.User, error) {
	var approved, blocked int
	var created, username, firstName string

	var terms, trial sql.NullString
	var subExp, notifyKind, notifySent string
	var balance, referredBy int64
	var refBonusPaid, whitelisted int
	var refEarned int64
	var webApproved, webDenied int
	err := b.db.QueryRowContext(ctx,
		"SELECT username, first_name, p2p_approved, blocked, created_at, terms_accepted_at, trial_used_at, sub_expire_at, notify_kind, notify_sent, balance, referred_by, ref_bonus_paid, whitelisted, ref_earned, web_approved, web_denied FROM users WHERE telegram_id = "+b.ph(1), telegramID).
		Scan(&username, &firstName, &approved, &blocked, &created, &terms, &trial, &subExp, &notifyKind, &notifySent, &balance, &referredBy, &refBonusPaid, &whitelisted, &refEarned, &webApproved, &webDenied)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &model.User{TelegramID: telegramID, Username: username, FirstName: firstName, P2PApproved: approved != 0, Blocked: blocked != 0, CreatedAt: created, TermsAcceptedAt: terms.String, TrialUsedAt: trial.String, SubExpireAt: subExp, NotifyKind: notifyKind, NotifySent: notifySent, Balance: balance, ReferredBy: referredBy, RefBonusPaid: refBonusPaid != 0, Whitelisted: whitelisted != 0, RefEarned: refEarned, WebApproved: webApproved != 0, WebDenied: webDenied != 0}, nil
}

func (b *base) SetP2PApproved(ctx context.Context, telegramID int64, approved bool) error {
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO users (telegram_id, p2p_approved, created_at) VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+") "+
			"ON CONFLICT (telegram_id) DO UPDATE SET p2p_approved = excluded.p2p_approved",
		telegramID, boolToInt(approved), nowStr())
	return err
}

func (b *base) ListUsers(ctx context.Context, limit, offset int) ([]model.User, int, error) {
	var total int
	if err := b.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM users").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := b.db.QueryContext(ctx,
		"SELECT telegram_id, username, first_name, p2p_approved, blocked, created_at FROM users "+
			"ORDER BY created_at DESC, telegram_id DESC LIMIT "+b.ph(1)+" OFFSET "+b.ph(2),
		limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.User
	for rows.Next() {
		var u model.User
		var approved, blocked int
		if err := rows.Scan(&u.TelegramID, &u.Username, &u.FirstName, &approved, &blocked, &u.CreatedAt); err != nil {
			return nil, 0, err
		}
		u.P2PApproved = approved != 0
		u.Blocked = blocked != 0
		out = append(out, u)
	}
	return out, total, rows.Err()
}

func (b *base) SetBlocked(ctx context.Context, telegramID int64, blocked bool) error {
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO users (telegram_id, p2p_approved, blocked, created_at) VALUES ("+b.ph(1)+", 0, "+b.ph(2)+", "+b.ph(3)+") "+
			"ON CONFLICT (telegram_id) DO UPDATE SET blocked = excluded.blocked",
		telegramID, boolToInt(blocked), nowStr())
	return err
}

func (b *base) DeleteUser(ctx context.Context, telegramID int64) error {
	_, err := b.db.ExecContext(ctx, "DELETE FROM users WHERE telegram_id = "+b.ph(1), telegramID)
	return err
}

func (b *base) DeletePaymentsByUser(ctx context.Context, telegramID int64) error {
	_, err := b.db.ExecContext(ctx, "DELETE FROM payments WHERE telegram_id = "+b.ph(1), telegramID)
	return err
}

func (b *base) DeleteP2PRequestsByUser(ctx context.Context, telegramID int64) error {
	_, err := b.db.ExecContext(ctx, "DELETE FROM p2p_requests WHERE telegram_id = "+b.ph(1), telegramID)
	return err
}

func (b *base) SetTermsAccepted(ctx context.Context, telegramID int64, ts string) error {

	if ts == "" {
		return nil
	}
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET terms_accepted_at = "+b.ph(1)+" WHERE telegram_id = "+b.ph(2),
		ts, telegramID)
	return err
}

func (b *base) SetTrialUsed(ctx context.Context, telegramID int64, ts string) error {
	if ts == "" {
		return nil
	}
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET trial_used_at = "+b.ph(1)+" WHERE telegram_id = "+b.ph(2),
		ts, telegramID)
	return err
}

func (b *base) SetSubExpiry(ctx context.Context, telegramID int64, expireAt, kind string) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET sub_expire_at = "+b.ph(1)+", notify_kind = "+b.ph(2)+", notify_sent = '' WHERE telegram_id = "+b.ph(3),
		expireAt, kind, telegramID)
	return err
}

func (b *base) MarkNotified(ctx context.Context, telegramID int64, sentCSV string) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET notify_sent = "+b.ph(1)+" WHERE telegram_id = "+b.ph(2),
		sentCSV, telegramID)
	return err
}

func (b *base) AddBalance(ctx context.Context, telegramID int64, kopecks int64) error {
	if kopecks == 0 {
		return nil
	}

	if _, err := b.db.ExecContext(ctx,
		"INSERT INTO users (telegram_id, p2p_approved, created_at) VALUES ("+b.ph(1)+", 0, "+b.ph(2)+") ON CONFLICT (telegram_id) DO NOTHING",
		telegramID, nowStr()); err != nil {
		return err
	}
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET balance = balance + "+b.ph(1)+" WHERE telegram_id = "+b.ph(2),
		kopecks, telegramID)
	return err
}

func (b *base) DeductBalance(ctx context.Context, telegramID int64, kopecks int64) (bool, error) {
	if kopecks <= 0 {
		return false, nil
	}
	res, err := b.db.ExecContext(ctx,
		"UPDATE users SET balance = balance - "+b.ph(1)+" WHERE telegram_id = "+b.ph(2)+" AND balance >= "+b.ph(3),
		kopecks, telegramID, kopecks)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (b *base) UsersForNotify(ctx context.Context) ([]model.User, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT telegram_id, username, first_name, sub_expire_at, notify_kind, notify_sent FROM users WHERE sub_expire_at <> ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.TelegramID, &u.Username, &u.FirstName, &u.SubExpireAt, &u.NotifyKind, &u.NotifySent); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (b *base) CreateP2PRequest(ctx context.Context, r *model.P2PRequest) error {
	if r.ID == 0 {
		r.ID = time.Now().UnixNano()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO p2p_requests (id, telegram_id, months, price, status, screenshot, comment, created_at, decided_at) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+", "+b.ph(8)+", "+b.ph(9)+")",
		r.ID, r.TelegramID, r.Months, r.Price, r.Status, r.Screenshot, r.Comment, r.CreatedAt, r.DecidedAt)
	return err
}

func (b *base) GetP2PRequest(ctx context.Context, id int64) (*model.P2PRequest, error) {
	r := &model.P2PRequest{}
	err := b.db.QueryRowContext(ctx,
		"SELECT id, telegram_id, months, price, status, screenshot, comment, created_at, decided_at "+
			"FROM p2p_requests WHERE id = "+b.ph(1), id).
		Scan(&r.ID, &r.TelegramID, &r.Months, &r.Price, &r.Status, &r.Screenshot, &r.Comment, &r.CreatedAt, &r.DecidedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (b *base) UpdateP2PRequest(ctx context.Context, r *model.P2PRequest) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE p2p_requests SET status = "+b.ph(1)+", screenshot = "+b.ph(2)+", comment = "+b.ph(3)+", decided_at = "+b.ph(4)+
			" WHERE id = "+b.ph(5),
		r.Status, r.Screenshot, r.Comment, r.DecidedAt, r.ID)
	return err
}

func (b *base) AddPayment(ctx context.Context, p *model.Payment) error {
	if p.ID == 0 {
		p.ID = time.Now().UnixNano()
	}
	if p.CreatedAt == "" {
		p.CreatedAt = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO payments (id, telegram_id, method, months, amount, status, comment, ext_id, created_at) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+", "+b.ph(8)+", "+b.ph(9)+")",
		p.ID, p.TelegramID, p.Method, p.Months, p.Amount, p.Status, p.Comment, p.ExtID, p.CreatedAt)
	if err != nil && isUniqueViolation(err) {
		return ErrDuplicateExtID
	}
	return err
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key")
}

func (b *base) ListPayments(ctx context.Context, limit, offset int) ([]model.Payment, int, error) {
	var total int
	if err := b.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM payments").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := b.db.QueryContext(ctx,
		"SELECT id, telegram_id, method, months, amount, status, comment, ext_id, created_at FROM payments "+
			"ORDER BY created_at DESC, id DESC LIMIT "+b.ph(1)+" OFFSET "+b.ph(2),
		limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.Payment
	for rows.Next() {
		var p model.Payment
		if err := rows.Scan(&p.ID, &p.TelegramID, &p.Method, &p.Months, &p.Amount, &p.Status, &p.Comment, &p.ExtID, &p.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

func (b *base) MostPopularPlan(ctx context.Context) (int, int, error) {
	var total int
	if err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM payments WHERE status = "+b.ph(1),
		model.PaymentPaid).Scan(&total); err != nil {
		return 0, 0, err
	}
	if total == 0 {
		return 0, 0, nil
	}
	var months int
	err := b.db.QueryRowContext(ctx,
		"SELECT months FROM payments WHERE status = "+b.ph(1)+
			" GROUP BY months ORDER BY COUNT(1) DESC, months ASC LIMIT 1",
		model.PaymentPaid).Scan(&months)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, total, nil
	}
	if err != nil {
		return 0, total, err
	}
	return months, total, nil
}

func (b *base) HasPaidPayment(ctx context.Context, telegramID int64) (bool, error) {
	var n int
	err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM payments WHERE telegram_id = "+b.ph(1)+" AND status = "+b.ph(2),
		telegramID, model.PaymentPaid).Scan(&n)
	return n > 0, err
}

func (b *base) PaidPayments(ctx context.Context) ([]model.Payment, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT id, telegram_id, method, months, amount, status, comment, ext_id, created_at FROM payments "+
			"WHERE status = "+b.ph(1)+" ORDER BY created_at DESC",
		model.PaymentPaid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Payment
	for rows.Next() {
		var p model.Payment
		if err := rows.Scan(&p.ID, &p.TelegramID, &p.Method, &p.Months, &p.Amount, &p.Status, &p.Comment, &p.ExtID, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (b *base) PaymentByExtID(ctx context.Context, extID string) (bool, error) {
	if extID == "" {
		return false, nil
	}
	var n int
	err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM payments WHERE ext_id = "+b.ph(1), extID).Scan(&n)
	return n > 0, err
}

func (b *base) LoadMediaFileID(ctx context.Context, section string) (string, bool, error) {
	var id string
	err := b.db.QueryRowContext(ctx,
		"SELECT file_id FROM media_cache WHERE section = "+b.ph(1), section).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func (b *base) SaveMediaFileID(ctx context.Context, section, fileID string) error {
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO media_cache (section, file_id, updated_at) VALUES ("+
			b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+") "+
			"ON CONFLICT (section) DO UPDATE SET file_id = excluded.file_id, updated_at = excluded.updated_at",
		section, fileID, nowStr())
	return err
}

func (b *base) DeleteMediaFileID(ctx context.Context, section string) error {
	_, err := b.db.ExecContext(ctx,
		"DELETE FROM media_cache WHERE section = "+b.ph(1), section)
	return err
}

type Snapshot struct {
	Config    *model.BotConfig
	Users     []model.User
	Payments  []model.Payment
	P2P       []model.P2PRequest
	Media     []MediaItem
	Promos    []model.PromoCode
	PromoUses []PromoUse
	PayLogs   []model.PayLogEntry
}

type PromoUse struct {
	Code       string
	TelegramID int64
	CreatedAt  string
}

type MediaItem struct {
	Section string
	FileID  string
}

func (b *base) Export(ctx context.Context) (*Snapshot, error) {
	snap := &Snapshot{}
	if cfg, ok, err := b.loadConfig(ctx); err != nil {
		return nil, err
	} else if ok {
		snap.Config = cfg
	}

	urows, err := b.db.QueryContext(ctx,
		"SELECT telegram_id, username, first_name, p2p_approved, blocked, created_at, terms_accepted_at, trial_used_at, sub_expire_at, notify_kind, notify_sent, balance, referred_by, ref_bonus_paid, whitelisted, ref_earned, web_approved, web_denied FROM users")
	if err != nil {
		return nil, err
	}
	for urows.Next() {
		var u model.User
		var approved, blocked, refBonusPaid, whitelisted int
		var refEarned int64
		var webApproved, webDenied int
		var terms, trial sql.NullString
		if err := urows.Scan(&u.TelegramID, &u.Username, &u.FirstName, &approved, &blocked, &u.CreatedAt, &terms, &trial, &u.SubExpireAt, &u.NotifyKind, &u.NotifySent, &u.Balance, &u.ReferredBy, &refBonusPaid, &whitelisted, &refEarned, &webApproved, &webDenied); err != nil {
			_ = urows.Close()
			return nil, err
		}
		u.P2PApproved = approved != 0
		u.Blocked = blocked != 0
		u.RefBonusPaid = refBonusPaid != 0
		u.Whitelisted = whitelisted != 0
		u.RefEarned = refEarned
		u.WebApproved = webApproved != 0
		u.WebDenied = webDenied != 0
		u.TermsAcceptedAt = terms.String
		u.TrialUsedAt = trial.String
		snap.Users = append(snap.Users, u)
	}
	if err := urows.Err(); err != nil {
		_ = urows.Close()
		return nil, err
	}
	_ = urows.Close()

	prows, err := b.db.QueryContext(ctx,
		"SELECT id, telegram_id, method, months, amount, status, comment, ext_id, created_at FROM payments")
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var p model.Payment
		if err := prows.Scan(&p.ID, &p.TelegramID, &p.Method, &p.Months, &p.Amount, &p.Status, &p.Comment, &p.ExtID, &p.CreatedAt); err != nil {
			_ = prows.Close()
			return nil, err
		}
		snap.Payments = append(snap.Payments, p)
	}
	if err := prows.Err(); err != nil {
		_ = prows.Close()
		return nil, err
	}
	_ = prows.Close()

	rrows, err := b.db.QueryContext(ctx,
		"SELECT id, telegram_id, months, price, status, screenshot, comment, created_at, decided_at FROM p2p_requests")
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var r model.P2PRequest
		if err := rrows.Scan(&r.ID, &r.TelegramID, &r.Months, &r.Price, &r.Status, &r.Screenshot, &r.Comment, &r.CreatedAt, &r.DecidedAt); err != nil {
			_ = rrows.Close()
			return nil, err
		}
		snap.P2P = append(snap.P2P, r)
	}
	if err := rrows.Err(); err != nil {
		_ = rrows.Close()
		return nil, err
	}
	_ = rrows.Close()

	mrows, err := b.db.QueryContext(ctx, "SELECT section, file_id FROM media_cache")
	if err != nil {
		return nil, err
	}
	for mrows.Next() {
		var m MediaItem
		if err := mrows.Scan(&m.Section, &m.FileID); err != nil {
			_ = mrows.Close()
			return nil, err
		}
		snap.Media = append(snap.Media, m)
	}
	if err := mrows.Err(); err != nil {
		_ = mrows.Close()
		return nil, err
	}
	_ = mrows.Close()

	if promos, err := b.ListPromos(ctx); err == nil {
		snap.Promos = promos
	} else {
		return nil, err
	}
	urows2, err := b.db.QueryContext(ctx, "SELECT code, telegram_id, created_at FROM promo_redemptions")
	if err != nil {
		return nil, err
	}
	for urows2.Next() {
		var u PromoUse
		if err := urows2.Scan(&u.Code, &u.TelegramID, &u.CreatedAt); err != nil {
			_ = urows2.Close()
			return nil, err
		}
		snap.PromoUses = append(snap.PromoUses, u)
	}
	if err := urows2.Err(); err != nil {
		_ = urows2.Close()
		return nil, err
	}
	_ = urows2.Close()

	lrows, err := b.db.QueryContext(ctx,
		"SELECT id, ext_id, telegram_id, method, stage, detail, created_at FROM payment_log")
	if err != nil {
		return nil, err
	}
	for lrows.Next() {
		var e model.PayLogEntry
		if err := lrows.Scan(&e.ID, &e.ExtID, &e.TelegramID, &e.Method, &e.Stage, &e.Detail, &e.CreatedAt); err != nil {
			_ = lrows.Close()
			return nil, err
		}
		snap.PayLogs = append(snap.PayLogs, e)
	}
	if err := lrows.Err(); err != nil {
		_ = lrows.Close()
		return nil, err
	}
	_ = lrows.Close()

	return snap, nil
}

func (b *base) Import(ctx context.Context, s *Snapshot) error {
	if s == nil {
		return nil
	}
	for i := range s.Users {
		if err := b.importUser(ctx, &s.Users[i]); err != nil {
			return err
		}
	}
	for i := range s.Payments {
		if err := b.AddPayment(ctx, &s.Payments[i]); err != nil && !errors.Is(err, ErrDuplicateExtID) {
			return err
		}
	}
	for i := range s.P2P {
		if err := b.CreateP2PRequest(ctx, &s.P2P[i]); err != nil && !isUniqueViolation(err) {
			return err
		}
	}
	for i := range s.Media {
		if err := b.SaveMediaFileID(ctx, s.Media[i].Section, s.Media[i].FileID); err != nil {
			return err
		}
	}
	for i := range s.Promos {
		if err := b.CreatePromo(ctx, &s.Promos[i]); err != nil {
			return err
		}
	}
	for i := range s.PromoUses {
		if _, err := b.db.ExecContext(ctx,
			"INSERT INTO promo_redemptions (code, telegram_id, created_at) VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+")",
			s.PromoUses[i].Code, s.PromoUses[i].TelegramID, s.PromoUses[i].CreatedAt); err != nil && !isUniqueViolation(err) {
			return err
		}
	}
	for i := range s.PayLogs {
		if err := b.AddPayLog(ctx, &s.PayLogs[i]); err != nil && !isUniqueViolation(err) {
			return err
		}
	}
	return nil
}

func (b *base) importUser(ctx context.Context, u *model.User) error {
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO users (telegram_id, p2p_approved, blocked, created_at, username, first_name, sub_expire_at, notify_kind, notify_sent, balance, referred_by, ref_bonus_paid, whitelisted, ref_earned, web_approved, web_denied) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+", "+b.ph(8)+", "+b.ph(9)+", "+b.ph(10)+", "+b.ph(11)+", "+b.ph(12)+", "+b.ph(13)+", "+b.ph(14)+", "+b.ph(15)+", "+b.ph(16)+") "+
			"ON CONFLICT (telegram_id) DO UPDATE SET "+
			"p2p_approved = excluded.p2p_approved, blocked = excluded.blocked, "+
			"created_at = excluded.created_at, username = excluded.username, first_name = excluded.first_name, "+
			"sub_expire_at = excluded.sub_expire_at, notify_kind = excluded.notify_kind, notify_sent = excluded.notify_sent, "+
			"balance = excluded.balance, referred_by = excluded.referred_by, ref_bonus_paid = excluded.ref_bonus_paid, whitelisted = excluded.whitelisted, ref_earned = excluded.ref_earned, web_approved = excluded.web_approved, web_denied = excluded.web_denied",
		u.TelegramID, boolToInt(u.P2PApproved), boolToInt(u.Blocked), u.CreatedAt, u.Username, u.FirstName,
		u.SubExpireAt, u.NotifyKind, u.NotifySent, u.Balance, u.ReferredBy, boolToInt(u.RefBonusPaid), boolToInt(u.Whitelisted), u.RefEarned, boolToInt(u.WebApproved), boolToInt(u.WebDenied))
	if err != nil {
		return err
	}
	if u.TermsAcceptedAt != "" {
		if err := b.SetTermsAccepted(ctx, u.TelegramID, u.TermsAcceptedAt); err != nil {
			return err
		}
	}
	if u.TrialUsedAt != "" {
		if err := b.SetTrialUsed(ctx, u.TelegramID, u.TrialUsedAt); err != nil {
			return err
		}
	}
	return nil
}

func (b *base) AddPayLog(ctx context.Context, e *model.PayLogEntry) error {
	if e.ID == 0 {
		e.ID = time.Now().UnixNano()
	}
	if e.CreatedAt == "" {
		e.CreatedAt = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO payment_log (id, ext_id, telegram_id, method, stage, detail, created_at) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+")",
		e.ID, e.ExtID, e.TelegramID, e.Method, e.Stage, e.Detail, e.CreatedAt)
	return err
}

func (b *base) AllPayLogs(ctx context.Context, limit int) ([]model.PayLogEntry, error) {
	if limit <= 0 {
		limit = 20000
	}
	rows, err := b.db.QueryContext(ctx,
		"SELECT id, ext_id, telegram_id, method, stage, detail, created_at FROM payment_log ORDER BY id DESC LIMIT "+b.ph(1), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PayLogEntry
	for rows.Next() {
		var e model.PayLogEntry
		if err := rows.Scan(&e.ID, &e.ExtID, &e.TelegramID, &e.Method, &e.Stage, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (b *base) PayLogs(ctx context.Context, extID string, telegramID int64, limit int) ([]model.PayLogEntry, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := b.db.QueryContext(ctx,
		"SELECT id, ext_id, telegram_id, method, stage, detail, created_at FROM payment_log "+
			"WHERE (ext_id <> '' AND ext_id = "+b.ph(1)+") OR ("+b.ph(2)+" > 0 AND telegram_id = "+b.ph(3)+") "+
			"ORDER BY id ASC LIMIT "+b.ph(4),
		extID, telegramID, telegramID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PayLogEntry
	for rows.Next() {
		var e model.PayLogEntry
		if err := rows.Scan(&e.ID, &e.ExtID, &e.TelegramID, &e.Method, &e.Stage, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (b *base) PurgePayLogs(ctx context.Context, before string) error {
	_, err := b.db.ExecContext(ctx,
		"DELETE FROM payment_log WHERE created_at < "+b.ph(1), before)
	return err
}

func (b *base) AddPendingInvoice(ctx context.Context, p *model.PendingInvoice) error {
	if p.ID == 0 {
		p.ID = time.Now().UnixNano()
	}
	if p.CreatedAt == "" {
		p.CreatedAt = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO pending_invoices (id, method, ext_id, telegram_id, months, created_at, resolved, purpose, kopecks) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", 0, "+b.ph(7)+", "+b.ph(8)+")",
		p.ID, p.Method, p.ExtID, p.TelegramID, p.Months, p.CreatedAt, p.Purpose, p.Kopecks)
	return err
}

func (b *base) ListUnresolvedPending(ctx context.Context, createdBefore string, limit int) ([]model.PendingInvoice, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT id, method, ext_id, telegram_id, months, created_at, purpose, kopecks FROM pending_invoices "+
			"WHERE resolved = 0 AND created_at <= "+b.ph(1)+" ORDER BY created_at ASC LIMIT "+b.ph(2),
		createdBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PendingInvoice
	for rows.Next() {
		var p model.PendingInvoice
		if err := rows.Scan(&p.ID, &p.Method, &p.ExtID, &p.TelegramID, &p.Months, &p.CreatedAt, &p.Purpose, &p.Kopecks); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (b *base) ResolvePending(ctx context.Context, id int64) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE pending_invoices SET resolved = 1 WHERE id = "+b.ph(1), id)
	return err
}

func (b *base) PendingByExtID(ctx context.Context, extID string) (*model.PendingInvoice, error) {
	if extID == "" {
		return nil, nil
	}
	p := &model.PendingInvoice{}
	err := b.db.QueryRowContext(ctx,
		"SELECT id, method, ext_id, telegram_id, months, created_at, purpose, kopecks FROM pending_invoices WHERE ext_id = "+b.ph(1)+" ORDER BY id DESC LIMIT 1", extID).
		Scan(&p.ID, &p.Method, &p.ExtID, &p.TelegramID, &p.Months, &p.CreatedAt, &p.Purpose, &p.Kopecks)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (b *base) SetReferredBy(ctx context.Context, telegramID, referrerID int64) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET referred_by = "+b.ph(1)+" WHERE telegram_id = "+b.ph(2)+" AND referred_by = 0",
		referrerID, telegramID)
	return err
}

func (b *base) AddRefEarned(ctx context.Context, telegramID int64, kopecks int64) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET ref_earned = ref_earned + "+b.ph(1)+" WHERE telegram_id = "+b.ph(2),
		kopecks, telegramID)
	return err
}

func (b *base) SetRefBonusPaid(ctx context.Context, telegramID int64) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET ref_bonus_paid = 1 WHERE telegram_id = "+b.ph(1), telegramID)
	return err
}

func (b *base) CountReferrals(ctx context.Context, referrerID int64) (int, error) {
	var n int
	err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM users WHERE referred_by = "+b.ph(1), referrerID).Scan(&n)
	return n, err
}

func (b *base) AllUserIDs(ctx context.Context) ([]int64, error) {
	rows, err := b.db.QueryContext(ctx, "SELECT telegram_id FROM users WHERE blocked = 0")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (b *base) CreateWebUser(ctx context.Context, u *model.WebUser) error {
	if u.CreatedAt == "" {
		u.CreatedAt = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO web_users (tg_id, email, pass_hash, created_at) VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+")",
		u.TgID, u.Email, u.PassHash, u.CreatedAt)
	return err
}

func (b *base) GetWebUserByTgID(ctx context.Context, tgID int64) (*model.WebUser, error) {
	u := &model.WebUser{}
	err := b.db.QueryRowContext(ctx,
		"SELECT tg_id, email, pass_hash, created_at FROM web_users WHERE tg_id = "+b.ph(1), tgID).
		Scan(&u.TgID, &u.Email, &u.PassHash, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (b *base) SetWebApproved(ctx context.Context, tgID int64, approved bool) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET web_approved = "+b.ph(1)+" WHERE telegram_id = "+b.ph(2),
		boolToInt(approved), tgID)
	return err
}

func (b *base) SetWebDenied(ctx context.Context, tgID int64, denied bool) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET web_denied = "+b.ph(1)+" WHERE telegram_id = "+b.ph(2),
		boolToInt(denied), tgID)
	return err
}

func (b *base) GetWebUserByEmail(ctx context.Context, email string) (*model.WebUser, error) {
	u := &model.WebUser{}
	err := b.db.QueryRowContext(ctx,
		"SELECT tg_id, email, pass_hash, created_at FROM web_users WHERE email = "+b.ph(1), email).
		Scan(&u.TgID, &u.Email, &u.PassHash, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (b *base) CreatePromo(ctx context.Context, p *model.PromoCode) error {
	if p.CreatedAt == "" {
		p.CreatedAt = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO promo_codes (code, kind, value, max_uses, used, expires_at, created_at) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+") "+
			"ON CONFLICT (code) DO UPDATE SET kind = excluded.kind, value = excluded.value, "+
			"max_uses = excluded.max_uses, expires_at = excluded.expires_at",
		p.Code, p.Kind, p.Value, p.MaxUses, p.Used, p.ExpiresAt, p.CreatedAt)
	return err
}

func (b *base) GetPromo(ctx context.Context, code string) (*model.PromoCode, error) {
	var p model.PromoCode
	err := b.db.QueryRowContext(ctx,
		"SELECT code, kind, value, max_uses, used, expires_at, created_at FROM promo_codes WHERE code = "+b.ph(1), code).
		Scan(&p.Code, &p.Kind, &p.Value, &p.MaxUses, &p.Used, &p.ExpiresAt, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (b *base) ListPromos(ctx context.Context) ([]model.PromoCode, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT code, kind, value, max_uses, used, expires_at, created_at FROM promo_codes ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PromoCode
	for rows.Next() {
		var p model.PromoCode
		if err := rows.Scan(&p.Code, &p.Kind, &p.Value, &p.MaxUses, &p.Used, &p.ExpiresAt, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (b *base) DeletePromo(ctx context.Context, code string) error {
	_, err := b.db.ExecContext(ctx, "DELETE FROM promo_codes WHERE code = "+b.ph(1), code)
	return err
}

func (b *base) PromoRedeemedBy(ctx context.Context, code string, telegramID int64) (bool, error) {
	var n int
	err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM promo_redemptions WHERE code = "+b.ph(1)+" AND telegram_id = "+b.ph(2),
		code, telegramID).Scan(&n)
	return n > 0, err
}

func (b *base) RedeemPromo(ctx context.Context, code string, telegramID int64) error {
	if _, err := b.db.ExecContext(ctx,
		"INSERT INTO promo_redemptions (code, telegram_id, created_at) VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+")",
		code, telegramID, nowStr()); err != nil {
		return err
	}
	_, err := b.db.ExecContext(ctx,
		"UPDATE promo_codes SET used = used + 1 WHERE code = "+b.ph(1), code)
	return err
}

func (b *base) SetWhitelisted(ctx context.Context, telegramID int64, on bool) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET whitelisted = "+b.ph(1)+" WHERE telegram_id = "+b.ph(2),
		boolToInt(on), telegramID)
	return err
}

// AddWhitelistID добавляет Telegram ID в предзаполненный вайтлист (до регистрации).
func (b *base) AddWhitelistID(ctx context.Context, telegramID int64) error {
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение telegramID передаётся биндовым параметром
		"INSERT INTO whitelist (telegram_id) VALUES ("+b.ph(1)+") ON CONFLICT(telegram_id) DO NOTHING",
		telegramID)
	return err
}

// RemoveWhitelistID убирает Telegram ID из предзаполненного вайтлиста.
func (b *base) RemoveWhitelistID(ctx context.Context, telegramID int64) error {
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение telegramID передаётся биндовым параметром
		"DELETE FROM whitelist WHERE telegram_id = "+b.ph(1), telegramID)
	return err
}

// IsWhitelistID сообщает, есть ли Telegram ID в предзаполненном вайтлисте.
func (b *base) IsWhitelistID(ctx context.Context, telegramID int64) (bool, error) {
	var x int
	err := b.db.QueryRowContext(ctx,
		"SELECT 1 FROM whitelist WHERE telegram_id = "+b.ph(1), telegramID).Scan(&x)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListWhitelistIDs возвращает все ID из предзаполненного вайтлиста.
func (b *base) ListWhitelistIDs(ctx context.Context) ([]int64, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT telegram_id FROM whitelist ORDER BY telegram_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

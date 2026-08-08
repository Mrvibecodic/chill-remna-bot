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
	WhitelistAllUsers(ctx context.Context) (int64, error)
	CountWhitelisted(ctx context.Context) (int, error)

	CreateInvite(ctx context.Context, inv *model.Invite) error
	GetInvite(ctx context.Context, code string) (*model.Invite, error)
	ListInvites(ctx context.Context) ([]model.Invite, error)
	UseInvite(ctx context.Context, code string) (bool, error)
	RevokeInvite(ctx context.Context, code string) error
	DeleteInvite(ctx context.Context, code string) error

	SetAutoPay(ctx context.Context, ap *model.AutoPay) error
	GetAutoPay(ctx context.Context, telegramID int64) (*model.AutoPay, error)
	SetAutoPayEnabled(ctx context.Context, telegramID int64, on bool) error
	UpdateAutoPayResult(ctx context.Context, telegramID int64, lastPayAt, nextTryAt string, fails int, lastError string) error
	MarkAutoPayCharged(ctx context.Context, telegramID int64, lastPayAt, paidPeriod, nextTryAt, lastError string) error
	ListAutoPay(ctx context.Context) ([]model.AutoPay, error)
	DeleteAutoPay(ctx context.Context, telegramID int64) error
	DeleteUser(ctx context.Context, telegramID int64) error
	AllUserIDs(ctx context.Context) ([]int64, error)

	SetPurchaseIntent(ctx context.Context, in *model.PurchaseIntent) error
	PurchaseIntent(ctx context.Context, telegramID int64) (*model.PurchaseIntent, error)
	DeletePurchaseIntent(ctx context.Context, telegramID int64) error
	DeletePurchaseIntentFor(ctx context.Context, telegramID int64, months int) error

	SetInvoiceSnapshot(ctx context.Context, telegramID int64, method string, months int, snap *model.PlanSnapshot) error
	InvoiceSnapshot(ctx context.Context, telegramID int64, method string, months int) (*model.PlanSnapshot, error)
	DeleteInvoiceSnapshot(ctx context.Context, telegramID int64, method string, months int) error

	SavePlan(ctx context.Context, p *model.Plan) error
	GetPlan(ctx context.Context, code string) (*model.Plan, error)
	ListPlans(ctx context.Context) ([]model.Plan, error)
	DeletePlan(ctx context.Context, code string) error

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
	SetUserSnapshot(ctx context.Context, telegramID int64, snap *model.PlanSnapshot) error
	ListSubRepairTargets(ctx context.Context) ([]SubRepairTarget, error)
	SetPaymentSnapshot(ctx context.Context, id int64, snap *model.PlanSnapshot) error
	LastPaidSubPayment(ctx context.Context, telegramID int64) (*model.Payment, error)

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
	// PayLogsFiltered отбирает записи журнала НА СТОРОНЕ БД по этапам и времени
	// и вторым значением отдаёт полное число подходящих записей. Именно полное
	// число, а не длину среза: иначе на нагруженном боте выгрузка молча теряла
	// бы всё, что не поместилось в лимит, и админ об этом не узнал бы.
	PayLogsFiltered(ctx context.Context, stages []string, since string, limit int) ([]model.PayLogEntry, int64, error)
	PurgePayLogs(ctx context.Context, before string) error

	AddTorrentReport(ctx context.Context, r *model.TorrentReport) error
	// TorrentReports — страница журнала (новые сверху) + общее число записей.
	TorrentReports(ctx context.Context, limit, offset int) ([]model.TorrentReport, int, error)
	// UserTorrentReports — страница журнала по одному нарушителю (новые сверху)
	// + общее число его отчётов. Идентичность та же, что у счётчика: telegram_id,
	// а у безтелеграмных аккаунтов — username панели.
	UserTorrentReports(ctx context.Context, telegramID int64, username string, limit, offset int) ([]model.TorrentReport, int, error)
	// CountTorrentReports — число отчётов по пользователю (по telegram_id, а
	// для аккаунтов без Telegram — по username панели) начиная с since.
	CountTorrentReports(ctx context.Context, telegramID int64, username, since string) (int, error)
	// CountTorrentReportsAll — число отчётов по всем; пустой since = за всё время.
	CountTorrentReportsAll(ctx context.Context, since string) (int, error)
	// DueTorrentUnblocks — записи, по которым пора уведомить о снятии
	// блокировки: срок вышел, уведомление не отправлено, есть telegram_id.
	DueTorrentUnblocks(ctx context.Context, now string) ([]model.TorrentReport, error)
	MarkTorrentUnblockNotified(ctx context.Context, id int64) error
	// PendingTorrentUnblocksByIP — ещё не отработанные записи по адресу:
	// нужны, когда админ снимает блокировку раньше срока вручную.
	PendingTorrentUnblocksByIP(ctx context.Context, ip string) ([]model.TorrentReport, error)
	// SetTorrentStrike/TorrentStrikeAt — момент последней автоблокировки по
	// торрентам. С него начинается новый отсчёт нарушений: иначе вернувшего
	// доступ пользователя выключало бы снова до конца окна повторов.
	SetTorrentStrike(ctx context.Context, telegramID int64, at string) error
	TorrentStrikeAt(ctx context.Context, telegramID int64) (string, error)
	PurgeTorrentReports(ctx context.Context, before string) error

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
	var snapRaw string
	err := b.db.QueryRowContext(ctx,
		"SELECT username, first_name, p2p_approved, blocked, created_at, terms_accepted_at, trial_used_at, sub_expire_at, notify_kind, notify_sent, balance, referred_by, ref_bonus_paid, whitelisted, ref_earned, web_approved, web_denied, plan_snapshot FROM users WHERE telegram_id = "+b.ph(1), telegramID).
		Scan(&username, &firstName, &approved, &blocked, &created, &terms, &trial, &subExp, &notifyKind, &notifySent, &balance, &referredBy, &refBonusPaid, &whitelisted, &refEarned, &webApproved, &webDenied, &snapRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &model.User{TelegramID: telegramID, Username: username, FirstName: firstName, P2PApproved: approved != 0, Blocked: blocked != 0, CreatedAt: created, TermsAcceptedAt: terms.String, TrialUsedAt: trial.String, SubExpireAt: subExp, NotifyKind: notifyKind, NotifySent: notifySent, Balance: balance, ReferredBy: referredBy, RefBonusPaid: refBonusPaid != 0, Whitelisted: whitelisted != 0, RefEarned: refEarned, WebApproved: webApproved != 0, WebDenied: webDenied != 0, Snapshot: model.DecodePlanSnapshot(snapRaw)}, nil
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
	// Автосписание лежит в отдельной таблице: если не удалить его вместе с
	// пользователем, планировщик продолжит списывать деньги за удалённого.
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
	_, _ = b.db.ExecContext(ctx, "DELETE FROM autopay WHERE telegram_id = "+b.ph(1), telegramID)
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
	_, _ = b.db.ExecContext(ctx, "DELETE FROM purchase_intents WHERE telegram_id = "+b.ph(1), telegramID)
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
	_, _ = b.db.ExecContext(ctx, "DELETE FROM invoice_snapshots WHERE telegram_id = "+b.ph(1), telegramID)
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
		"INSERT INTO p2p_requests (id, telegram_id, months, price, status, screenshot, comment, created_at, decided_at, plan_snapshot) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+", "+b.ph(8)+", "+b.ph(9)+", "+b.ph(10)+")",
		r.ID, r.TelegramID, r.Months, r.Price, r.Status, r.Screenshot, r.Comment, r.CreatedAt, r.DecidedAt, r.Snapshot.Encode())
	return err
}

func (b *base) GetP2PRequest(ctx context.Context, id int64) (*model.P2PRequest, error) {
	r := &model.P2PRequest{}
	var snapRaw string
	err := b.db.QueryRowContext(ctx,
		"SELECT "+p2pCols+" FROM p2p_requests WHERE id = "+b.ph(1), id).
		Scan(&r.ID, &r.TelegramID, &r.Months, &r.Price, &r.Status, &r.Screenshot, &r.Comment, &r.CreatedAt, &r.DecidedAt, &snapRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Snapshot = model.DecodePlanSnapshot(snapRaw)
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
		"INSERT INTO payments (id, telegram_id, method, months, amount, status, comment, ext_id, created_at, plan_snapshot) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+", "+b.ph(8)+", "+b.ph(9)+", "+b.ph(10)+")",
		p.ID, p.TelegramID, p.Method, p.Months, p.Amount, p.Status, p.Comment, p.ExtID, p.CreatedAt, p.Snapshot.Encode())
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
		"SELECT "+paymentCols+" FROM payments "+
			"ORDER BY created_at DESC, id DESC LIMIT "+b.ph(1)+" OFFSET "+b.ph(2),
		limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.Payment
	for rows.Next() {
		var p model.Payment
		var snapRaw string
		if err := rows.Scan(&p.ID, &p.TelegramID, &p.Method, &p.Months, &p.Amount, &p.Status, &p.Comment, &p.ExtID, &p.CreatedAt, &snapRaw); err != nil {
			return nil, 0, err
		}
		p.Snapshot = model.DecodePlanSnapshot(snapRaw)
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// subPaymentCond отбирает платежи, которые действительно являются покупкой
// подписки. Триал (`method='trial'`) и пополнения баланса пишутся в ту же
// таблицу с months = 0 — без этого условия они попадали и в «популярный
// тариф», и в признак «пользователь платил».
const subPaymentCond = " AND months > 0"

func (b *base) MostPopularPlan(ctx context.Context) (int, int, error) {
	var total int
	if err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM payments WHERE status = "+b.ph(1)+subPaymentCond,
		model.PaymentPaid).Scan(&total); err != nil {
		return 0, 0, err
	}
	if total == 0 {
		return 0, 0, nil
	}
	var months int
	err := b.db.QueryRowContext(ctx,
		"SELECT months FROM payments WHERE status = "+b.ph(1)+subPaymentCond+
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
		"SELECT COUNT(1) FROM payments WHERE telegram_id = "+b.ph(1)+" AND status = "+b.ph(2)+subPaymentCond,
		telegramID, model.PaymentPaid).Scan(&n)
	return n > 0, err
}

func (b *base) PaidPayments(ctx context.Context) ([]model.Payment, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT "+paymentCols+" FROM payments "+
			"WHERE status = "+b.ph(1)+" ORDER BY created_at DESC",
		model.PaymentPaid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Payment
	for rows.Next() {
		var p model.Payment
		var snapRaw string
		if err := rows.Scan(&p.ID, &p.TelegramID, &p.Method, &p.Months, &p.Amount, &p.Status, &p.Comment, &p.ExtID, &p.CreatedAt, &snapRaw); err != nil {
			return nil, err
		}
		p.Snapshot = model.DecodePlanSnapshot(snapRaw)
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

// Списки колонок вынесены в константы: снимок сделки добавил их сразу в
// несколько запросов, и расхождение между SELECT и Scan ловится только в
// рантайме.
const (
	paymentCols = "id, telegram_id, method, months, amount, status, comment, ext_id, created_at, plan_snapshot"
	p2pCols     = "id, telegram_id, months, price, status, screenshot, comment, created_at, decided_at, plan_snapshot"
	autoPayCols = "telegram_id, method, method_id, title, months, amount, currency, enabled, created_at, " +
		"last_pay_at, paid_period, next_try_at, fails, last_error, plan_snapshot"
)

// SubRepairTarget — пользователь с действующей подпиской. Условия сделки
// здесь намеренно НЕ хранятся: их источник — последняя покупка, а не история
// пользователя (см. App.repairUser).
type SubRepairTarget struct {
	TelegramID  int64
	SubExpireAt string
}

// ListSubRepairTargets возвращает кандидатов на сверку лимитов.
//
// Фильтра по users.plan_snapshot здесь нет намеренно: его пишет только новый
// образ бота, и отбор по нему выкинул бы ровно тех, ради кого сверка нужна
// больше всего, — людей, у которых ПЕРВАЯ покупка прошла во время отката.
func (b *base) ListSubRepairTargets(ctx context.Context) ([]SubRepairTarget, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT telegram_id, sub_expire_at FROM users "+
			"WHERE sub_expire_at <> '' AND blocked = 0")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubRepairTarget
	for rows.Next() {
		var t SubRepairTarget
		if err := rows.Scan(&t.TelegramID, &t.SubExpireAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LastPaidSubPayment — последняя оплаченная покупка подписки пользователя
// (пополнения баланса и триал сюда не попадают).
func (b *base) LastPaidSubPayment(ctx context.Context, telegramID int64) (*model.Payment, error) {
	p := &model.Payment{}
	var snapRaw string
	err := b.db.QueryRowContext(ctx,
		"SELECT "+paymentCols+" FROM payments WHERE telegram_id = "+b.ph(1)+
			" AND status = "+b.ph(2)+subPaymentCond+" ORDER BY created_at DESC, id DESC LIMIT 1",
		telegramID, model.PaymentPaid).
		Scan(&p.ID, &p.TelegramID, &p.Method, &p.Months, &p.Amount, &p.Status, &p.Comment, &p.ExtID, &p.CreatedAt, &snapRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Snapshot = model.DecodePlanSnapshot(snapRaw)
	return p, nil
}

// SetPaymentSnapshot дописывает снимок в уже записанный платёж.
func (b *base) SetPaymentSnapshot(ctx context.Context, id int64, snap *model.PlanSnapshot) error {
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"UPDATE payments SET plan_snapshot = "+b.ph(1)+" WHERE id = "+b.ph(2),
		snap.Encode(), id)
	return err
}

// SetUserSnapshot запоминает условия действующей подписки пользователя.
func (b *base) SetUserSnapshot(ctx context.Context, telegramID int64, snap *model.PlanSnapshot) error {
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"INSERT INTO users (telegram_id, p2p_approved, created_at, plan_snapshot) VALUES ("+b.ph(1)+", 0, "+b.ph(2)+", "+b.ph(3)+") "+
			"ON CONFLICT (telegram_id) DO UPDATE SET plan_snapshot = excluded.plan_snapshot",
		telegramID, nowStr(), snap.Encode())
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
	// AutoPays и Pendings раньше в снимок не входили: после переезда базы
	// или восстановления из бэкапа подключённые автосписания пропадали, а
	// незакрытые счета переставали добиваться реконсилятором.
	AutoPays []model.AutoPay
	Pendings []model.PendingInvoice
	// Plans — справочник тарифов. В снимок входит с самого появления таблицы:
	// без него переезд базы стирал бы всю тарифную сетку.
	Plans []model.Plan
	// Intents — незавершённые намерения покупки. Переезд базы посреди покупки
	// редок, но без них человек, выбравший год, доплачивал бы месяц.
	Intents []model.PurchaseIntent
	// InvoiceSnaps — условия выставленных счетов Stars: строки счёта у них
	// нет, и без переноса оплата пришла бы на текущие условия, а не на
	// проданные.
	InvoiceSnaps []InvoiceSnap
}

type PromoUse struct {
	Code       string
	TelegramID int64
	CreatedAt  string
}

// InvoiceSnap — строка условий выставленного счёта для снимка базы.
type InvoiceSnap struct {
	TelegramID int64
	Method     string
	Months     int
	Snapshot   *model.PlanSnapshot
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
		"SELECT telegram_id, username, first_name, p2p_approved, blocked, created_at, terms_accepted_at, trial_used_at, sub_expire_at, notify_kind, notify_sent, balance, referred_by, ref_bonus_paid, whitelisted, ref_earned, web_approved, web_denied, plan_snapshot FROM users")
	if err != nil {
		return nil, err
	}
	for urows.Next() {
		var u model.User
		var approved, blocked, refBonusPaid, whitelisted int
		var refEarned int64
		var webApproved, webDenied int
		var terms, trial sql.NullString
		var snapRaw string
		if err := urows.Scan(&u.TelegramID, &u.Username, &u.FirstName, &approved, &blocked, &u.CreatedAt, &terms, &trial, &u.SubExpireAt, &u.NotifyKind, &u.NotifySent, &u.Balance, &u.ReferredBy, &refBonusPaid, &whitelisted, &refEarned, &webApproved, &webDenied, &snapRaw); err != nil {
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
		u.Snapshot = model.DecodePlanSnapshot(snapRaw)
		snap.Users = append(snap.Users, u)
	}
	if err := urows.Err(); err != nil {
		_ = urows.Close()
		return nil, err
	}
	_ = urows.Close()

	prows, err := b.db.QueryContext(ctx,
		"SELECT "+paymentCols+" FROM payments")
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var p model.Payment
		var snapRaw string
		if err := prows.Scan(&p.ID, &p.TelegramID, &p.Method, &p.Months, &p.Amount, &p.Status, &p.Comment, &p.ExtID, &p.CreatedAt, &snapRaw); err != nil {
			_ = prows.Close()
			return nil, err
		}
		p.Snapshot = model.DecodePlanSnapshot(snapRaw)
		snap.Payments = append(snap.Payments, p)
	}
	if err := prows.Err(); err != nil {
		_ = prows.Close()
		return nil, err
	}
	_ = prows.Close()

	rrows, err := b.db.QueryContext(ctx,
		"SELECT "+p2pCols+" FROM p2p_requests")
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var r model.P2PRequest
		var snapRaw string
		if err := rrows.Scan(&r.ID, &r.TelegramID, &r.Months, &r.Price, &r.Status, &r.Screenshot, &r.Comment, &r.CreatedAt, &r.DecidedAt, &snapRaw); err != nil {
			_ = rrows.Close()
			return nil, err
		}
		r.Snapshot = model.DecodePlanSnapshot(snapRaw)
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
	if plans, err := b.ListPlans(ctx); err == nil {
		snap.Plans = plans
	} else {
		return nil, err
	}
	intentRows, err := b.db.QueryContext(ctx, "SELECT "+intentCols+" FROM purchase_intents")
	if err != nil {
		return nil, err
	}
	for intentRows.Next() {
		var in model.PurchaseIntent
		if err := intentRows.Scan(&in.TelegramID, &in.PlanCode, &in.Months, &in.Days, &in.CreatedAt); err != nil {
			_ = intentRows.Close()
			return nil, err
		}
		snap.Intents = append(snap.Intents, in)
	}
	if err := intentRows.Err(); err != nil {
		_ = intentRows.Close()
		return nil, err
	}
	_ = intentRows.Close()

	snapRows, err := b.db.QueryContext(ctx, "SELECT "+invoiceSnapCols+" FROM invoice_snapshots")
	if err != nil {
		return nil, err
	}
	for snapRows.Next() {
		var v InvoiceSnap
		var raw string
		if err := snapRows.Scan(&v.TelegramID, &v.Method, &v.Months, &raw, &v.CreatedAt); err != nil {
			_ = snapRows.Close()
			return nil, err
		}
		v.Snapshot = model.DecodePlanSnapshot(raw)
		snap.InvoiceSnaps = append(snap.InvoiceSnaps, v)
	}
	if err := snapRows.Err(); err != nil {
		_ = snapRows.Close()
		return nil, err
	}
	_ = snapRows.Close()
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

	arows, err := b.db.QueryContext(ctx,
		"SELECT "+autoPayCols+" FROM autopay")
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var ap model.AutoPay
		var enabled int
		var snapRaw string
		if err := arows.Scan(&ap.TelegramID, &ap.Method, &ap.MethodID, &ap.Title, &ap.Months, &ap.Amount,
			&ap.Currency, &enabled, &ap.CreatedAt, &ap.LastPayAt, &ap.PaidPeriod, &ap.NextTryAt, &ap.Fails, &ap.LastError, &snapRaw); err != nil {
			_ = arows.Close()
			return nil, err
		}
		ap.Enabled = enabled != 0
		ap.Snapshot = model.DecodePlanSnapshot(snapRaw)
		snap.AutoPays = append(snap.AutoPays, ap)
	}
	if err := arows.Err(); err != nil {
		_ = arows.Close()
		return nil, err
	}
	_ = arows.Close()

	irows, err := b.db.QueryContext(ctx,
		"SELECT id, method, ext_id, telegram_id, months, created_at, resolved, purpose, kopecks, plan_snapshot FROM pending_invoices")
	if err != nil {
		return nil, err
	}
	for irows.Next() {
		var p model.PendingInvoice
		var resolved int
		var snapRaw string
		if err := irows.Scan(&p.ID, &p.Method, &p.ExtID, &p.TelegramID, &p.Months, &p.CreatedAt, &resolved, &p.Purpose, &p.Kopecks, &snapRaw); err != nil {
			_ = irows.Close()
			return nil, err
		}
		p.Resolved = resolved != 0
		p.Snapshot = model.DecodePlanSnapshot(snapRaw)
		snap.Pendings = append(snap.Pendings, p)
	}
	if err := irows.Err(); err != nil {
		_ = irows.Close()
		return nil, err
	}
	_ = irows.Close()

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
	for i := range s.Plans {
		// Тариф с недопустимым кодом (например, записанный более новой
		// версией с другими правилами) пропускаем: обрывать переезд всей базы
		// из-за одной строки справочника нельзя.
		if err := b.SavePlan(ctx, &s.Plans[i]); err != nil {
			if !errors.Is(err, ErrPlanCode) {
				return err
			}
			// Молчать нельзя: иначе оператор уверен, что переехало всё.
			fmt.Printf("перенос базы: тариф %q пропущен — недопустимый код\n", s.Plans[i].Code)
		}
	}
	for i := range s.Intents {
		if err := b.SetPurchaseIntent(ctx, &s.Intents[i]); err != nil {
			return err
		}
	}
	for i := range s.InvoiceSnaps {
		v := &s.InvoiceSnaps[i]
		if err := b.SetInvoiceSnapshot(ctx, v.TelegramID, v.Method, v.Months, v.Snapshot); err != nil {
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
	for i := range s.AutoPays {
		if err := b.SetAutoPay(ctx, &s.AutoPays[i]); err != nil {
			return err
		}
	}
	for i := range s.Pendings {
		p := &s.Pendings[i]
		if _, err := b.db.ExecContext(ctx,
			// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
			"INSERT INTO pending_invoices (id, method, ext_id, telegram_id, months, created_at, resolved, purpose, kopecks, plan_snapshot) "+
				"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+", "+b.ph(8)+", "+b.ph(9)+", "+b.ph(10)+")",
			p.ID, p.Method, p.ExtID, p.TelegramID, p.Months, p.CreatedAt, boolToInt(p.Resolved), p.Purpose, p.Kopecks, p.Snapshot.Encode()); err != nil && !isUniqueViolation(err) {
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
	if u.Snapshot != nil {
		if err := b.SetUserSnapshot(ctx, u.TelegramID, u.Snapshot); err != nil {
			return err
		}
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

const torrentReportCols = "id, telegram_id, username, node, ip, protocol, inbound, source, destination, block_seconds, will_unblock_at, unblock_notified, created_at"

func (b *base) scanTorrentReport(rows *sql.Rows) (model.TorrentReport, error) {
	var r model.TorrentReport
	var notified int
	err := rows.Scan(&r.ID, &r.TelegramID, &r.Username, &r.Node, &r.IP, &r.Protocol, &r.Inbound,
		&r.Source, &r.Destination, &r.BlockSeconds, &r.WillUnblockAt, &notified, &r.CreatedAt)
	r.UnblockNotified = notified != 0
	return r, err
}

func (b *base) AddTorrentReport(ctx context.Context, r *model.TorrentReport) error {
	if r.ID == 0 {
		r.ID = time.Now().UnixNano()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = nowStr()
	}
	// ON CONFLICT DO NOTHING: панель переотправляет вебхук, если не дождалась
	// 200, а по журналу считаются страйки — дубликаты ускоряли бы отключение
	// подписки. Идемпотентный id вычисляет вызывающий (см. torrentReportID).
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO torrent_reports ("+torrentReportCols+") VALUES ("+
			b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+", "+
			b.ph(8)+", "+b.ph(9)+", "+b.ph(10)+", "+b.ph(11)+", "+b.ph(12)+", "+b.ph(13)+") "+
			"ON CONFLICT (id) DO NOTHING",
		r.ID, r.TelegramID, r.Username, r.Node, r.IP, r.Protocol, r.Inbound,
		r.Source, r.Destination, r.BlockSeconds, r.WillUnblockAt, boolToInt(r.UnblockNotified), r.CreatedAt)
	return err
}

func (b *base) TorrentReports(ctx context.Context, limit, offset int) ([]model.TorrentReport, int, error) {
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := b.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM torrent_reports").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := b.db.QueryContext(ctx,
		"SELECT "+torrentReportCols+" FROM torrent_reports ORDER BY id DESC LIMIT "+b.ph(1)+" OFFSET "+b.ph(2),
		limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.TorrentReport
	for rows.Next() {
		r, err := b.scanTorrentReport(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// torrentWho — отбор «этот нарушитель» для счётчика и журнала. Ветка выбирается
// в Go, а не условием в SQL: сравнение плейсхолдера с литералом («$2 <> 0»)
// заставляло Postgres выводить для параметра тип int4, и любой telegram_id
// больше 2^31 (то есть почти любой живой) ронял запрос с ошибкой кодирования.
// Заодно простой предикат по одной колонке нормально ложится на индекс.
func (b *base) torrentWho(telegramID int64, username string, from int) (string, []any) {
	if telegramID != 0 {
		return "telegram_id = " + b.ph(from), []any{telegramID}
	}
	return "username <> '' AND username = " + b.ph(from), []any{username}
}

func (b *base) UserTorrentReports(ctx context.Context, telegramID int64, username string, limit, offset int) ([]model.TorrentReport, int, error) {
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	who, args := b.torrentWho(telegramID, username, 1)
	where := " FROM torrent_reports WHERE " + who
	var total int
	if err := b.db.QueryRowContext(ctx, "SELECT COUNT(*)"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := b.db.QueryContext(ctx,
		"SELECT "+torrentReportCols+where+" ORDER BY id DESC LIMIT "+b.ph(len(args)+1)+" OFFSET "+b.ph(len(args)+2),
		append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.TorrentReport
	for rows.Next() {
		r, err := b.scanTorrentReport(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// CountTorrentReports — число отчётов по нарушителю. Пустой since означает «за
// всё время»: даты пишутся в RFC3339, а лексикографически любая строка >= "".
func (b *base) CountTorrentReports(ctx context.Context, telegramID int64, username, since string) (int, error) {
	who, args := b.torrentWho(telegramID, username, 1)
	q := "SELECT COUNT(*) FROM torrent_reports WHERE " + who
	if since != "" {
		q += " AND created_at >= " + b.ph(len(args)+1)
		args = append(args, since)
	}
	var n int
	err := b.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// CountTorrentReportsAll — число отчётов по всем сразу; пустой since = за всё время.
func (b *base) CountTorrentReportsAll(ctx context.Context, since string) (int, error) {
	q := "SELECT COUNT(*) FROM torrent_reports"
	var args []any
	if since != "" {
		q += " WHERE created_at >= " + b.ph(1)
		args = append(args, since)
	}
	var n int
	err := b.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

func (b *base) DueTorrentUnblocks(ctx context.Context, now string) ([]model.TorrentReport, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT "+torrentReportCols+" FROM torrent_reports "+
			"WHERE unblock_notified = 0 AND telegram_id <> 0 AND will_unblock_at <> '' AND will_unblock_at <= "+b.ph(1)+
			" ORDER BY id ASC LIMIT 500", now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TorrentReport
	for rows.Next() {
		r, err := b.scanTorrentReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (b *base) PendingTorrentUnblocksByIP(ctx context.Context, ip string) ([]model.TorrentReport, error) {
	if strings.TrimSpace(ip) == "" {
		return nil, nil
	}
	rows, err := b.db.QueryContext(ctx,
		"SELECT "+torrentReportCols+" FROM torrent_reports "+
			"WHERE unblock_notified = 0 AND ip = "+b.ph(1)+" ORDER BY id ASC LIMIT 500", ip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TorrentReport
	for rows.Next() {
		r, err := b.scanTorrentReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (b *base) MarkTorrentUnblockNotified(ctx context.Context, id int64) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE torrent_reports SET unblock_notified = 1 WHERE id = "+b.ph(1), id)
	return err
}

func (b *base) SetTorrentStrike(ctx context.Context, telegramID int64, at string) error {
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO torrent_strikes (tg_id, struck_at) VALUES ("+b.ph(1)+", "+b.ph(2)+") "+
			"ON CONFLICT (tg_id) DO UPDATE SET struck_at = excluded.struck_at",
		telegramID, at)
	return err
}

func (b *base) TorrentStrikeAt(ctx context.Context, telegramID int64) (string, error) {
	var at string
	err := b.db.QueryRowContext(ctx,
		"SELECT struck_at FROM torrent_strikes WHERE tg_id = "+b.ph(1), telegramID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return at, err
}

func (b *base) PurgeTorrentReports(ctx context.Context, before string) error {
	_, err := b.db.ExecContext(ctx,
		"DELETE FROM torrent_reports WHERE created_at < "+b.ph(1), before)
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

func (b *base) PayLogsFiltered(ctx context.Context, stages []string, since string, limit int) ([]model.PayLogEntry, int64, error) {
	if limit <= 0 {
		limit = 20000
	}
	where, args := "", []any{}
	if len(stages) > 0 {
		ph := make([]string, 0, len(stages))
		for _, st := range stages {
			args = append(args, st)
			ph = append(ph, b.ph(len(args)))
		}
		where = " WHERE stage IN (" + strings.Join(ph, ", ") + ")"
	}
	if since != "" {
		args = append(args, since)
		cond := "created_at >= " + b.ph(len(args))
		if where == "" {
			where = " WHERE " + cond
		} else {
			where += " AND " + cond
		}
	}

	var total int64
	// #nosec G202 -- where собран из b.ph плейсхолдеров, значения идут аргументами
	if err := b.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM payment_log"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit)
	// #nosec G202 -- см. выше
	rows, err := b.db.QueryContext(ctx,
		"SELECT id, ext_id, telegram_id, method, stage, detail, created_at FROM payment_log"+where+
			" ORDER BY id DESC LIMIT "+b.ph(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.PayLogEntry
	for rows.Next() {
		var e model.PayLogEntry
		if err := rows.Scan(&e.ID, &e.ExtID, &e.TelegramID, &e.Method, &e.Stage, &e.Detail, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
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
		"INSERT INTO pending_invoices (id, method, ext_id, telegram_id, months, created_at, resolved, purpose, kopecks, plan_snapshot) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", 0, "+b.ph(7)+", "+b.ph(8)+", "+b.ph(9)+")",
		p.ID, p.Method, p.ExtID, p.TelegramID, p.Months, p.CreatedAt, p.Purpose, p.Kopecks, p.Snapshot.Encode())
	return err
}

func (b *base) ListUnresolvedPending(ctx context.Context, createdBefore string, limit int) ([]model.PendingInvoice, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT id, method, ext_id, telegram_id, months, created_at, purpose, kopecks, plan_snapshot FROM pending_invoices "+
			"WHERE resolved = 0 AND created_at <= "+b.ph(1)+" ORDER BY created_at ASC LIMIT "+b.ph(2),
		createdBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PendingInvoice
	for rows.Next() {
		var p model.PendingInvoice
		var snapRaw string
		if err := rows.Scan(&p.ID, &p.Method, &p.ExtID, &p.TelegramID, &p.Months, &p.CreatedAt, &p.Purpose, &p.Kopecks, &snapRaw); err != nil {
			return nil, err
		}
		p.Snapshot = model.DecodePlanSnapshot(snapRaw)
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
	var snapRaw string
	err := b.db.QueryRowContext(ctx,
		"SELECT id, method, ext_id, telegram_id, months, created_at, purpose, kopecks, plan_snapshot FROM pending_invoices WHERE ext_id = "+b.ph(1)+" ORDER BY id DESC LIMIT 1", extID).
		Scan(&p.ID, &p.Method, &p.ExtID, &p.TelegramID, &p.Months, &p.CreatedAt, &p.Purpose, &p.Kopecks, &snapRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Snapshot = model.DecodePlanSnapshot(snapRaw)
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
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
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

// ---------------------------------------------------------------------------
// Приглашения (режим публичности «по приглашениям»)
// ---------------------------------------------------------------------------

// CreateInvite сохраняет новое приглашение. Код должен быть уникальным.
func (b *base) CreateInvite(ctx context.Context, inv *model.Invite) error {
	if inv.CreatedAt == "" {
		inv.CreatedAt = nowStr()
	}
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO invites (code, max_uses, used, expires_at, created_at, revoked, note) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+")",
		inv.Code, inv.MaxUses, inv.Used, inv.ExpiresAt, inv.CreatedAt, boolToInt(inv.Revoked), inv.Note)
	return err
}

func (b *base) GetInvite(ctx context.Context, code string) (*model.Invite, error) {
	var inv model.Invite
	var revoked int
	err := b.db.QueryRowContext(ctx,
		"SELECT code, max_uses, used, expires_at, created_at, revoked, note FROM invites WHERE code = "+b.ph(1), code).
		Scan(&inv.Code, &inv.MaxUses, &inv.Used, &inv.ExpiresAt, &inv.CreatedAt, &revoked, &inv.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	inv.Revoked = revoked != 0
	return &inv, nil
}

func (b *base) ListInvites(ctx context.Context) ([]model.Invite, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT code, max_uses, used, expires_at, created_at, revoked, note FROM invites ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Invite
	for rows.Next() {
		var inv model.Invite
		var revoked int
		if err := rows.Scan(&inv.Code, &inv.MaxUses, &inv.Used, &inv.ExpiresAt, &inv.CreatedAt, &revoked, &inv.Note); err != nil {
			return nil, err
		}
		inv.Revoked = revoked != 0
		out = append(out, inv)
	}
	return out, rows.Err()
}

// UseInvite атомарно «тратит» одну активацию приглашения: увеличивает счётчик
// только если приглашение не отозвано, не просрочено и лимит не исчерпан.
// Возвращает false, если приглашение недействительно (или его нет).
func (b *base) UseInvite(ctx context.Context, code string) (bool, error) {
	res, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"UPDATE invites SET used = used + 1 WHERE code = "+b.ph(1)+
			" AND revoked = 0"+
			" AND (max_uses <= 0 OR used < max_uses)"+
			" AND (expires_at = '' OR expires_at > "+b.ph(2)+")",
		code, nowStr())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (b *base) RevokeInvite(ctx context.Context, code string) error {
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"UPDATE invites SET revoked = 1 WHERE code = "+b.ph(1), code)
	return err
}

func (b *base) DeleteInvite(ctx context.Context, code string) error {
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
	_, err := b.db.ExecContext(ctx, "DELETE FROM invites WHERE code = "+b.ph(1), code)
	return err
}

// ---------------------------------------------------------------------------
// Автосписание
// ---------------------------------------------------------------------------

// SetAutoPay создаёт или перезаписывает запись автосписания пользователя.
func (b *base) SetAutoPay(ctx context.Context, ap *model.AutoPay) error {
	if ap.CreatedAt == "" {
		ap.CreatedAt = nowStr()
	}
	if ap.Method == "" {
		ap.Method = model.PayMethodYooKassa
	}
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"INSERT INTO autopay (telegram_id, method, method_id, title, months, amount, currency, enabled, created_at, last_pay_at, paid_period, next_try_at, fails, last_error, plan_snapshot) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+", "+b.ph(8)+", "+b.ph(9)+", "+b.ph(10)+", "+b.ph(11)+", "+b.ph(12)+", "+b.ph(13)+", "+b.ph(14)+", "+b.ph(15)+") "+
			"ON CONFLICT (telegram_id) DO UPDATE SET method = excluded.method, method_id = excluded.method_id, "+
			"title = excluded.title, months = excluded.months, amount = excluded.amount, currency = excluded.currency, "+
			"enabled = excluded.enabled, last_pay_at = excluded.last_pay_at, paid_period = excluded.paid_period, "+
			"next_try_at = excluded.next_try_at, fails = excluded.fails, last_error = excluded.last_error, "+
			"plan_snapshot = excluded.plan_snapshot",
		ap.TelegramID, ap.Method, ap.MethodID, ap.Title, ap.Months, ap.Amount, ap.Currency,
		boolToInt(ap.Enabled), ap.CreatedAt, ap.LastPayAt, ap.PaidPeriod, ap.NextTryAt, ap.Fails, ap.LastError,
		ap.Snapshot.Encode())
	return err
}

func (b *base) GetAutoPay(ctx context.Context, telegramID int64) (*model.AutoPay, error) {
	var ap model.AutoPay
	var enabled int
	var snapRaw string
	err := b.db.QueryRowContext(ctx,
		"SELECT "+autoPayCols+" FROM autopay WHERE telegram_id = "+b.ph(1), telegramID).
		Scan(&ap.TelegramID, &ap.Method, &ap.MethodID, &ap.Title, &ap.Months, &ap.Amount, &ap.Currency,
			&enabled, &ap.CreatedAt, &ap.LastPayAt, &ap.PaidPeriod, &ap.NextTryAt, &ap.Fails, &ap.LastError, &snapRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ap.Enabled = enabled != 0
	ap.Snapshot = model.DecodePlanSnapshot(snapRaw)
	return &ap, nil
}

func (b *base) SetAutoPayEnabled(ctx context.Context, telegramID int64, on bool) error {
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"UPDATE autopay SET enabled = "+b.ph(1)+", fails = 0, last_error = '', next_try_at = '' WHERE telegram_id = "+b.ph(2),
		boolToInt(on), telegramID)
	return err
}

// UpdateAutoPayResult записывает исход попытки списания.
func (b *base) UpdateAutoPayResult(ctx context.Context, telegramID int64, lastPayAt, nextTryAt string, fails int, lastError string) error {
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"UPDATE autopay SET last_pay_at = "+b.ph(1)+", next_try_at = "+b.ph(2)+", fails = "+b.ph(3)+", last_error = "+b.ph(4)+
			" WHERE telegram_id = "+b.ph(5),
		lastPayAt, nextTryAt, fails, lastError, telegramID)
	return err
}

// MarkAutoPayCharged фиксирует состоявшееся списание: за какой период списали
// (защита от повторной оплаты того же периода), когда и с каким исходом
// продления. Счётчик неудач сбрасывается — деньги-то прошли.
func (b *base) MarkAutoPayCharged(ctx context.Context, telegramID int64, lastPayAt, paidPeriod, nextTryAt, lastError string) error {
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
	_, err := b.db.ExecContext(ctx,
		"UPDATE autopay SET last_pay_at = "+b.ph(1)+", paid_period = "+b.ph(2)+", next_try_at = "+b.ph(3)+
			", fails = 0, last_error = "+b.ph(4)+" WHERE telegram_id = "+b.ph(5),
		lastPayAt, paidPeriod, nextTryAt, lastError, telegramID)
	return err
}

func (b *base) ListAutoPay(ctx context.Context) ([]model.AutoPay, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT "+autoPayCols+" FROM autopay ORDER BY telegram_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AutoPay
	for rows.Next() {
		var ap model.AutoPay
		var enabled int
		var snapRaw string
		if err := rows.Scan(&ap.TelegramID, &ap.Method, &ap.MethodID, &ap.Title, &ap.Months, &ap.Amount, &ap.Currency,
			&enabled, &ap.CreatedAt, &ap.LastPayAt, &ap.PaidPeriod, &ap.NextTryAt, &ap.Fails, &ap.LastError, &snapRaw); err != nil {
			return nil, err
		}
		ap.Enabled = enabled != 0
		ap.Snapshot = model.DecodePlanSnapshot(snapRaw)
		out = append(out, ap)
	}
	return out, rows.Err()
}

func (b *base) DeleteAutoPay(ctx context.Context, telegramID int64) error {
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
	_, err := b.db.ExecContext(ctx, "DELETE FROM autopay WHERE telegram_id = "+b.ph(1), telegramID)
	return err
}

// WhitelistAllUsers выдаёт доступ всем уже зарегистрированным пользователям.
// Нужно при закрытии ранее публичного бота: иначе смена режима мгновенно
// отрезала бы действующих клиентов. Возвращает число затронутых строк.
func (b *base) WhitelistAllUsers(ctx context.Context) (int64, error) {
	res, err := b.db.ExecContext(ctx, "UPDATE users SET whitelisted = 1 WHERE whitelisted = 0")
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}

// CountWhitelisted — сколько пользователей уже имеют доступ (в т.ч. впущенные
// по приглашению).
func (b *base) CountWhitelisted(ctx context.Context) (int, error) {
	var n int
	err := b.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE whitelisted = 1").Scan(&n)
	return n, err
}

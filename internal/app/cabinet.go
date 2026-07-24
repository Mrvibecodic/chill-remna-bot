package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"
	"golang.org/x/crypto/bcrypt"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// This file backs the web cabinet (a browser site, not inside Telegram). It
// reuses the whole Mini App API; only sign-in differs. Telegram sign-in maps to
// the real Telegram id; email+password accounts get a synthetic NEGATIVE id so
// they slot into the bot's telegram-id-keyed system without colliding with real
// (positive) Telegram ids. The trial is never available via the cabinet.

func (a *App) cabinetCfg() model.CabinetConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.CabinetConfig{}
	}
	return a.botCfg.Cabinet
}

// CabinetEnabled reports the web-cabinet feature flag (read live).
func (a *App) CabinetEnabled() bool { return a.cabinetCfg().Enabled }

// CabinetPath is the URL path the cabinet is served at (e.g. "/cabinet/").
func (a *App) CabinetPath() string {
	p := a.cabinetCfg().Path
	if p == "" {
		return "/cabinet/"
	}
	return p
}

// CabinetTitle/CabinetDescription/CabinetFavicon/CabinetAntiFP expose the
// cabinet's branding + privacy settings to the HTML server.
func (a *App) CabinetTitle() string       { return a.cabinetCfg().Title }
func (a *App) CabinetDescription() string { return a.cabinetCfg().Desc }
func (a *App) CabinetFavicon() string     { return a.cabinetCfg().Favicon }
func (a *App) CabinetAntiFP() bool        { return a.cabinetCfg().AntiFP }

// CabinetBotUsername returns the bot @username (for the Telegram Login Widget).
func (a *App) CabinetBotUsername() string { return a.botUsername(context.Background()) }

// CabinetEnsureUser makes sure a Telegram-signed cabinet user exists locally.
// MiniBlocked reports whether the user is blocked (gates mini-app + cabinet).
func (a *App) MiniBlocked(ctx context.Context, tgID int64) bool {
	if a.store == nil {
		return false
	}
	u, _ := a.store.GetUser(ctx, tgID)
	return u != nil && u.Blocked
}

func (a *App) CabinetEnsureUser(ctx context.Context, tgID int64) {
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, tgID)
	}
}

var (
	cabNotifyMu sync.Mutex
	cabNotified = map[int64]bool{}
)

// cabinetNeedsApproval reports whether this sign-in type needs admin approval.
func (a *App) cabinetNeedsApproval(isEmail bool) bool {
	switch a.cabinetCfg().Approval {
	case model.CabinetApprovalAll:
		return true
	case model.CabinetApprovalTG:
		return !isEmail
	case model.CabinetApprovalEmail:
		return isEmail
	}
	return false
}

// errCabinetDenied is returned on sign-in when the admin explicitly rejected
// the account's access request (persisted in users.web_denied). Unlike the
// pending state, a denied account does NOT re-notify the admin.
var errCabinetDenied = errors.New("доступ отклонён администратором")

// CabinetGate enforces the "approve new web users" policy. It returns an error
// (and notifies the admin once) when the account still needs approval, or a
// permanent "denied" error when the admin already rejected the request.
func (a *App) CabinetGate(ctx context.Context, tgID int64, isEmail bool) error {
	if !a.cabinetNeedsApproval(isEmail) {
		return nil
	}
	if a.store != nil {
		if u, _ := a.store.GetUser(ctx, tgID); u != nil {
			if u.WebApproved {
				return nil
			}
			if u.WebDenied {
				return errCabinetDenied
			}
		}
	}
	cabNotifyMu.Lock()
	first := !cabNotified[tgID]
	cabNotified[tgID] = true
	cabNotifyMu.Unlock()
	if first {
		a.notifyAdminWebRequest(ctx, tgID, isEmail)
	}
	return errors.New("аккаунт ожидает одобрения администратором")
}

func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

var errCabinetOff = errors.New("веб-кабинет выключен")

// errCabinetAuth is the single, uniform error returned for every email
// sign-in/registration failure so the endpoints never reveal whether an
// account exists (anti-enumeration / anti-«пробив»).
var errCabinetAuth = errors.New("неверный email или пароль")

// dummyBcryptHash is a valid bcrypt hash (cost 10 = bcrypt.DefaultCost). When a
// login/register targets a non-existent email we still run a bcrypt comparison
// against it, so the response time does not leak whether the email is known.
const dummyBcryptHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// CabinetEmailRegister creates an email+password account and returns its
// synthetic identity. No email confirmation (by design).
func (a *App) CabinetEmailRegister(ctx context.Context, email, password string) (int64, error) {
	if !a.CabinetEnabled() {
		return 0, errCabinetOff
	}
	email = normEmail(email)
	if !strings.Contains(email, "@") || len(email) < 5 {
		return 0, errors.New("неверный email")
	}
	if len(password) < 8 {
		return 0, errors.New("пароль слишком короткий (мин. 8 символов)")
	}
	if a.store == nil {
		return 0, errCabinetOff
	}
	if u, _ := a.store.GetWebUserByEmail(ctx, email); u != nil {
		// Email already exists: instead of revealing that, treat the attempt as
		// a login — a correct password signs them in, a wrong one returns the
		// same generic error a normal login would.
		if bcrypt.CompareHashAndPassword([]byte(u.PassHash), []byte(password)) != nil {
			return 0, errCabinetAuth
		}
		if err := a.CabinetGate(ctx, u.TgID, true); err != nil {
			return 0, err
		}
		return u.TgID, nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	tgID := -time.Now().UnixNano() // synthetic negative identity
	if err := a.store.CreateWebUser(ctx, &model.WebUser{TgID: tgID, Email: email, PassHash: string(hash)}); err != nil {
		return 0, errCabinetAuth
	}
	_ = a.store.UpsertUser(ctx, tgID)
	if err := a.CabinetGate(ctx, tgID, true); err != nil {
		return 0, err
	}
	return tgID, nil
}

// CabinetEmailLogin verifies email+password and returns the account identity.
func (a *App) CabinetEmailLogin(ctx context.Context, email, password string) (int64, error) {
	if !a.CabinetEnabled() {
		return 0, errCabinetOff
	}
	if a.store == nil {
		return 0, errCabinetOff
	}
	u, _ := a.store.GetWebUserByEmail(ctx, normEmail(email))
	if u == nil {
		// Spend the same work as a real comparison so timing does not reveal
		// that the email is unknown.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(password))
		return 0, errCabinetAuth
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PassHash), []byte(password)) != nil {
		return 0, errCabinetAuth
	}
	if err := a.CabinetGate(ctx, u.TgID, true); err != nil {
		return 0, err
	}
	return u.TgID, nil
}

// CabinetP2PScreenshot accepts a payment screenshot uploaded from the web
// cabinet, marks the request submitted and forwards the image to the admin for
// the usual manual approval.
func (a *App) CabinetP2PScreenshot(ctx context.Context, tgID, reqID int64, filename string, data []byte) error {
	if a.store == nil {
		return errors.New("хранилище недоступно")
	}
	req, err := a.store.GetP2PRequest(ctx, reqID)
	if err != nil || req == nil || req.TelegramID != tgID {
		return errors.New("заявка не найдена")
	}
	if req.Status != model.P2PAwaiting && req.Status != model.P2PSubmitted {
		return errors.New("заявка уже обработана")
	}
	if len(data) == 0 {
		return errors.New("пустой файл")
	}
	req.Screenshot = "web"
	req.Status = model.P2PSubmitted
	if err := a.store.UpdateP2PRequest(ctx, req); err != nil {
		return err
	}
	a.payLog(ctx, model.PayMethodP2P, p2pExt(req.ID), tgID, "screenshot_submitted", "из веб-кабинета, ожидает проверки")
	lang := a.lang(a.cfg.AdminID)
	caption := i18n.T(lang, "admin.payment_caption", a.userLabelByID(ctx, req.TelegramID), req.Months, req.Price+curSuffix(a.curFor(model.PayMethodP2P)), req.ID)
	id := strconv.FormatInt(req.ID, 10)
	a.sendAdminPhotoUpload(ctx, filename, data, caption, [][]models.InlineKeyboardButton{{
		btn(i18n.T(lang, "admin.btn_pay_ok"), "adm:pok:"+id),
		btn(i18n.T(lang, "admin.btn_pay_no"), "adm:pno:"+id),
	}})
	return nil
}

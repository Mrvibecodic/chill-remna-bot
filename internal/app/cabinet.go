package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

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

func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

var errCabinetOff = errors.New("веб-кабинет выключен")

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
		return 0, errors.New("этот email уже зарегистрирован")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	tgID := -time.Now().UnixNano() // synthetic negative identity
	if err := a.store.CreateWebUser(ctx, &model.WebUser{TgID: tgID, Email: email, PassHash: string(hash)}); err != nil {
		return 0, errors.New("этот email уже зарегистрирован")
	}
	_ = a.store.UpsertUser(ctx, tgID)
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
	if u == nil || bcrypt.CompareHashAndPassword([]byte(u.PassHash), []byte(password)) != nil {
		return 0, errors.New("неверный email или пароль")
	}
	return u.TgID, nil
}

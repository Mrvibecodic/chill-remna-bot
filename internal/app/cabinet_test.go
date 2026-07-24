package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

func TestCabinetEmailRegisterLogin(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeCabinet()
	a.botCfg.Cabinet.Enabled = true
	ctx := context.Background()

	id, err := a.CabinetEmailRegister(ctx, "User@Example.com", "secret12")
	if err != nil || id >= 0 {
		t.Fatalf("register: id=%d err=%v (id must be negative synthetic)", id, err)
	}
	if u, _ := fs.GetUser(ctx, id); u == nil {
		t.Fatal("local user not created for web account")
	}
	// Anti-enumeration: re-registering an existing email no longer reveals it
	// exists. With the CORRECT password it logs the user in (same id, no error);
	// with a WRONG password it returns the generic auth error.
	if rid, err := a.CabinetEmailRegister(ctx, "user@example.com", "secret12"); err != nil || rid != id {
		t.Fatalf("re-register w/ correct pass should log in (case-insensitive): rid=%d err=%v", rid, err)
	}
	if _, err := a.CabinetEmailRegister(ctx, "user@example.com", "wrongpass9"); err == nil {
		t.Fatal("re-register with wrong password must be rejected")
	}
	if lid, err := a.CabinetEmailLogin(ctx, "user@example.com", "secret12"); err != nil || lid != id {
		t.Fatalf("login: lid=%d err=%v", lid, err)
	}
	if _, err := a.CabinetEmailLogin(ctx, "user@example.com", "wrong"); err == nil {
		t.Fatal("wrong password must be rejected")
	}
	if _, err := a.CabinetEmailRegister(ctx, "a@b.io", "123"); err == nil {
		t.Fatal("short password must be rejected")
	}
}

func TestCabinetApprovalGate(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeCabinet()
	a.botCfg.Cabinet.Enabled = true
	a.botCfg.Cabinet.Approval = model.CabinetApprovalAll
	ctx := context.Background()

	if _, err := a.CabinetEmailRegister(ctx, "x@y.com", "password1"); err == nil {
		t.Fatal("registration must be gated when approval=all")
	}
	wu, _ := fs.GetWebUserByEmail(ctx, "x@y.com")
	if wu == nil {
		t.Fatal("account should still be created while pending approval")
	}
	if _, err := a.CabinetEmailLogin(ctx, "x@y.com", "password1"); err == nil {
		t.Fatal("login must be gated until approved")
	}
	_ = fs.SetWebApproved(ctx, wu.TgID, true)
	if lid, err := a.CabinetEmailLogin(ctx, "x@y.com", "password1"); err != nil || lid != wu.TgID {
		t.Fatalf("approved login should pass: %d %v", lid, err)
	}
	// email mode does not gate Telegram sign-ins
	a.botCfg.Cabinet.Approval = model.CabinetApprovalEmail
	if err := a.CabinetGate(ctx, 12345, false); err != nil {
		t.Fatalf("tg sign-in must not be gated in email mode: %v", err)
	}
}

// Отказ админа должен быть персистентным: юзер при повторном входе видит
// «доступ отклонён» (а не «ожидает одобрения»), и админу НЕ шлётся новая
// заявка. Одобрение (adm:wok) снимает отказ.
func TestCabinetDeniedGate(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.NormalizeCabinet()
	a.botCfg.Cabinet.Enabled = true
	a.botCfg.Cabinet.Approval = model.CabinetApprovalAll
	ctx := context.Background()

	if _, err := a.CabinetEmailRegister(ctx, "denied@example.com", "password1"); err == nil {
		t.Fatal("регистрация должна быть загейчена при approval=all")
	}
	wu, _ := fs.GetWebUserByEmail(ctx, "denied@example.com")
	if wu == nil {
		t.Fatal("аккаунт должен существовать в ожидании одобрения")
	}
	if n := strings.Count(fm.joined(), "Запрос доступа в веб-кабинет"); n != 1 {
		t.Fatalf("админу должна прийти ровно одна заявка, пришло %d", n)
	}

	// Админ отклоняет заявку.
	a.handleCallback(ctx, cb(100, "adm:wno:"+itoa64(wu.TgID)))
	if u, _ := fs.GetUser(ctx, wu.TgID); u == nil || !u.WebDenied {
		t.Fatal("отказ должен сохраниться (web_denied)")
	}

	// Повторный вход: юзер видит «отклонён», админу НОВАЯ заявка не приходит.
	if _, err := a.CabinetEmailLogin(ctx, "denied@example.com", "password1"); err == nil || !strings.Contains(err.Error(), "отклонён") {
		t.Fatalf("ожидалась ошибка «доступ отклонён администратором», получено: %v", err)
	}
	if n := strings.Count(fm.joined(), "Запрос доступа в веб-кабинет"); n != 1 {
		t.Fatalf("после отказа повторных заявок быть не должно, всего %d", n)
	}

	// Админ передумал: adm:wok на старом сообщении одобряет и снимает отказ.
	a.handleCallback(ctx, cb(100, "adm:wok:"+itoa64(wu.TgID)))
	if lid, err := a.CabinetEmailLogin(ctx, "denied@example.com", "password1"); err != nil || lid != wu.TgID {
		t.Fatalf("после одобрения вход должен пройти: lid=%d err=%v", lid, err)
	}
}

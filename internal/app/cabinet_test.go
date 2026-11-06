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

	id, err := a.CabinetEmailRegister(ctx, "User@Example.com", "secret12", "")
	if err != nil || id >= 0 {
		t.Fatalf("register: id=%d err=%v (id must be negative synthetic)", id, err)
	}
	if u, _ := fs.GetUser(ctx, id); u == nil {
		t.Fatal("local user not created for web account")
	}
	// Anti-enumeration: re-registering an existing email no longer reveals it
	// exists. With the CORRECT password it logs the user in (same id, no error);
	// with a WRONG password it returns the generic auth error.
	if rid, err := a.CabinetEmailRegister(ctx, "user@example.com", "secret12", ""); err != nil || rid != id {
		t.Fatalf("re-register w/ correct pass should log in (case-insensitive): rid=%d err=%v", rid, err)
	}
	if _, err := a.CabinetEmailRegister(ctx, "user@example.com", "wrongpass9", ""); err == nil {
		t.Fatal("re-register with wrong password must be rejected")
	}
	if lid, err := a.CabinetEmailLogin(ctx, "user@example.com", "secret12"); err != nil || lid != id {
		t.Fatalf("login: lid=%d err=%v", lid, err)
	}
	if _, err := a.CabinetEmailLogin(ctx, "user@example.com", "wrong"); err == nil {
		t.Fatal("wrong password must be rejected")
	}
	if _, err := a.CabinetEmailRegister(ctx, "a@b.io", "123", ""); err == nil {
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

	if _, err := a.CabinetEmailRegister(ctx, "x@example.com", "password1", ""); err == nil {
		t.Fatal("registration must be gated when approval=all")
	}
	wu, _ := fs.GetWebUserByEmail(ctx, "x@example.com")
	if wu == nil {
		t.Fatal("account should still be created while pending approval")
	}
	if _, err := a.CabinetEmailLogin(ctx, "x@example.com", "password1"); err == nil {
		t.Fatal("login must be gated until approved")
	}
	_ = fs.SetWebApproved(ctx, wu.TgID, true)
	if lid, err := a.CabinetEmailLogin(ctx, "x@example.com", "password1"); err != nil || lid != wu.TgID {
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

	if _, err := a.CabinetEmailRegister(ctx, "denied@example.com", "password1", ""); err == nil {
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

// Из кабинета чек тоже приходит и PDF-файлом: картинку уводим админу фото,
// PDF — документом (sendPhoto его не примет), посторонний файл отклоняем.
func TestCabinetP2PScreenshot_PDFAndImage(t *testing.T) {
	pdf := append([]byte("%PDF-1.7\n"), make([]byte, 64)...)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

	cases := []struct {
		name, file string
		data       []byte
		wantDoc    bool
		wantErr    bool
	}{
		{name: "pdf", file: "check.pdf", data: pdf, wantDoc: true},
		{name: "картинка", file: "shot.png", data: png},
		{name: "имя без расширения", file: "blob", data: pdf, wantDoc: true},
		{name: "посторонний файл", file: "report.xlsx", data: []byte("PK\x03\x04 zip"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, fm, fs := newTestApp(t)
			a.store = fs
			a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
			ctx := context.Background()
			const user int64 = 555
			req := &model.P2PRequest{TelegramID: user, Months: 1, Price: "100", Status: model.P2PAwaiting}
			if err := fs.CreateP2PRequest(ctx, req); err != nil {
				t.Fatal(err)
			}
			err := a.CabinetP2PScreenshot(ctx, user, req.ID, tc.file, tc.data)
			if tc.wantErr {
				if err == nil {
					t.Fatal("посторонний файл должен быть отклонён")
				}
				if r, _ := fs.GetP2PRequest(ctx, req.ID); r == nil || r.Status != model.P2PAwaiting {
					t.Fatalf("заявка не должна была уйти на проверку: %+v", r)
				}
				return
			}
			if err != nil {
				t.Fatalf("чек не принят: %v", err)
			}
			if r, _ := fs.GetP2PRequest(ctx, req.ID); r == nil || r.Status != model.P2PSubmitted {
				t.Fatalf("заявка не помечена отправленной: %+v", r)
			}
			gotDoc := len(fm.sentDocIDs) == 1 && fm.sentDocIDs[0] == tc.file
			if gotDoc != tc.wantDoc {
				t.Fatalf("способ отправки админу неверный: документы=%v\n%s", fm.sentDocIDs, fm.joined())
			}
		})
	}
}

// Удалённый из бота аккаунт по почте не входит в кабинет ни заново, ни по
// выданному раньше пропуску.
func TestCabinetDeletedUserCannotSignIn(t *testing.T) {
	for _, mode := range []string{"usr:delbot:", "usr:delfull:"} {
		t.Run(mode, func(t *testing.T) {
			a, _, fs := newTestApp(t)
			a.store = fs
			a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
			a.botCfg.NormalizeCabinet()
			a.botCfg.Cabinet.Enabled = true
			ctx := context.Background()

			id, err := a.CabinetEmailRegister(ctx, "gone@example.com", "password1", "")
			if err != nil {
				t.Fatalf("регистрация: %v", err)
			}
			if e := a.SessionEpoch(ctx, id); e != 0 {
				t.Fatalf("поколение живого аккаунта %d, want 0", e)
			}

			a.handleCallback(ctx, cb(100, mode+itoa64(id)))

			if _, err := a.CabinetEmailLogin(ctx, "gone@example.com", "password1"); err == nil {
				t.Fatal("после удаления вход по почте должен быть закрыт")
			}
			if e := a.SessionEpoch(ctx, id); e >= 0 {
				t.Fatalf("старый пропуск удалённого аккаунта не должен приниматься: поколение %d", e)
			}
			// Тот же адрес можно зарегистрировать заново — это новый аккаунт.
			nid, err := a.CabinetEmailRegister(ctx, "gone@example.com", "password2", "")
			if err != nil || nid == id {
				t.Fatalf("повторная регистрация: id=%d (старый %d) err=%v", nid, id, err)
			}
		})
	}
}

// Удалили человека с неотвеченной заявкой в кабинет — при новом входе админу
// приходит новая заявка, а не тишина до перезапуска бота.
func TestCabinetDeletedUserRequestsAgain(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.NormalizeCabinet()
	a.botCfg.Cabinet.Enabled = true
	a.botCfg.Cabinet.Approval = model.CabinetApprovalTG
	ctx := context.Background()
	const user = int64(1000000731)
	// Отметка о заявке живёт в памяти процесса — чистим её до и после теста.
	forget := func() {
		cabNotifyMu.Lock()
		delete(cabNotified, user)
		cabNotifyMu.Unlock()
	}
	forget()
	t.Cleanup(forget)

	_ = fs.UpsertUser(ctx, user)
	if err := a.CabinetGate(ctx, user, false); err == nil {
		t.Fatal("вход через Telegram должен ждать одобрения")
	}
	a.handleCallback(ctx, cb(100, "usr:delbot:"+itoa64(user)))
	_ = fs.UpsertUser(ctx, user)
	if err := a.CabinetGate(ctx, user, false); err == nil {
		t.Fatal("после удаления вход снова должен ждать одобрения")
	}
	if n := strings.Count(fm.joined(), "Запрос доступа в веб-кабинет"); n != 2 {
		t.Fatalf("после удаления админу должна прийти новая заявка, всего заявок %d", n)
	}
}

// Удаление посреди чтения поколения из базы: устаревшее значение не должно
// остаться в памяти, иначе старый пропуск удалённого аккаунта жил бы до
// перезапуска бота.
func TestSessionEpochStaleReadNotCached(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.NormalizeCabinet()
	a.botCfg.Cabinet.Enabled = true
	ctx := context.Background()

	id, err := a.CabinetEmailRegister(ctx, "race@example.com", "password1", "")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.forgetEpoch(id)
	fs.afterWebUserRead = func() {
		fs.afterWebUserRead = nil
		a.handleCallback(ctx, cb(100, "usr:delbot:"+itoa64(id)))
	}
	_ = a.SessionEpoch(ctx, id) // застало аккаунт живым, удаление прошло посреди
	if e := a.SessionEpoch(ctx, id); e != -1 {
		t.Fatalf("после удаления поколение %d, want -1", e)
	}
}

// Одобрение новых в кабинете — два независимых тумблера; кнопка из старых
// сообщений режим не меняет.
func TestCabinetApprovalToggles(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.NormalizeCabinet()
	a.botCfg.Cabinet.Enabled = true
	ctx := context.Background()
	mode := func() string {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.botCfg.Cabinet.Approval
	}

	steps := []struct{ tap, want, screen string }{
		{"menu:cabappem", model.CabinetApprovalEmail, "Telegram — <b>нет</b>, почта — <b>да</b>"},
		{"menu:cabapptg", model.CabinetApprovalAll, "Telegram — <b>да</b>, почта — <b>да</b>"},
		{"menu:cabappem", model.CabinetApprovalTG, "Telegram — <b>да</b>, почта — <b>нет</b>"},
		{"menu:cabapprove", model.CabinetApprovalTG, "Telegram — <b>да</b>, почта — <b>нет</b>"},
		{"menu:cabapptg", model.CabinetApprovalOff, "Telegram — <b>нет</b>, почта — <b>нет</b>"},
	}
	for _, s := range steps {
		fm.texts = nil
		a.handleCallback(ctx, cb(100, s.tap))
		if got := mode(); got != s.want {
			t.Fatalf("%s: режим %q, want %q", s.tap, got, s.want)
		}
		if !strings.Contains(fm.joined(), s.screen) {
			t.Fatalf("%s: на экране нет %q:\n%s", s.tap, s.screen, fm.joined())
		}
	}
	// Не админ режим не меняет.
	a.handleCallback(ctx, cb(1000000001, "menu:cabappem"))
	if got := mode(); got != model.CabinetApprovalOff {
		t.Fatalf("не админ поменял режим: %q", got)
	}
	// В закрытом боте почту одобряет админ всегда — экран это говорит.
	a.mu.Lock()
	a.botCfg.AccessMode = model.AccessWhitelist
	a.mu.Unlock()
	fm.texts = nil
	a.handleCallback(ctx, cb(100, "menu:cabinet"))
	if !strings.Contains(fm.joined(), "почта — <b>да (бот закрыт)</b>") {
		t.Fatalf("закрытый бот: %s", fm.joined())
	}
}

package app

import (
	"context"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
)

const rsTestDump = `COPY public.users (id, telegram_id, username, name, points, is_blocked, is_trial_available, created_at) FROM stdin;
1	111	alice	Alice	50	f	f	2026-01-01 10:00:00+00
2	222	bob	Bob	0	f	t	2026-02-01 10:00:00+00
\.

COPY public.subscriptions (id, user_id, status, is_trial, expire_at) FROM stdin;
1	1	ACTIVE	f	2026-09-01 00:00:00+00
\.

COPY public.referrals (id, referrer_id, referred_id, level) FROM stdin;
1	1	2	FIRST
\.

COPY public.promocodes (id, code, is_active, reward_type, reward, plan_snapshot, expires_at, max_activations, is_reusable, created_at) FROM stdin;
1	WELCOME	t	DURATION	7	\N	\N	100	t	2026-01-01 00:00:00+00
\.

COPY public.promocode_activations (id, promocode_id, user_id, activated_at) FROM stdin;
1	1	1	2026-02-02 00:00:00+00
\.

COPY public.transactions (id, payment_id, user_id, status, is_test, purchase_type, gateway_type, pricing, currency, plan_snapshot, created_at) FROM stdin;
1	9f1c1b6e-0000-4000-8000-000000000001	1	COMPLETED	f	NEW	YOOKASSA	{"final_amount": "199.00"}	RUB	{"duration": 30}	2026-01-05 12:00:00+00
\.
`

func rsTestApp(t *testing.T) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.runInline = true
	fm.downloads = map[string][]byte{"dump-1": []byte(rsTestDump)}
	return a, fm, fs
}

// docMsg — сообщение с приложенным документом от админа.
func docMsg(uid int64, fileID string, size int64) *models.Message {
	return &models.Message{
		From:     &models.User{ID: uid},
		Chat:     models.Chat{ID: uid},
		Document: &models.Document{FileID: fileID, FileName: "db_backup.sql", FileSize: size},
	}
}

func TestRSImport_WizardEndToEnd(t *testing.T) {
	a, fm, fs := rsTestApp(t)
	ctx := context.Background()

	a.onRSImport(ctx, 100, "up")
	if !a.getUI(100).awaitRSDump {
		t.Fatal("после «Загрузить дамп» бот должен ждать файл")
	}

	a.handleDocument(ctx, docMsg(100, "dump-1", int64(len(rsTestDump))))
	if a.rsDumpPeek(100) == nil {
		t.Fatal("дамп не разобран")
	}
	if !strings.Contains(strings.Join(fm.texts, "\n"), "Что нашлось в дампе") {
		t.Fatalf("предпросмотр не показан: %v", fm.texts)
	}

	a.onRSImport(ctx, 100, "apply")

	alice, _ := fs.GetUser(ctx, 111)
	if alice == nil {
		t.Fatal("пользователь не создан")
	}
	if alice.SubExpireAt != "2026-09-01T00:00:00Z" {
		t.Fatalf("срок подписки: %q", alice.SubExpireAt)
	}
	if alice.TrialUsedAt == "" {
		t.Fatal("использованный триал не отмечен")
	}
	if alice.Balance != 5000 || alice.RefEarned != 5000 {
		t.Fatalf("баллы → баланс: balance=%d earned=%d", alice.Balance, alice.RefEarned)
	}
	bob, _ := fs.GetUser(ctx, 222)
	if bob == nil || bob.ReferredBy != 111 || !bob.RefBonusPaid {
		t.Fatalf("реферальная связь перенесена неверно: %+v", bob)
	}
	if p, _ := fs.GetPromo(ctx, "WELCOME"); p == nil || p.Kind != model.PromoKindDays || p.Value != 7 {
		t.Fatalf("промокод: %+v", p)
	}
	if used, _ := fs.PromoRedeemedBy(ctx, "WELCOME", 111); !used {
		t.Fatal("активация промокода не перенесена")
	}
	if seen, _ := fs.PaymentByExtID(ctx, "rs:9f1c1b6e-0000-4000-8000-000000000001"); !seen {
		t.Fatal("платёж не попал в историю")
	}
	if a.rsDumpPeek(100) != nil {
		t.Fatal("после импорта разобранные данные должны очищаться")
	}
}

func TestRSImport_SecondRunChangesNothing(t *testing.T) {
	a, fm, fs := rsTestApp(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		a.onRSImport(ctx, 100, "up")
		fm.downloads["dump-1"] = []byte(rsTestDump)
		a.handleDocument(ctx, docMsg(100, "dump-1", int64(len(rsTestDump))))
		a.onRSImport(ctx, 100, "apply")
	}

	alice, _ := fs.GetUser(ctx, 111)
	if alice.Balance != 5000 {
		t.Fatalf("повторный импорт начислил деньги второй раз: %d", alice.Balance)
	}
	if alice.RefEarned != 5000 {
		t.Fatalf("повторный импорт удвоил «заработано»: %d", alice.RefEarned)
	}
	if n := len(fs.pays); n != 1 {
		t.Fatalf("платёж задвоился: %d записей", n)
	}
	if p, _ := fs.GetPromo(ctx, "WELCOME"); p.Used != 1 {
		t.Fatalf("активация промокода задвоилась: used=%d", p.Used)
	}
}

// Двойное нажатие «Импортировать» (или нажатие на уже отработавшем экране)
// не должно применять дамп второй раз.
func TestRSImport_DoubleApplyDoesNothing(t *testing.T) {
	a, _, fs := rsTestApp(t)
	ctx := context.Background()

	a.getUI(100).awaitRSDump = true
	a.handleDocument(ctx, docMsg(100, "dump-1", int64(len(rsTestDump))))
	a.onRSImport(ctx, 100, "apply")
	a.onRSImport(ctx, 100, "apply")

	if n := len(fs.pays); n != 1 {
		t.Fatalf("платёж задвоился: %d записей", n)
	}
	alice, _ := fs.GetUser(ctx, 111)
	if alice.Balance != 5000 {
		t.Fatalf("баланс начислен дважды: %d", alice.Balance)
	}
}

func TestRSImport_KeepsNewerLocalData(t *testing.T) {
	a, fm, fs := rsTestApp(t)
	ctx := context.Background()

	// Пользователь уже пришёл в наш бот и купил подписку подольше.
	_ = fs.UpsertUser(ctx, 111)
	_ = fs.SetSubExpiry(ctx, 111, "2027-01-01T00:00:00Z", "paid")
	_ = fs.AddBalance(ctx, 111, 100)

	a.onRSImport(ctx, 100, "up")
	fm.downloads["dump-1"] = []byte(rsTestDump)
	a.handleDocument(ctx, docMsg(100, "dump-1", int64(len(rsTestDump))))
	a.onRSImport(ctx, 100, "apply")

	u, _ := fs.GetUser(ctx, 111)
	if u.SubExpireAt != "2027-01-01T00:00:00Z" {
		t.Fatalf("импорт укоротил подписку: %q", u.SubExpireAt)
	}
	if u.Balance != 100 {
		t.Fatalf("существующему пользователю деньги начислять нельзя: %d", u.Balance)
	}
}

// Импорт не должен ничего менять у тех, кого в дампе нет: у бота могут быть
// свои пользователи, не имеющие к remnashop никакого отношения.
func TestRSImport_LeavesUnrelatedUsersAlone(t *testing.T) {
	a, _, fs := rsTestApp(t)
	ctx := context.Background()

	_ = fs.UpsertUser(ctx, 555)
	_ = fs.SetUserInfo(ctx, 555, "local", "Local")
	_ = fs.SetSubExpiry(ctx, 555, "2026-12-01T00:00:00Z", "paid")
	_ = fs.AddBalance(ctx, 555, 777)
	before, _ := fs.GetUser(ctx, 555)

	a.getUI(100).awaitRSDump = true
	a.handleDocument(ctx, docMsg(100, "dump-1", int64(len(rsTestDump))))
	a.onRSImport(ctx, 100, "apply")

	after, _ := fs.GetUser(ctx, 555)
	if *after != *before {
		t.Fatalf("посторонний пользователь изменился:\nбыло:  %+v\nстало: %+v", before, after)
	}
}

// Блокировка из remnashop не должна отбирать доступ у того, кто уже пользуется
// нашим ботом: заблокированным импорт заводит только новых.
func TestRSImport_DoesNotBlockExistingUser(t *testing.T) {
	a, fm, fs := rsTestApp(t)
	ctx := context.Background()

	blockedDump := strings.Replace(rsTestDump,
		"1\t111\talice\tAlice\t50\tf\tf\t",
		"1\t111\talice\tAlice\t50\tt\tf\t", 1)
	fm.downloads["dump-blocked"] = []byte(blockedDump)

	_ = fs.UpsertUser(ctx, 111) // уже наш активный пользователь

	a.getUI(100).awaitRSDump = true
	a.handleDocument(ctx, docMsg(100, "dump-blocked", int64(len(blockedDump))))
	a.onRSImport(ctx, 100, "apply")

	u, _ := fs.GetUser(ctx, 111)
	if u.Blocked {
		t.Fatal("импорт заблокировал существующего пользователя бота")
	}

	// А вот заведённого импортом — блокирует.
	a2, fm2, fs2 := rsTestApp(t)
	fm2.downloads["dump-blocked"] = []byte(blockedDump)
	a2.getUI(100).awaitRSDump = true
	a2.handleDocument(ctx, docMsg(100, "dump-blocked", int64(len(blockedDump))))
	a2.onRSImport(ctx, 100, "apply")
	if u2, _ := fs2.GetUser(ctx, 111); u2 == nil || !u2.Blocked {
		t.Fatalf("новому пользователю блокировка должна перенестись: %+v", u2)
	}
}

func TestRSImport_IgnoresDocumentsFromNonAdmin(t *testing.T) {
	a, fm, fs := rsTestApp(t)
	ctx := context.Background()
	a.getUI(999).awaitRSDump = true
	fm.downloads["dump-1"] = []byte(rsTestDump)

	a.handleDocument(ctx, docMsg(999, "dump-1", int64(len(rsTestDump))))

	if a.rsDumpPeek(999) != nil {
		t.Fatal("не-админ не должен запускать импорт")
	}
	if u, _ := fs.GetUser(ctx, 111); u != nil {
		t.Fatal("данные не должны быть импортированы")
	}
}

func TestRSImport_TooBigFileRejected(t *testing.T) {
	a, _, _ := rsTestApp(t)
	ctx := context.Background()
	a.getUI(100).awaitRSDump = true

	a.handleDocument(ctx, docMsg(100, "dump-1", maxDumpBytes+1))

	if a.rsDumpPeek(100) != nil {
		t.Fatal("слишком большой файл не должен разбираться")
	}
}

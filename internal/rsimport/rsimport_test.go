package rsimport

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// sampleDump повторяет форму настоящего pg_dump remnashop: заголовки COPY,
// табы между значениями, \N вместо NULL, escape-последовательности.
const sampleDump = `--
-- PostgreSQL database dump
--

SET statement_timeout = 0;

COPY public.users (id, telegram_id, username, name, points, is_blocked, is_trial_available, created_at) FROM stdin;
1	111	alice	Alice\tA	50	f	f	2026-01-01 10:00:00+00
2	222	\N	Bob	0	t	t	2026-02-01 10:00:00+00
3	\N	\N	Web User	10	f	t	2026-03-01 10:00:00+00
\.

COPY public.settings (id, menu) FROM stdin;
1	{}
\.

COPY public.subscriptions (id, user_id, status, is_trial, expire_at) FROM stdin;
1	1	ACTIVE	f	2026-09-01 00:00:00+00
2	1	EXPIRED	t	2026-02-01 00:00:00+00
3	2	DELETED	f	2027-01-01 00:00:00+00
\.

COPY public.referrals (id, referrer_id, referred_id, level) FROM stdin;
1	1	2	FIRST
\.

COPY public.referral_rewards (id, referral_id, user_id, type, amount, is_issued) FROM stdin;
1	1	1	POINTS	50	t
2	1	1	EXTRA_DAYS	7	t
3	1	1	POINTS	20	f
\.

COPY public.promocodes (id, code, is_active, reward_type, reward, plan_snapshot, expires_at, max_activations, is_reusable, created_at) FROM stdin;
1	WELCOME	t	DURATION	7	\N	2026-12-01 00:00:00+00	100	t	2026-01-01 00:00:00+00
2	TRAFFIC50	t	TRAFFIC	50	\N	\N	\N	t	2026-01-01 00:00:00+00
3	PLAN30	t	SUBSCRIPTION	\N	{"duration": 30, "name": "Base"}	\N	\N	t	2026-01-01 00:00:00+00
\.

COPY public.promocode_activations (id, promocode_id, user_id, activated_at) FROM stdin;
1	1	1	2026-02-02 00:00:00+00
2	1	3	2026-02-03 00:00:00+00
\.

COPY public.transactions (id, payment_id, user_id, status, is_test, purchase_type, gateway_type, pricing, currency, plan_snapshot, created_at) FROM stdin;
1	9f1c1b6e-0000-4000-8000-000000000001	1	COMPLETED	f	NEW	YOOKASSA	{"original_amount": "249.00", "discount_percent": 20, "final_amount": "199.00"}	RUB	{"duration": 30}	2026-01-05 12:00:00+00
2	9f1c1b6e-0000-4000-8000-000000000002	2	PENDING	f	NEW	YOOKASSA	{"original_amount": "199.00", "discount_percent": 0, "final_amount": "199.00"}	RUB	{"duration": 30}	2026-01-06 12:00:00+00
3	9f1c1b6e-0000-4000-8000-000000000003	1	COMPLETED	t	NEW	TELEGRAM_STARS	{"original_amount": "60", "discount_percent": 0, "final_amount": "60"}	XTR	{"duration": 30}	2026-01-07 12:00:00+00
\.

--
-- PostgreSQL database dump complete
--
`

func loadSample(t *testing.T) *Data {
	t.Helper()
	d, err := Load(strings.NewReader(sampleDump))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return d
}

func userByID(d *Data, tgID int64) *User {
	for i := range d.Users {
		if d.Users[i].TelegramID == tgID {
			return &d.Users[i]
		}
	}
	return nil
}

func TestParseDumpReadsOnlyWantedTables(t *testing.T) {
	tables, err := ParseDump(strings.NewReader(sampleDump), Tables...)
	if err != nil {
		t.Fatalf("ParseDump: %v", err)
	}
	if _, ok := tables["settings"]; ok {
		t.Fatal("таблица settings не запрашивалась, но попала в результат")
	}
	users := tables["users"]
	if users == nil || len(users.Rows) != 3 {
		t.Fatalf("ожидалось 3 строки users, got %+v", users)
	}
	if users.Col("telegram_id") != 1 {
		t.Fatalf("колонки разобраны неверно: %v", users.Cols)
	}
	// \N → NULL, а не строка "\N".
	if users.Rows[2][1] != nil {
		t.Fatal("telegram_id=\\N должен стать NULL")
	}
	// \t внутри значения — настоящий таб, а не разделитель.
	if got := *users.Rows[0][3]; got != "Alice\tA" {
		t.Fatalf("escape не раскрыт: %q", got)
	}
}

func TestParseDumpGzip(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(sampleDump)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := Load(&buf)
	if err != nil {
		t.Fatalf("Load(gzip): %v", err)
	}
	if len(d.Users) != 2 {
		t.Fatalf("из gzip-дампа ожидалось 2 пользователя, got %d", len(d.Users))
	}
}

func TestParseDumpInserts(t *testing.T) {
	const dump = `INSERT INTO public.users (id, telegram_id, username, name, points, is_blocked, is_trial_available, created_at) VALUES (1, 111, 'ali''ce', 'A, B', 5, false, true, '2026-01-01 10:00:00+00');
INSERT INTO public.users (id, telegram_id, username, name, points, is_blocked, is_trial_available, created_at) VALUES (2, NULL, NULL, 'Web', 0, false, true, '2026-01-01 10:00:00+00');
`
	d, err := Load(strings.NewReader(dump))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(d.Users) != 1 || d.SkippedWeb != 1 {
		t.Fatalf("ожидался 1 перенос и 1 пропуск, got %+v", d)
	}
	u := d.Users[0]
	if u.Username != "ali'ce" {
		t.Fatalf("кавычка внутри строки разобрана неверно: %q", u.Username)
	}
	if u.FirstName != "A, B" {
		t.Fatalf("запятая внутри строки разбила значение: %q", u.FirstName)
	}
	if u.BalanceKopecks != 500 {
		t.Fatalf("баллы → баланс: ожидалось 500 копеек, got %d", u.BalanceKopecks)
	}
}

func TestParseDumpRejectsForeignFile(t *testing.T) {
	if _, err := Load(strings.NewReader("just some text\n")); err == nil {
		t.Fatal("посторонний файл должен отвергаться")
	}
}

func TestBuildUsers(t *testing.T) {
	d := loadSample(t)

	if d.TotalUsers != 3 || len(d.Users) != 2 || d.SkippedWeb != 1 {
		t.Fatalf("счётчики пользователей: %+v", d)
	}

	alice := userByID(d, 111)
	if alice == nil {
		t.Fatal("пользователь 111 потерялся")
	}
	if alice.Username != "alice" || alice.FirstName != "Alice\tA" {
		t.Fatalf("профиль разобран неверно: %+v", alice)
	}
	// Самая поздняя подписка, DELETED игнорируется.
	if alice.SubExpireAt != "2026-09-01T00:00:00Z" {
		t.Fatalf("срок подписки: %q", alice.SubExpireAt)
	}
	if !alice.TrialUsed {
		t.Fatal("is_trial_available=f должен означать «триал использован»")
	}
	if alice.BalanceKopecks != 5000 || alice.RefEarnedKopecks != 5000 {
		t.Fatalf("баллы → деньги: %+v", alice)
	}

	bob := userByID(d, 222)
	if bob == nil {
		t.Fatal("пользователь 222 потерялся")
	}
	if !bob.Blocked {
		t.Fatal("блокировка не перенеслась")
	}
	if bob.SubExpireAt != "" {
		t.Fatalf("подписка со статусом DELETED не должна переноситься: %q", bob.SubExpireAt)
	}
	if bob.TrialUsed {
		t.Fatal("у Боба триал ещё доступен")
	}
	if bob.ReferredBy != 111 {
		t.Fatalf("реферальная связь: %d", bob.ReferredBy)
	}
	if d.Referrals != 1 {
		t.Fatalf("счётчик рефералов: %d", d.Referrals)
	}
}

func TestBuildPromos(t *testing.T) {
	d := loadSample(t)

	if len(d.Promos) != 2 {
		t.Fatalf("ожидались 2 переносимых промокода, got %d: %+v", len(d.Promos), d.Promos)
	}
	byCode := map[string]Promo{}
	for _, p := range d.Promos {
		byCode[p.Code] = p
	}
	if p := byCode["WELCOME"]; p.Kind != "days" || p.Value != 7 || p.MaxUses != 100 {
		t.Fatalf("DURATION → дни: %+v", p)
	}
	if p := byCode["PLAN30"]; p.Kind != "days" || p.Value != 30 {
		t.Fatalf("SUBSCRIPTION → дни из снимка плана: %+v", p)
	}
	if len(d.Warnings) == 0 {
		t.Fatal("про пропущенный TRAFFIC-промокод должно быть предупреждение")
	}
	// Активация веб-пользователя (без telegram_id) не переносится.
	if len(d.PromoUses) != 1 || d.PromoUses[0].TelegramID != 111 {
		t.Fatalf("активации промокодов: %+v", d.PromoUses)
	}
}

// legacyDump — схема remnashop до v0.8.0: дочерние таблицы ссылались на
// пользователя через *_telegram_id. Такой дамп мы не разбираем.
const legacyDump = `COPY public.users (id, telegram_id, username, name, is_blocked, created_at) FROM stdin;
1	111	alice	Alice	f	2026-01-01 10:00:00+00
\.

COPY public.subscriptions (id, user_telegram_id, status, expire_at) FROM stdin;
1	111	ACTIVE	2026-09-01 00:00:00+00
\.
`

func TestLoadRejectsPre080Schema(t *testing.T) {
	_, err := Load(strings.NewReader(legacyDump))
	if err == nil {
		t.Fatal("дамп старой схемы должен отвергаться, а не разбираться наполовину")
	}
	msg := err.Error()
	for _, want := range []string{"v0.8.0", "user_telegram_id", "бэкап"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("в ошибке нет %q: %s", want, msg)
		}
	}
}

func TestBuildPayments(t *testing.T) {
	d := loadSample(t)

	// PENDING и is_test пропускаются.
	if len(d.Payments) != 1 {
		t.Fatalf("ожидался 1 платёж, got %+v", d.Payments)
	}
	p := d.Payments[0]
	if p.TelegramID != 111 || p.Method != "yookassa" || p.Months != 1 {
		t.Fatalf("платёж разобран неверно: %+v", p)
	}
	if p.Amount != "199.00 ₽" {
		t.Fatalf("сумма берётся из final_amount: %q", p.Amount)
	}
	if p.ExtID != "rs:9f1c1b6e-0000-4000-8000-000000000001" {
		t.Fatalf("ext_id должен быть помечен источником: %q", p.ExtID)
	}
}

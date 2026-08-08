package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"remnabot/internal/crypto"
	"remnabot/internal/model"

	_ "remnabot/internal/storage/drivers"
	"time"
)

func testCrypter(t *testing.T) *crypto.Crypter {
	t.Helper()
	c, err := crypto.NewFromKeyMaterial([]byte("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func openSQLiteTest(t *testing.T) Storage {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(model.DBSQLite, path, testCrypter(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func sampleConfig() *model.BotConfig {
	return &model.BotConfig{
		Installed: true, Language: "ru", DBKind: "sqlite",
		Panel: model.PanelConfig{
			Mode: "remote", InstallType: "egames", BaseURL: "https://p",
			APIToken: "secret-token", Cookie: "AbCdEfGh=IjKlMnOp",
		},
	}
}

func TestSQLiteContract(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t)

	if _, ok, err := st.LoadConfig(ctx); err != nil || ok {
		t.Fatalf("на пустой БД: ok=%v err=%v", ok, err)
	}
	want := sampleConfig()
	if err := st.SaveConfig(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.LoadConfig(ctx)
	if err != nil || !ok {
		t.Fatalf("load после save: ok=%v err=%v", ok, err)
	}
	if got.Panel.APIToken != want.Panel.APIToken || got.Language != want.Language || got.Panel.Cookie != want.Panel.Cookie {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	want.Language = "en"
	if err := st.SaveConfig(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, _, _ = st.LoadConfig(ctx)
	if got.Language != "en" {
		t.Fatalf("upsert не сработал: %q", got.Language)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	st := openSQLiteTest(t)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTransferSQLiteToSQLite(t *testing.T) {
	ctx := context.Background()
	src := openSQLiteTest(t)
	dst := openSQLiteTest(t)

	cfg := sampleConfig()
	if err := src.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := src.UpsertUser(ctx, 777); err != nil {
		t.Fatal(err)
	}
	if err := src.SetUserInfo(ctx, 777, "vasya", "Вася"); err != nil {
		t.Fatal(err)
	}
	if err := src.SetBlocked(ctx, 777, true); err != nil {
		t.Fatal(err)
	}
	if err := src.SetTermsAccepted(ctx, 777, "2026-05-28T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := src.AddPayment(ctx, &model.Payment{TelegramID: 777, Method: model.PayMethodYooKassa, Months: 3, Amount: "450", Status: model.PaymentPaid, ExtID: "yk_1"}); err != nil {
		t.Fatal(err)
	}
	pr := &model.P2PRequest{TelegramID: 777, Months: 1, Price: "150", Status: model.P2PApproved}
	if err := src.CreateP2PRequest(ctx, pr); err != nil {
		t.Fatal(err)
	}
	if err := src.SaveMediaFileID(ctx, "main_menu", "file_abc"); err != nil {
		t.Fatal(err)
	}

	if err := Transfer(ctx, src, dst); err != nil {
		t.Fatal(err)
	}

	got, ok, err := dst.LoadConfig(ctx)
	if err != nil || !ok || got.Panel.APIToken != cfg.Panel.APIToken {
		t.Fatalf("config не перенёсся: ok=%v err=%v", ok, err)
	}

	u, err := dst.GetUser(ctx, 777)
	if err != nil || u == nil {
		t.Fatalf("user не перенёсся: %v", err)
	}
	if u.Username != "vasya" || u.FirstName != "Вася" || !u.Blocked || u.TermsAcceptedAt == "" {
		t.Fatalf("поля юзера потеряны: %+v", u)
	}

	if ok, _ := dst.HasPaidPayment(ctx, 777); !ok {
		t.Fatal("платёж не перенёсся")
	}
	if dup, _ := dst.PaymentByExtID(ctx, "yk_1"); !dup {
		t.Fatal("ext_id платежа не перенёсся")
	}

	if r, err := dst.GetP2PRequest(ctx, pr.ID); err != nil || r == nil || r.Status != model.P2PApproved {
		t.Fatalf("p2p-заявка не перенёслась: %+v err=%v", r, err)
	}

	if id, ok, _ := dst.LoadMediaFileID(ctx, "main_menu"); !ok || id != "file_abc" {
		t.Fatalf("media_cache не перенёсся: id=%q ok=%v", id, ok)
	}

	if err := Transfer(ctx, src, dst); err != nil {
		t.Fatalf("повторный Transfer упал: %v", err)
	}
}

func TestPostgresContract(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN не задан")
	}
	ctx := context.Background()
	st, err := Open(model.DBPostgres, dsn, testCrypter(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := sampleConfig()
	if err := st.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.LoadConfig(ctx)
	if err != nil || !ok || got.Panel.APIToken != cfg.Panel.APIToken {
		t.Fatalf("PG roundtrip провален: ok=%v err=%v", ok, err)
	}
}

func eachStore(t *testing.T, fn func(t *testing.T, st Storage)) {
	t.Run("sqlite", func(t *testing.T) { fn(t, openSQLiteTest(t)) })
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		t.Run("postgres", func(t *testing.T) {
			st, err := Open(model.DBPostgres, dsn, testCrypter(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.Migrate(context.Background()); err != nil {
				t.Fatal(err)
			}

			cleanPGData(t, dsn)
			fn(t, st)
		})
	}
}

func cleanPGData(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// payment_log добавлен: без него прогоны на общей БД накапливали записи и
	// тест журнала видел данные предыдущего запуска.
	for _, tbl := range []string{"payments", "p2p_requests", "autopay", "invites", "users", "payment_log", "pending_invoices", "torrent_reports", "torrent_strikes"} {
		if _, err := db.Exec("DELETE FROM " + tbl); err != nil {
			t.Fatalf("очистка %s: %v", tbl, err)
		}
	}
}

func TestUsersAndP2P(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()

		if err := st.UpsertUser(ctx, 777); err != nil {
			t.Fatal(err)
		}
		u, err := st.GetUser(ctx, 777)
		if err != nil || u == nil {
			t.Fatalf("GetUser: %v %v", u, err)
		}
		if u.P2PApproved {
			t.Fatal("новый юзер не должен быть approved")
		}
		if err := st.SetP2PApproved(ctx, 777, true); err != nil {
			t.Fatal(err)
		}
		if u, _ = st.GetUser(ctx, 777); u == nil || !u.P2PApproved {
			t.Fatal("после SetP2PApproved должен быть approved")
		}
		if u2, _ := st.GetUser(ctx, 999999); u2 != nil {
			t.Fatal("несуществующий юзер -> nil")
		}

		r := &model.P2PRequest{TelegramID: 777, Months: 3, Price: "150", Status: model.P2PAwaiting}
		if err := st.CreateP2PRequest(ctx, r); err != nil {
			t.Fatal(err)
		}
		if r.ID == 0 {
			t.Fatal("id заявки не присвоен")
		}
		got, err := st.GetP2PRequest(ctx, r.ID)
		if err != nil || got == nil {
			t.Fatalf("GetP2PRequest: %v %v", got, err)
		}
		if got.Months != 3 || got.Status != model.P2PAwaiting {
			t.Fatalf("заявка не совпала: %+v", got)
		}
		got.Status = model.P2PApproved
		got.Screenshot = "fid"
		if err := st.UpdateP2PRequest(ctx, got); err != nil {
			t.Fatal(err)
		}
		if g2, _ := st.GetP2PRequest(ctx, r.ID); g2 == nil || g2.Status != model.P2PApproved || g2.Screenshot != "fid" {
			t.Fatalf("UpdateP2PRequest не применился: %+v", g2)
		}
	})
}

func TestUsersListBlockDelete(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		for _, id := range []int64{11, 22, 33} {
			if err := st.UpsertUser(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		users, total, err := st.ListUsers(ctx, 10, 0)
		if err != nil || total != 3 || len(users) != 3 {
			t.Fatalf("ListUsers: total=%d len=%d err=%v", total, len(users), err)
		}

		page, total, err := st.ListUsers(ctx, 2, 0)
		if err != nil || total != 3 || len(page) != 2 {
			t.Fatalf("ListUsers page: total=%d len=%d err=%v", total, len(page), err)
		}

		if err := st.SetBlocked(ctx, 22, true); err != nil {
			t.Fatal(err)
		}
		u, _ := st.GetUser(ctx, 22)
		if u == nil || !u.Blocked {
			t.Fatalf("после SetBlocked(true) должен быть Blocked: %+v", u)
		}
		if err := st.SetBlocked(ctx, 22, false); err != nil {
			t.Fatal(err)
		}
		if u, _ = st.GetUser(ctx, 22); u == nil || u.Blocked {
			t.Fatalf("после SetBlocked(false) не должен быть Blocked: %+v", u)
		}

		if err := st.SetBlocked(ctx, 44, true); err != nil {
			t.Fatal(err)
		}
		if u, _ = st.GetUser(ctx, 44); u == nil || !u.Blocked {
			t.Fatalf("SetBlocked должен апсертить: %+v", u)
		}

		if err := st.DeleteUser(ctx, 11); err != nil {
			t.Fatal(err)
		}
		if u, _ = st.GetUser(ctx, 11); u != nil {
			t.Fatal("после DeleteUser юзер должен исчезнуть")
		}
	})
}

func TestUserInfoAndPurchase(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()

		if err := st.SetUserInfo(ctx, 6882779276, "vasya", "Вася"); err != nil {
			t.Fatal(err)
		}
		if u, _ := st.GetUser(ctx, 6882779276); u != nil {
			t.Fatal("SetUserInfo не должен создавать запись")
		}

		if err := st.UpsertUser(ctx, 6882779276); err != nil {
			t.Fatal(err)
		}
		if err := st.SetUserInfo(ctx, 6882779276, "vasya", "Вася"); err != nil {
			t.Fatal(err)
		}
		u, _ := st.GetUser(ctx, 6882779276)
		if u == nil || u.Username != "vasya" || u.FirstName != "Вася" {
			t.Fatalf("ник/имя не сохранились: %+v", u)
		}

		if ok, _ := st.HasApprovedPurchase(ctx, 6882779276); ok {
			t.Fatal("без заявок покупок быть не должно")
		}
		r := &model.P2PRequest{TelegramID: 6882779276, Months: 1, Price: "100", Status: model.P2PApproved}
		if err := st.CreateP2PRequest(ctx, r); err != nil {
			t.Fatal(err)
		}
		if ok, _ := st.HasApprovedPurchase(ctx, 6882779276); !ok {
			t.Fatal("после approved-заявки покупка должна определяться")
		}

		users, _, err := st.ListUsers(ctx, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, x := range users {
			if x.TelegramID == 6882779276 && x.Username == "vasya" {
				found = true
			}
		}
		if !found {
			t.Fatalf("ник не попал в ListUsers: %+v", users)
		}
	})
}

func TestBalance(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		_ = st.UpsertUser(ctx, 555)
		if err := st.AddBalance(ctx, 555, 50000); err != nil {
			t.Fatal(err)
		}
		if u, _ := st.GetUser(ctx, 555); u == nil || u.Balance != 50000 {
			t.Fatalf("AddBalance: %+v", u)
		}
		ok, err := st.DeductBalance(ctx, 555, 15000)
		if err != nil || !ok {
			t.Fatalf("DeductBalance ok=%v err=%v", ok, err)
		}
		if u, _ := st.GetUser(ctx, 555); u == nil || u.Balance != 35000 {
			t.Fatalf("после списания баланс должен быть 35000: %+v", u)
		}

		if ok, _ := st.DeductBalance(ctx, 555, 99999); ok {
			t.Fatal("DeductBalance не должен списывать при нехватке")
		}
		if u, _ := st.GetUser(ctx, 555); u == nil || u.Balance != 35000 {
			t.Fatalf("баланс не должен меняться при нехватке: %+v", u)
		}
	})
}

func TestPendingInvoices(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()

		old := &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "yk_1", TelegramID: 555, Months: 1, CreatedAt: "2020-01-01T00:00:00Z"}
		fresh := &model.PendingInvoice{Method: model.PayMethodCryptoBot, ExtID: "cb:9", TelegramID: 555, Months: 3, CreatedAt: "2099-01-01T00:00:00Z"}
		if err := st.AddPendingInvoice(ctx, old); err != nil {
			t.Fatal(err)
		}
		if err := st.AddPendingInvoice(ctx, fresh); err != nil {
			t.Fatal(err)
		}

		list, err := st.ListUnresolvedPending(ctx, "2050-01-01T00:00:00Z", 10)
		if err != nil || len(list) != 1 || list[0].ExtID != "yk_1" {
			t.Fatalf("ListUnresolvedPending фильтр по времени неверен: %+v err=%v", list, err)
		}

		if err := st.ResolvePending(ctx, old.ID); err != nil {
			t.Fatal(err)
		}
		list, _ = st.ListUnresolvedPending(ctx, "2050-01-01T00:00:00Z", 10)
		if len(list) != 0 {
			t.Fatalf("после ResolvePending старый инвойс не должен возвращаться: %+v", list)
		}

		list, _ = st.ListUnresolvedPending(ctx, "2099-12-31T00:00:00Z", 1)
		if len(list) != 1 {
			t.Fatalf("limit не соблюдён: %d", len(list))
		}
	})
}

func TestPaymentsLog(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		if ok, _ := st.HasPaidPayment(ctx, 555); ok {
			t.Fatal("без записей оплат быть не должно")
		}
		if err := st.AddPayment(ctx, &model.Payment{TelegramID: 555, Method: model.PayMethodStars, Months: 1, Amount: "100 ⭐", Status: model.PaymentPaid}); err != nil {
			t.Fatal(err)
		}
		if err := st.AddPayment(ctx, &model.Payment{TelegramID: 555, Method: model.PayMethodP2P, Months: 3, Amount: "300 руб", Status: model.PaymentRejected, Comment: "no screenshot"}); err != nil {
			t.Fatal(err)
		}
		if ok, _ := st.HasPaidPayment(ctx, 555); !ok {
			t.Fatal("после paid-оплаты должно определяться")
		}
		items, total, err := st.ListPayments(ctx, 10, 0)
		if err != nil || total != 2 || len(items) != 2 {
			t.Fatalf("ListPayments: total=%d len=%d err=%v", total, len(items), err)
		}

		paid, err := st.PaidPayments(ctx)
		if err != nil || len(paid) != 1 || paid[0].Status != model.PaymentPaid {
			t.Fatalf("PaidPayments: len=%d err=%v", len(paid), err)
		}
	})
}

// PayLogsFiltered отбирает НА СТОРОНЕ БД и отдаёт полное число подходящих
// записей — без этого выгрузка на нагруженном боте молча теряла бы всё, что не
// поместилось в лимит.
func TestPayLogsFiltered(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		old := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339)
		recent := time.Now().UTC().Format(time.RFC3339)

		add := func(stage, created string, n int) {
			for i := 0; i < n; i++ {
				if err := st.AddPayLog(ctx, &model.PayLogEntry{
					ExtID: "e", TelegramID: 1, Method: "heleket", Stage: stage,
					Detail: "d", CreatedAt: created,
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
		add("invoice_created", recent, 40) // успешные — не должны попадать
		add("invoice_error", recent, 7)
		add("panel_error", old, 5) // старые — отсекаются окном

		stages := []string{"invoice_error", "panel_error", "verify_error"}

		// Без окна: 12 сбоев, успешные отброшены.
		got, total, err := st.PayLogsFiltered(ctx, stages, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		if total != 12 || len(got) != 12 {
			t.Fatalf("без окна: total=%d len=%d (ожидалось 12/12)", total, len(got))
		}
		for _, e := range got {
			if e.Stage == "invoice_created" {
				t.Fatal("в выборку сбоев попал успешный этап")
			}
		}

		// Окно в 7 суток отсекает старые записи.
		since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
		got, total, err = st.PayLogsFiltered(ctx, stages, since, 100)
		if err != nil {
			t.Fatal(err)
		}
		if total != 7 || len(got) != 7 {
			t.Fatalf("окно 7 суток: total=%d len=%d (ожидалось 7/7)", total, len(got))
		}

		// Лимит режет срез, но НЕ общее число — иначе усечение не объявить.
		got, total, err = st.PayLogsFiltered(ctx, stages, "", 3)
		if err != nil {
			t.Fatal(err)
		}
		if total != 12 || len(got) != 3 {
			t.Fatalf("лимит: total=%d len=%d (ожидалось 12/3)", total, len(got))
		}

		// Пустой список этапов — все записи.
		_, total, err = st.PayLogsFiltered(ctx, nil, "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		if total != 52 {
			t.Fatalf("без фильтра по этапам: total=%d (ожидалось 52)", total)
		}
	})
}

// Триал и пополнение баланса пишутся в payments с months = 0. Ни в
// «популярный тариф», ни в признак «пользователь платил» они попадать не
// должны — иначе ноль выигрывает группировку, а витрина, которая показывает
// плашку только для ненулевого срока, не показывает её никогда.
func TestPaidPaymentIgnoresTrialAndTopUp(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		const u int64 = 909

		if err := st.AddPayment(ctx, &model.Payment{
			TelegramID: u, Method: "trial", Months: 0, Amount: "—", Status: model.PaymentPaid,
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.AddPayment(ctx, &model.Payment{
			TelegramID: u, Method: model.PayMethodYooKassa, Months: 0, Amount: "500 ₽",
			Status: model.PaymentPaid, Comment: "topup", ExtID: "yk_top_909",
		}); err != nil {
			t.Fatal(err)
		}
		if ok, _ := st.HasPaidPayment(ctx, u); ok {
			t.Fatal("триал и пополнение не делают пользователя платящим")
		}
		if months, total, _ := st.MostPopularPlan(ctx); months != 0 || total != 0 {
			t.Fatalf("до покупок статистика должна быть пустой: months=%d total=%d", months, total)
		}

		if err := st.AddPayment(ctx, &model.Payment{
			TelegramID: u, Method: model.PayMethodStars, Months: 3, Amount: "300 ⭐",
			Status: model.PaymentPaid, ExtID: "st_909",
		}); err != nil {
			t.Fatal(err)
		}
		if ok, _ := st.HasPaidPayment(ctx, u); !ok {
			t.Fatal("после покупки подписки пользователь платящий")
		}
		if months, total, _ := st.MostPopularPlan(ctx); months != 3 || total != 1 {
			t.Fatalf("популярный тариф: months=%d total=%d (ожидалось 3/1)", months, total)
		}
	})
}

// Автосписания и незакрытые счета обязаны переживать перенос базы: без них
// после переезда у людей молча отключалось автопродление, а оплаченный, но
// не доставленный счёт переставал добиваться реконсилятором.
func TestTransferKeepsAutoPayAndPending(t *testing.T) {
	src := openSQLiteTest(t)
	dst := openSQLiteTest(t)
	ctx := context.Background()

	if err := src.SetAutoPay(ctx, &model.AutoPay{
		TelegramID: 777, Method: model.PayMethodYooKassa, MethodID: "pm_1", Title: "Карта •• 4242",
		Months: 3, Amount: "450", Currency: "RUB", Enabled: true, PaidPeriod: "2030-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := src.AddPendingInvoice(ctx, &model.PendingInvoice{
		ID: 42, Method: model.PayMethodCryptoBot, ExtID: "cb_42", TelegramID: 777, Months: 6,
		Purpose: "sub", Kopecks: 0,
	}); err != nil {
		t.Fatal(err)
	}

	if err := Transfer(ctx, src, dst); err != nil {
		t.Fatal(err)
	}

	ap, err := dst.GetAutoPay(ctx, 777)
	if err != nil || ap == nil {
		t.Fatalf("автосписание не перенеслось: %v", err)
	}
	if ap.MethodID != "pm_1" || ap.Months != 3 || !ap.Enabled || ap.PaidPeriod != "2030-01-01T00:00:00Z" {
		t.Fatalf("поля автосписания потеряны: %+v", ap)
	}

	list, err := dst.ListUnresolvedPending(ctx, "2099-12-31T00:00:00Z", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("незакрытый счёт не перенёсся: len=%d err=%v", len(list), err)
	}
	if list[0].ExtID != "cb_42" || list[0].Months != 6 {
		t.Fatalf("поля счёта потеряны: %+v", list[0])
	}
}

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

	// Засеваем источник: конфиг + пользователь (с ником/блоком/терминами) +
	// платёж + P2P-заявка + кэш медиа.
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

	// Конфиг.
	got, ok, err := dst.LoadConfig(ctx)
	if err != nil || !ok || got.Panel.APIToken != cfg.Panel.APIToken {
		t.Fatalf("config не перенёсся: ok=%v err=%v", ok, err)
	}
	// Пользователь со всеми полями.
	u, err := dst.GetUser(ctx, 777)
	if err != nil || u == nil {
		t.Fatalf("user не перенёсся: %v", err)
	}
	if u.Username != "vasya" || u.FirstName != "Вася" || !u.Blocked || u.TermsAcceptedAt == "" {
		t.Fatalf("поля юзера потеряны: %+v", u)
	}
	// Платёж.
	if ok, _ := dst.HasPaidPayment(ctx, 777); !ok {
		t.Fatal("платёж не перенёсся")
	}
	if dup, _ := dst.PaymentByExtID(ctx, "yk_1"); !dup {
		t.Fatal("ext_id платежа не перенёсся")
	}
	// P2P-заявка (id сохраняется).
	if r, err := dst.GetP2PRequest(ctx, pr.ID); err != nil || r == nil || r.Status != model.P2PApproved {
		t.Fatalf("p2p-заявка не перенёслась: %+v err=%v", r, err)
	}
	// Медиа-кэш.
	if id, ok, _ := dst.LoadMediaFileID(ctx, "main_menu"); !ok || id != "file_abc" {
		t.Fatalf("media_cache не перенёсся: id=%q ok=%v", id, ok)
	}

	// Идемпотентность: повторный перенос не должен падать (платёж-дубль пропускается).
	if err := Transfer(ctx, src, dst); err != nil {
		t.Fatalf("повторный Transfer упал: %v", err)
	}
}

// TestPostgresContract запускается, только если задан TEST_POSTGRES_DSN
// (в CI поднимается через сервис postgres). Прогоняет тот же контракт против PG.
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

// eachStore прогоняет fn против SQLite (всегда) и PostgreSQL (если задан TEST_POSTGRES_DSN).
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
			// PG-БД общая между тестами — чистим данные, чтобы счётчики были детерминированы.
			cleanPGData(t, dsn)
			fn(t, st)
		})
	}
}

// cleanPGData очищает таблицы данных в общей PG-БД (settings/schema_migrations не трогаем).
func cleanPGData(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tbl := range []string{"payments", "p2p_requests", "users"} {
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
		// пагинация
		page, total, err := st.ListUsers(ctx, 2, 0)
		if err != nil || total != 3 || len(page) != 2 {
			t.Fatalf("ListUsers page: total=%d len=%d err=%v", total, len(page), err)
		}

		// блокировка
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

		// SetBlocked для несуществующего создаёт запись
		if err := st.SetBlocked(ctx, 44, true); err != nil {
			t.Fatal(err)
		}
		if u, _ = st.GetUser(ctx, 44); u == nil || !u.Blocked {
			t.Fatalf("SetBlocked должен апсертить: %+v", u)
		}

		// удаление
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
		// SetUserInfo для несуществующего — no-op (строку не создаёт)
		if err := st.SetUserInfo(ctx, 6882779276, "vasya", "Вася"); err != nil {
			t.Fatal(err)
		}
		if u, _ := st.GetUser(ctx, 6882779276); u != nil {
			t.Fatal("SetUserInfo не должен создавать запись")
		}
		// после регистрации — обновляет ник/имя
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
		// HasApprovedPurchase
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
		// ник попадает в список
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

func TestPendingInvoices(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		// Свежий и старый pending.
		old := &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "yk_1", TelegramID: 555, Months: 1, CreatedAt: "2020-01-01T00:00:00Z"}
		fresh := &model.PendingInvoice{Method: model.PayMethodCryptoBot, ExtID: "cb:9", TelegramID: 555, Months: 3, CreatedAt: "2099-01-01T00:00:00Z"}
		if err := st.AddPendingInvoice(ctx, old); err != nil {
			t.Fatal(err)
		}
		if err := st.AddPendingInvoice(ctx, fresh); err != nil {
			t.Fatal(err)
		}
		// createdBefore = 2050 → видим только старый (fresh из 2099 отфильтрован).
		list, err := st.ListUnresolvedPending(ctx, "2050-01-01T00:00:00Z", 10)
		if err != nil || len(list) != 1 || list[0].ExtID != "yk_1" {
			t.Fatalf("ListUnresolvedPending фильтр по времени неверен: %+v err=%v", list, err)
		}
		// Резолв снимает с учёта.
		if err := st.ResolvePending(ctx, old.ID); err != nil {
			t.Fatal(err)
		}
		list, _ = st.ListUnresolvedPending(ctx, "2050-01-01T00:00:00Z", 10)
		if len(list) != 0 {
			t.Fatalf("после ResolvePending старый инвойс не должен возвращаться: %+v", list)
		}
		// limit соблюдается.
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
		// PaidPayments — только оплаченные (rejected не считается).
		paid, err := st.PaidPayments(ctx)
		if err != nil || len(paid) != 1 || paid[0].Status != model.PaymentPaid {
			t.Fatalf("PaidPayments: len=%d err=%v", len(paid), err)
		}
	})
}

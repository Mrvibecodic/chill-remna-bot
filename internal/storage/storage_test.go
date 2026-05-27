package storage

import (
	"context"
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
	cfg := sampleConfig()
	if err := src.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := Transfer(ctx, src, dst); err != nil {
		t.Fatal(err)
	}
	got, ok, err := dst.LoadConfig(ctx)
	if err != nil || !ok {
		t.Fatalf("load из dst: ok=%v err=%v", ok, err)
	}
	if got.Panel.APIToken != cfg.Panel.APIToken {
		t.Fatal("Transfer потерял данные")
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
			fn(t, st)
		})
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

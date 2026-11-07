package app

import (
	"context"
	"testing"

	"remnabot/internal/model"
)

// Platega узнаёт покупателя по metadata.user_email: Telegram — числовой ID с
// telegram.org, аккаунт кабинета — его почта. Почты нет — только ID.
func TestPlategaBuyerMetadata(t *testing.T) {
	cases := []struct {
		name      string
		id        int64
		webEmail  string
		wantEmail any
	}{
		{"telegram", 1000000001, "", "1000000001@telegram.org"},
		{"почта", -1000000000021, "buyer@example.com", "buyer@example.com"},
		{"почты нет", -1000000000022, "", nil},
		{"почта, привязан Telegram", 1000000023, "linked@example.com", "linked@example.com"},
	}
	for _, c := range cases {
		t.Run(c.name+" пополнение", func(t *testing.T) {
			a, _, fs, created := plTopUpStub(t, true)
			ctx := context.Background()
			_ = fs.UpsertUser(ctx, c.id)
			if c.webEmail != "" {
				_ = fs.CreateWebUser(ctx, &model.WebUser{TgID: c.id, Email: c.webEmail, PassHash: "x"})
			}
			if _, _, err := a.topUpCreate(ctx, c.id, 10000, "pl", c.id < 0); err != nil {
				t.Fatalf("счёт: %v", err)
			}
			checkPlMeta(t, *created, c.id, c.wantEmail)
		})
		t.Run(c.name+" покупка", func(t *testing.T) {
			a, _, fs, created := plTopUpStub(t, true)
			ctx := context.Background()
			if c.webEmail != "" {
				_ = fs.CreateWebUser(ctx, &model.WebUser{TgID: c.id, Email: c.webEmail, PassHash: "x"})
			}
			if _, _, err := a.plCreateTransactionSnap(ctx, c.id, 1, 150, "VPN", "https://example.com/r", nil); err != nil {
				t.Fatalf("счёт: %v", err)
			}
			checkPlMeta(t, *created, c.id, c.wantEmail)
		})
	}
}

func checkPlMeta(t *testing.T, body map[string]any, id int64, wantEmail any) {
	t.Helper()
	md, _ := body["metadata"].(map[string]any)
	if md == nil {
		t.Fatalf("в запросе нет metadata: %v", body)
	}
	if md["user_email"] != wantEmail {
		t.Fatalf("user_email = %v, ждали %v", md["user_email"], wantEmail)
	}
	if md["userId"] != itoa64(id) {
		t.Fatalf("userId = %v, ждали %d", md["userId"], id)
	}
}

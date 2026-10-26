package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// Аккаунту кабинета по почте (отрицательный id) чата в Telegram нет: после
// выдачи подписки и пополнения бот не должен туда писать — Telegram отвечал
// «chat not found» на каждую попытку. Обычному пользователю сообщение
// по-прежнему уходит.
func TestNoChatForEmailAccount(t *testing.T) {
	a, fm, fs := p2pTopUpApp(t, true)
	ctx := context.Background()
	const email int64 = -1000000001
	const tg int64 = 1000000002
	_ = fs.UpsertUser(ctx, email)
	_ = fs.UpsertUser(ctx, tg)

	if err := a.finalizeTopUp(ctx, email, 5000, model.PayMethodP2P, "50 ₽", "p2p-1"); err != nil {
		t.Fatal(err)
	}
	if u, _ := fs.GetUser(ctx, email); u == nil || u.Balance != 5000 {
		t.Fatalf("баланс не зачислен: %+v", u)
	}
	a.sendSubActive(ctx, email, "https://example.test/s", "2030-01-01T00:00:00Z")
	if got := fm.joined(); strings.Contains(got, "Баланс пополнен") || strings.Contains(got, "example.test/s") {
		t.Fatalf("аккаунту без чата ушло сообщение:\n%s", got)
	}

	if err := a.finalizeTopUp(ctx, tg, 5000, model.PayMethodP2P, "50 ₽", "p2p-2"); err != nil {
		t.Fatal(err)
	}
	a.sendSubActive(ctx, tg, "https://example.test/s", "2030-01-01T00:00:00Z")
	if got := fm.joined(); !strings.Contains(got, "Баланс пополнен") || !strings.Contains(got, "example.test/s") {
		t.Fatalf("обычному пользователю сообщение не ушло:\n%s", got)
	}
}

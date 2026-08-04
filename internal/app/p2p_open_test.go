package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// При «перевод всем без одобрения» реквизиты выдаются сразу, без заявки админу.
func TestP2POpenForAll(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.P2P = model.P2PConfig{Enabled: true, OpenForAll: true, Cards: []string{"0000 1111 2222 3333"}}
	a.botCfg.NormalizePricing()
	a.botCfg.Pricing.Base = map[int]string{1: "100"}
	ctx := context.Background()

	a.getUI(200).buyMonths = 1
	a.startP2P(ctx, 200)

	joined := strings.Join(fm.texts, "\n")
	if !strings.Contains(joined, "0000 1111 2222 3333") {
		t.Fatalf("ожидались реквизиты карты, получено: %q", joined)
	}
	if reqs := len(fs.reqs); reqs != 1 {
		t.Fatalf("ожидалась одна заявка на оплату, получено %d", reqs)
	}
}

// Без опции — прежнее поведение: нужен ручной допуск админа.
func TestP2PNeedsApprovalWhenClosed(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.P2P = model.P2PConfig{Enabled: true, Cards: []string{"0000"}}
	a.botCfg.NormalizePricing()
	ctx := context.Background()

	a.startP2P(ctx, 201)
	if strings.Contains(strings.Join(fm.texts, "\n"), "0000") {
		t.Fatal("без одобрения реквизиты выдавать нельзя")
	}
	if u, _ := fs.GetUser(ctx, 201); u != nil && u.P2PApproved {
		t.Fatal("пользователь не должен становиться одобренным сам по себе")
	}
}

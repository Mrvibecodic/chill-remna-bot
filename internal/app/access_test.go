package app

import (
	"context"
	"testing"
	"time"

	"remnabot/internal/model"
)

// В режиме «по приглашениям» новичок не проходит, а после активации кода —
// проходит, и повторно код не тратится.
func TestAccessInvite_RedeemGrantsAccess(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	a.setAccessMode(ctx, model.AccessInvite)

	if !a.denyAccess(ctx, 500, false) {
		t.Fatal("в режиме приглашений посторонний не должен проходить")
	}
	inv := &model.Invite{Code: "abc123", MaxUses: 1}
	if err := fs.CreateInvite(ctx, inv); err != nil {
		t.Fatalf("создание приглашения: %v", err)
	}
	if msg, ok := a.redeemInvite(ctx, 500, "abc123"); !ok || msg == "" {
		t.Fatalf("активация приглашения: ok=%v msg=%q", ok, msg)
	}
	if a.denyAccess(ctx, 500, false) {
		t.Fatal("после активации приглашения доступ должен быть")
	}
	// Лимит исчерпан — второй пользователь не пройдёт.
	if _, ok := a.redeemInvite(ctx, 501, "abc123"); ok {
		t.Fatal("приглашение на 1 активацию не должно срабатывать дважды")
	}
	if !a.denyAccess(ctx, 501, false) {
		t.Fatal("второй пользователь без приглашения не должен проходить")
	}
}

func TestAccessInvite_ExpiredAndRevoked(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	a.setAccessMode(ctx, model.AccessInvite)

	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	_ = fs.CreateInvite(ctx, &model.Invite{Code: "old", MaxUses: 0, ExpiresAt: past})
	if _, ok := a.redeemInvite(ctx, 600, "old"); ok {
		t.Fatal("просроченное приглашение не должно активироваться")
	}

	_ = fs.CreateInvite(ctx, &model.Invite{Code: "rev", MaxUses: 5})
	_ = fs.RevokeInvite(ctx, "rev")
	if _, ok := a.redeemInvite(ctx, 601, "rev"); ok {
		t.Fatal("отозванное приглашение не должно активироваться")
	}
}

// Приглашение с неограниченным сроком и числом активаций работает многократно.
func TestAccessInvite_Unlimited(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	a.setAccessMode(ctx, model.AccessInvite)
	_ = fs.CreateInvite(ctx, &model.Invite{Code: "many", MaxUses: 0})

	for _, id := range []int64{700, 701, 702} {
		if _, ok := a.redeemInvite(ctx, id, "many"); !ok {
			t.Fatalf("многоразовое приглашение должно срабатывать для %d", id)
		}
		if a.denyAccess(ctx, id, false) {
			t.Fatalf("пользователь %d должен иметь доступ", id)
		}
	}
}

// Публичный режим никого не отсекает, а вайтлист-режим продолжает работать
// по-старому (legacy-флаг WhitelistMode).
func TestAccessModes_PublicAndLegacyWhitelist(t *testing.T) {
	a, _ := refTestApp(t)
	ctx := context.Background()

	a.setAccessMode(ctx, model.AccessPublic)
	if a.denyAccess(ctx, 800, false) {
		t.Fatal("публичный режим должен пускать всех")
	}
	if a.botCfg.WhitelistMode {
		t.Fatal("в публичном режиме legacy-флаг должен быть выключен")
	}

	a.setAccessMode(ctx, model.AccessWhitelist)
	if !a.botCfg.WhitelistMode {
		t.Fatal("режим вайтлиста должен выставлять legacy-флаг")
	}
	if !a.denyAccess(ctx, 801, false) {
		t.Fatal("вайтлист должен отсекать посторонних")
	}
}

func TestNewInviteCode_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		c := newInviteCode()
		if c == "" || len(c) != inviteCodeLen {
			t.Fatalf("некорректный код: %q", c)
		}
		if seen[c] {
			t.Fatalf("повтор кода приглашения: %q", c)
		}
		seen[c] = true
	}
}

// Полный путь: приглашённый открывает t.me/бот?start=inv_КОД — доступ должен
// выдаться ДО проверки режима публичности, иначе ссылка бесполезна.
func TestInviteViaStartCommand(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	a.setAccessMode(ctx, model.AccessInvite)
	_ = fs.CreateInvite(ctx, &model.Invite{Code: "linkcode", MaxUses: 1})

	a.handleMessage(ctx, msgText(900, "/start inv_linkcode"))
	if !a.accessGranted(ctx, 900) {
		t.Fatalf("после перехода по ссылке доступ должен быть выдан, сообщения: %v", fm.texts)
	}
	if a.denyAccess(ctx, 900, false) {
		t.Fatal("приглашённый должен проходить дальше")
	}

	// Неверный код: доступа нет, пользователь получает объяснение.
	a.handleMessage(ctx, msgText(901, "/start inv_нетакого"))
	if a.accessGranted(ctx, 901) {
		t.Fatal("по неверному коду доступ выдаваться не должен")
	}
}

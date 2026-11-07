package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

func inviteUsers(t *testing.T, fs *fakeStore, code string) []int64 {
	t.Helper()
	list, _, err := fs.InviteUses(context.Background(), code, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, u := range list {
		ids = append(ids, u.TelegramID)
	}
	return ids
}

// Закрытый бот: вошедший по ссылке записывается за приглашением, а экран
// «Кто пришёл» открывается из карточки приглашения и показывает его.
func TestInviteUses_InviteModeRecordsAndShows(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	a.setAccessMode(ctx, model.AccessInvite, false)
	_ = fs.CreateInvite(ctx, &model.Invite{Code: "closedcode", MaxUses: 0})

	a.handleMessage(ctx, msgText(1000000001, "/start inv_closedcode"))
	if got := inviteUsers(t, fs, "closedcode"); len(got) != 1 || got[0] != 1000000001 {
		t.Fatalf("вошедший не записан: %v", got)
	}

	a.handleCallback(ctx, cb(100, "acc:show:closedcode"))
	found := false
	for _, d := range fm.allCallbackData() {
		if d == "acc:who:closedcode:0" {
			found = true
		}
	}
	if !found {
		t.Fatal("в карточке приглашения нет кнопки «Кто пришёл»")
	}
	a.handleCallback(ctx, cb(100, "acc:who:closedcode:0"))
	found = false
	for _, d := range fm.allCallbackData() {
		if d == "usr:view:1000000001" {
			found = true
		}
	}
	if !found {
		t.Fatalf("в списке пришедших нет человека, экран: %s", fm.joined())
	}

	// Чужой экран открыть нельзя.
	before := len(fm.allCallbackData())
	a.handleCallback(ctx, cb(1000000001, "acc:who:closedcode:0"))
	for _, d := range fm.allCallbackData()[before:] {
		if d == "usr:view:1000000001" {
			t.Fatal("список пришедших показан не администратору")
		}
	}
}

// Открытый бот: приглашение только помечает новичков — без лимита и срока,
// без уведомления администратору, уже знакомых и повторы не пишет.
func TestInviteUses_OpenModeMarksNewcomers(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	_ = fs.CreateInvite(ctx, &model.Invite{Code: "opencode", MaxUses: 1, ExpiresAt: "2000-01-01T00:00:00Z"})
	_ = fs.CreateInvite(ctx, &model.Invite{Code: "revcode", MaxUses: 0})
	_ = fs.RevokeInvite(ctx, "revcode")
	_ = fs.UpsertUser(ctx, 1000000002)

	a.handleMessage(ctx, msgText(1000000002, "/start inv_opencode"))
	a.handleMessage(ctx, msgText(1000000003, "/start inv_opencode"))
	a.handleMessage(ctx, msgText(1000000004, "/start inv_opencode"))
	a.handleMessage(ctx, msgText(1000000003, "/start inv_opencode"))
	a.MiniStart(ctx, 1000000005, "inv_opencode", false)
	a.handleMessage(ctx, msgText(1000000006, "/start inv_revcode"))

	got := inviteUsers(t, fs, "opencode")
	want := map[int64]bool{1000000003: true, 1000000004: true, 1000000005: true}
	if len(got) != len(want) {
		t.Fatalf("записаны %v, ждали новичков %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("записан не тот: %v", got)
		}
	}
	if inv, _ := fs.GetInvite(ctx, "opencode"); inv.Used != 3 {
		t.Fatalf("счётчик активаций: %d, ждали 3", inv.Used)
	}
	if got := inviteUsers(t, fs, "revcode"); len(got) != 0 {
		t.Fatalf("отозванное приглашение записало людей: %v", got)
	}
	if strings.Contains(fm.joined(), "По приглашению") {
		t.Fatal("в открытом режиме администратору не должны приходить уведомления")
	}
	if u := fs.users[1000000003]; u != nil && u.Whitelisted {
		t.Fatal("в открытом режиме приглашение не должно выдавать доступ")
	}
}

// Удаление приглашения уносит и список пришедших.
func TestInviteUses_DeleteClears(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	a.setAccessMode(ctx, model.AccessInvite, false)
	_ = fs.CreateInvite(ctx, &model.Invite{Code: "gone", MaxUses: 0})
	if _, ok := a.redeemInvite(ctx, 1000000007, "gone"); !ok {
		t.Fatal("приглашение не сработало")
	}
	a.onAccess(ctx, 100, "del:gone")
	if _, n, _ := fs.InviteUses(ctx, "gone", 0, 0); n != 0 {
		t.Fatalf("после удаления приглашения осталось записей: %d", n)
	}
}

// Открытый бот: новое приглашение создаётся с экрана «Доступ», а карточка
// говорит, что ссылка только записывает пришедших. В белом списке — что
// ссылка не действует, и создать её с экрана нельзя.
func TestInviteCard_HintByMode(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	_ = fs.CreateInvite(ctx, &model.Invite{Code: "hintcode", MaxUses: 0})

	cases := []struct {
		mode, hint string
		newBtn     bool
	}{
		{model.AccessPublic, "ссылка доступ не выдаёт", true},
		{model.AccessInvite, "получит постоянный доступ", true},
		{model.AccessWhitelist, "никто не войдёт и не запишется", false},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			a.setAccessMode(ctx, c.mode, true)
			before := len(fm.allCallbackData())
			a.handleCallback(ctx, cb(100, "menu:access"))
			hasNew := false
			for _, d := range fm.allCallbackData()[before:] {
				if d == "acc:new" {
					hasNew = true
				}
			}
			if hasNew != c.newBtn {
				t.Fatalf("кнопка «Новое приглашение» на экране «Доступ»: %v, ждали %v", hasNew, c.newBtn)
			}
			n := len(fm.texts)
			a.handleCallback(ctx, cb(100, "acc:show:hintcode"))
			card := strings.Join(fm.texts[n:], "\n")
			if !strings.Contains(card, c.hint) {
				t.Fatalf("в карточке нет подсказки %q:\n%s", c.hint, card)
			}
		})
	}
}

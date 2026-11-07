package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// Карточка пользователя показывает тариф последней покупки под текущим
// именем, удалённый — под именем из сделки с пометкой, без покупок — ничего.
func TestUserCard_PlanName(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	_ = fs.SavePlan(ctx, &model.Plan{Code: "vip", Name: "Премиум <VIP>"})
	for _, id := range []int64{1000000011, 1000000012, 1000000013} {
		_ = fs.UpsertUser(ctx, id)
	}
	fs.users[1000000011].Snapshot = &model.PlanSnapshot{Code: "vip", Name: "Старое имя"}
	fs.users[1000000012].Snapshot = &model.PlanSnapshot{Code: "gone", Name: "Летний"}

	cases := []struct {
		id   int64
		want string
		not  string
	}{
		{1000000011, "🏷 Последний тариф: <b>Премиум &lt;VIP&gt;</b>", "Старое имя"},
		{1000000012, "🏷 Последний тариф: <b>Летний</b> (тариф удалён)", ""},
		{1000000013, "", "Последний тариф"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			before := len(fm.texts)
			a.handleCallback(ctx, cb(100, "usr:view:"+itoa(int(c.id))))
			card := strings.Join(fm.texts[before:], "\n")
			if c.want != "" && !strings.Contains(card, c.want) {
				t.Fatalf("нет строки %q в карточке:\n%s", c.want, card)
			}
			if c.not != "" && strings.Contains(card, c.not) {
				t.Fatalf("лишнее %q в карточке:\n%s", c.not, card)
			}
		})
	}
}

type planErrStore struct{ *fakeStore }

func (planErrStore) GetPlan(context.Context, string) (*model.Plan, error) {
	return nil, errors.New("база недоступна")
}

// Сбой чтения тарифа не выдаётся за удалённый тариф.
func TestUserCard_PlanReadError(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = planErrStore{fs}
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 1000000014)
	fs.users[1000000014].Snapshot = &model.PlanSnapshot{Code: "vip", Name: "Премиум"}
	a.handleCallback(ctx, cb(100, "usr:view:1000000014"))
	card := fm.joined()
	if !strings.Contains(card, "🏷 Последний тариф: <b>Премиум</b>") || strings.Contains(card, "удалён") {
		t.Fatalf("карточка при сбое чтения тарифа:\n%s", card)
	}
}

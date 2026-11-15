package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// Подпись пользователя в админке: по способу регистрации; значок 💬 — только
// на кнопке и только когда у почтового аккаунта есть второй способ входа
// (привязан Telegram). В текстах — «почта / @ник (ID)».
func TestUserLabels_ByLoginMethods(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()

	const tgOnly, mailOnly, both, bothNoNick int64 = 1000000041, -1000000042, 1000000043, 1000000044
	for _, id := range []int64{tgOnly, mailOnly, both, bothNoNick} {
		_ = fs.UpsertUser(ctx, id)
	}
	fs.users[tgOnly].Username = "alex_demo"
	fs.users[both].Username = "ivan_tg"
	fs.users[bothNoNick].FirstName = "Олег"
	_ = fs.CreateWebUser(ctx, &model.WebUser{TgID: mailOnly, Email: "maria@example.com"})
	_ = fs.CreateWebUser(ctx, &model.WebUser{TgID: both, Email: "ivan@example.com", PrevTgID: -1})
	_ = fs.CreateWebUser(ctx, &model.WebUser{TgID: bothNoNick, Email: "o<leg>@example.com", PrevTgID: -2})

	a.handleCallback(ctx, cb(100, "usr:list"))
	if !strings.Contains(fm.last(), "💬 после почты — к аккаунту привязан Telegram") {
		t.Fatalf("в списке нет пояснения:\n%s", fm.last())
	}
	var labels []string
	for _, row := range fm.lastKB {
		for _, b := range row {
			labels = append(labels, b.Text)
		}
	}
	all := strings.Join(labels, "\n")
	for _, want := range []string{
		"@alex_demo (1000000041)",
		"maria@example.com",
		"ivan@example.com 💬",
		"o<leg>@example.com 💬",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("нет кнопки %q среди:\n%s", want, all)
		}
	}
	for _, bad := range []string{"📧", "@alex_demo (1000000041) 💬", "maria@example.com 💬", "ivan_tg"} {
		if strings.Contains(all, bad) {
			t.Fatalf("лишнее %q на кнопках:\n%s", bad, all)
		}
	}

	// Поиск подписывает кнопки так же, а не текстовой подписью с ником.
	a.handleCallback(ctx, cb(100, "usr:find"))
	a.handleMessage(ctx, msgText(100, "ivan"))
	var found []string
	for _, row := range fm.lastKB {
		for _, b := range row {
			found = append(found, b.Text)
		}
	}
	if f := strings.Join(found, "\n"); !strings.Contains(f, "ivan@example.com 💬") || strings.Contains(f, "ivan_tg") ||
		!strings.Contains(fm.last(), "💬 после почты") {
		t.Fatalf("поиск:\n%s\n%s", fm.last(), f)
	}

	cases := []struct {
		id   int64
		want string
	}{
		{tgOnly, "👤 <b>@alex_demo (1000000041)</b>"},
		{mailOnly, "👤 <b>maria@example.com</b>"},
		{both, "👤 <b>ivan@example.com / @ivan_tg (1000000043)</b>"},
		{bothNoNick, "👤 <b>o&lt;leg&gt;@example.com / Олег (1000000044)</b>"},
	}
	for _, c := range cases {
		a.handleCallback(ctx, cb(100, "usr:view:"+itoa64(c.id)))
		if !strings.Contains(fm.last(), c.want) || strings.Contains(fm.last(), "💬") {
			t.Fatalf("карточка %d: ждём %q:\n%s", c.id, c.want, fm.last())
		}
	}
}

package app

import (
	"context"
	"strings"
	"testing"
)

// Прогноз в «Кошельке» — обычные строки: таблица в <pre> на телефоне
// переносилась и получала кнопку «Копировать код».
func TestWalletForecastPlainLines(t *testing.T) {
	a, fs := planApp(t)
	fm := a.msg.(*fakeMsg)
	ctx := context.Background()
	const uid int64 = 1000000001
	_ = fs.UpsertUser(ctx, uid)
	_ = fs.AddBalance(ctx, uid, 45000) // 450 ₽
	a.handleCallback(ctx, cb(uid, "menu:balance"))
	out := fm.joined()
	if strings.Contains(out, "<pre>") || strings.Contains(out, "Plan") {
		t.Fatalf("прогноз всё ещё таблицей:\n%s", out)
	}
	for _, want := range []string{"• 1 мес — 150 ₽ · Хватит: 3× ≈ 3 мес", "• 3 мес — 400 ₽ · Хватит: 1× ≈ 3 мес", "• 12 мес — 1400 ₽ · Хватит: —"} {
		if !strings.Contains(out, want) {
			t.Fatalf("нет строки %q:\n%s", want, out)
		}
	}
}

// «Продлить» без покупок: пояснение без своей кнопки «Главная» и уходит из
// чата вместе с витриной, а не остаётся висеть.
func TestRenewNoHistoryNoticeLeavesWithScreen(t *testing.T) {
	a, _ := planApp(t)
	fm := a.msg.(*fakeMsg)
	ctx := context.Background()
	const uid int64 = 1000000001
	a.handleCallback(ctx, cb(uid, "menu:renew"))
	note := 0
	for id, txt := range fm.live {
		if strings.Contains(txt, "продлевать нечего") {
			note = id
		}
	}
	if note == 0 {
		t.Fatalf("пояснение не показано:\n%s", fm.joined())
	}
	if a.screenMsgID(uid) == note {
		t.Fatal("текущим экраном считается пояснение, а не витрина")
	}
	a.handleCallback(ctx, cb(uid, "menu:home"))
	if _, alive := fm.live[note]; alive {
		t.Fatal("пояснение осталось в чате после перехода")
	}
}

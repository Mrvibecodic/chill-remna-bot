package app

import (
	"context"
	"strings"
	"testing"
)

// Недоплата по уведомлению (заплачено 148 при счёте 150) окончательна: сверка,
// опрос мини-аппа и кнопка «Проверить» видят только зачисленное (146.52 —
// в пределах допуска комиссии) и выдавать по той же операции не должны.
func TestYooMoneyUnderpaidVerdictSticks(t *testing.T) {
	a, fm, fs, st := ymStub(t)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	_, label, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	st.ops = []map[string]any{ymOp("op-1", label, 146.52)}
	if _, err := a.HandleYooMoneyWebhook(ctx, ymForm("s3cret", "op-1", label, "148.00", "146.52")); err != nil {
		t.Fatal(err)
	}
	if done, _ := fs.PaymentByExtID(ctx, label); done {
		t.Fatal("недоплата выдана вебхуком")
	}
	p, _ := fs.PendingByExtID(ctx, label)
	if p == nil || !p.Resolved {
		t.Fatal("после недоплаты счёт не закрыт")
	}
	a.reconcileInvoice(ctx, fs, p)
	a.handleCallback(ctx, cb(u, "ymc:"+label))
	if done, _ := fs.PaymentByExtID(ctx, label); done {
		t.Fatal("недоплата выдана сверкой или кнопкой")
	}
	if n := strings.Count(fm.joined(), "НЕ выданы"); n != 1 {
		t.Fatalf("тревог о недоплате %d, ожидалась одна:\n%s", n, fm.joined())
	}
	if strings.Contains(fm.joined(), "повторная оплата") {
		t.Fatalf("недоплата названа повторной оплатой:\n%s", fm.joined())
	}

	// Вторая, полная оплата той же ссылки выдаётся — вердикт касается
	// только своей операции.
	st.ops = append([]map[string]any{ymOp("op-2", label, 145.50)}, st.ops...)
	if _, err := a.HandleYooMoneyWebhook(ctx, ymForm("s3cret", "op-2", label, "150.00", "145.50")); err != nil {
		t.Fatal(err)
	}
	if done, _ := fs.PaymentByExtID(ctx, label); !done {
		t.Fatal("полная оплата после недоплаты не выдана")
	}
}

// Решение о выдаче — по истории кошелька: «заплачено 150» в уведомлении не
// перекрывает 0.97 ₽, дошедшие до кошелька.
func TestYooMoneyWithdrawDoesNotOverrideHistory(t *testing.T) {
	a, _, fs, st := ymStub(t)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	_, label, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	st.ops = []map[string]any{ymOp("op-1", label, 0.97)}
	if _, err := a.HandleYooMoneyWebhook(ctx, ymForm("s3cret", "op-1", label, "150.00", "0.97")); err != nil {
		t.Fatal(err)
	}
	if done, _ := fs.PaymentByExtID(ctx, label); done {
		t.Fatal("выдано по сумме из уведомления")
	}
}

// Уведомление по второй оплате пришло раньше, чем по первой: админ всё равно
// узнаёт о лишней оплате.
func TestYooMoneyDuplicateAlertOutOfOrder(t *testing.T) {
	a, fm, fs, st := ymStub(t)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	_, label, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	st.ops = []map[string]any{ymOp("op-B", label, 145.50), ymOp("op-A", label, 145.50)}
	for _, op := range []string{"op-B", "op-A"} {
		if _, err := a.HandleYooMoneyWebhook(ctx, ymForm("s3cret", op, label, "150.00", "145.50")); err != nil {
			t.Fatal(err)
		}
	}
	if done, _ := fs.PaymentByExtID(ctx, label); !done {
		t.Fatal("не выдано")
	}
	if n := strings.Count(fm.joined(), "повторная оплата"); n != 1 {
		t.Fatalf("тревог о лишней оплате %d, ожидалась одна:\n%s", n, fm.joined())
	}
}

// Кнопка «Проверить» при недоплате или без строки счёта не называет ту же
// операцию «повторной оплатой».
func TestYooMoneyButtonNoFalseDuplicate(t *testing.T) {
	a, fm, fs, st := ymStub(t)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	_, label, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	st.ops = []map[string]any{ymOp("op-1", label, 100.00)}
	a.handleCallback(ctx, cb(u, "ymc:"+label))
	lbl := "ym:555:aaaaaaaaaaaa"
	st.ops = append(st.ops, ymOp("op-x", lbl, 150.00))
	a.handleCallback(ctx, cb(u, "ymc:"+lbl))
	if strings.Contains(fm.joined(), "повторная оплата") {
		t.Fatalf("ложная «повторная оплата»:\n%s", fm.joined())
	}
}

// Смена токена на чужой кошелёк при включённом способе выключает способ;
// совпадающий кошелёк его не трогает.
func TestYooMoneyMismatchTurnsMethodOff(t *testing.T) {
	a, fm, _, st := ymStub(t)
	ctx := context.Background()
	a.setYooMoneyField(ctx, 100, "ym_token", "tok-ok")
	if !a.ymConfig().Enabled {
		t.Fatal("совпадающий кошелёк выключил способ")
	}
	st.account = "410011000000009"
	a.setYooMoneyField(ctx, 100, "ym_token", "tok-ok")
	if a.ymConfig().Enabled {
		t.Fatal("чужой кошелёк оставил способ включённым")
	}
	if !strings.Contains(fm.last(), "Способ выключен") {
		t.Fatalf("админ не узнал о выключении: %s", fm.last())
	}
}

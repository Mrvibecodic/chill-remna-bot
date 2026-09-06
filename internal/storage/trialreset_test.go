package storage

import (
	"context"
	"testing"

	"remnabot/internal/model"
)

// Отбор кандидатов на возврат триала проверяется на НАСТОЯЩЕЙ базе обоих
// диалектов: двойник хранилища повторяет условия своими словами и разойтись с
// SQL может незаметно.
func TestTrialResetTargets(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		const expired = "2020-01-01T00:00:00Z"

		mk := func(id int64, trial, expire string) {
			t.Helper()
			if err := st.UpsertUser(ctx, id); err != nil {
				t.Fatal(err)
			}
			if trial != "" {
				if err := st.SetTrialUsed(ctx, id, trial); err != nil {
					t.Fatal(err)
				}
			}
			if expire != "" {
				if err := st.SetSubExpiry(ctx, id, expire, "trial"); err != nil {
					t.Fatal(err)
				}
			}
			if trial != "" {
				if err := st.AddPayment(ctx, &model.Payment{
					TelegramID: id, Method: model.PayMethodTrial, Amount: "—", Status: model.PaymentPaid,
				}); err != nil {
					t.Fatal(err)
				}
			}
		}

		mk(9001, expired, expired) // кандидат
		mk(9002, "", expired)      // триал не брал
		mk(9003, expired, "")      // подписки не было вовсе
		mk(9004, expired, expired) // заплатил
		if err := st.AddPayment(ctx, &model.Payment{
			TelegramID: 9004, Method: model.PayMethodYooKassa, Months: 1,
			Amount: "150", Status: model.PaymentPaid,
		}); err != nil {
			t.Fatal(err)
		}
		mk(9005, expired, expired) // забанен
		if err := st.SetBlocked(ctx, 9005, true); err != nil {
			t.Fatal(err)
		}
		mk(-9006, expired, expired) // кабинетный аккаунт без чата

		idsLim := func(maxResets, lim int) map[int64]int {
			t.Helper()
			out := map[int64]int{}
			targets, err := st.ListTrialResetTargets(ctx, maxResets, lim)
			if err != nil {
				t.Fatal(err)
			}
			for _, x := range targets {
				out[x.TelegramID] = x.Resets
			}
			return out
		}
		ids := func(maxResets int) map[int64]int { return idsLim(maxResets, 100) }

		got := ids(1)
		if len(got) != 1 {
			t.Fatalf("в кандидаты попал кто-то лишний: %v", got)
		}
		if _, ok := got[9001]; !ok {
			t.Fatalf("кандидат потерян: %v", got)
		}

		// Возврат снимает отметку, чистит зеркало срока и поднимает счётчик.
		ok, err := st.ResetTrialForRepeat(ctx, 9001, expired)
		if err != nil || !ok {
			t.Fatalf("возврат не сработал: ok=%v err=%v", ok, err)
		}
		u, err := st.GetUser(ctx, 9001)
		if err != nil {
			t.Fatal(err)
		}
		if u.TrialUsedAt != "" || u.SubExpireAt != "" || u.NotifyKind != "" {
			t.Fatalf("состояние не очищено: %+v", u)
		}

		// Повторный вызов на уже сброшенном не должен накручивать счётчик:
		// иначе гонка двух проходов съедала бы потолок повторов вдвое.
		if ok, err := st.ResetTrialForRepeat(ctx, 9001, expired); err != nil || ok {
			t.Fatalf("повторный сброс отчитался как сделанный: ok=%v err=%v", ok, err)
		}

		// Снова берём триал — и потолок в один повтор его уже не пускает.
		if err := st.SetTrialUsed(ctx, 9001, expired); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSubExpiry(ctx, 9001, expired, "trial"); err != nil {
			t.Fatal(err)
		}
		if got := ids(1); len(got) != 0 {
			t.Fatalf("потолок повторов не соблюдён: %v", got)
		}
		if got := ids(2); got[9001] != 1 {
			t.Fatalf("счётчик повторов не сохранился: %v", got)
		}
		if n, err := st.TrialResets(ctx, 9001); err != nil || n != 1 {
			t.Fatalf("счётчик повторов читается неверно: %d %v", n, err)
		}
		// Человек ОПЛАТИЛ между отбором и записью: срок в базе уже не тот,
		// что видел проход, и стирать его нельзя.
		if err := st.SetSubExpiry(ctx, 9001, "2099-01-01T00:00:00Z", "paid"); err != nil {
			t.Fatal(err)
		}
		if ok, err := st.ResetTrialForRepeat(ctx, 9001, expired); err != nil || ok {
			t.Fatalf("возврат затёр срок оплаченной подписки: ok=%v err=%v", ok, err)
		}
		if u, _ := st.GetUser(ctx, 9001); u.SubExpireAt != "2099-01-01T00:00:00Z" {
			t.Fatalf("срок оплаченной подписки потерян: %q", u.SubExpireAt)
		}
		// Нулевой потолок выключает возврат целиком.
		if got := ids(0); len(got) != 0 {
			t.Fatalf("нулевой потолок вернул кандидатов: %v", got)
		}

		// Возврат денег не делает клиента «никогда не платившим»: иначе схема
		// «купил, вернул, снова бесплатные триалы» была бы открыта.
		mk(9007, expired, expired)
		if err := st.AddPayment(ctx, &model.Payment{
			TelegramID: 9007, Method: model.PayMethodYooKassa, Months: 1,
			Amount: "150", Status: model.PaymentRefunded,
		}); err != nil {
			t.Fatal(err)
		}
		// Недоступный в Telegram: единственный смысл возврата — позвать
		// человека, а письмо ему не дойдёт, и повтор сгорел бы молча.
		mk(9008, expired, expired)
		if err := st.SetUnreachable(ctx, 9008, "2020-02-02T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		got = ids(5)
		if _, has := got[9007]; has {
			t.Fatalf("вернувший деньги попал в кандидаты: %v", got)
		}
		if _, has := got[9008]; has {
			t.Fatalf("недоступный в telegram попал в кандидаты: %v", got)
		}

		// Отсечка живёт в SQL: иначе те, кому возврат не положен, навсегда
		// закрывали бы очередь собой.
		for i := int64(9100); i < 9110; i++ {
			mk(i, expired, expired)
		}
		if got := idsLim(5, 5); len(got) != 5 {
			t.Fatalf("отсечка пачки не применена: %d", len(got))
		}
	})
}

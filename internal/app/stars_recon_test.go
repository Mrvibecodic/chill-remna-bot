package app

import (
	"context"
	"testing"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
)

// starTx — строка истории звёздных операций: входящая оплата.
func starTx(id string, payer int64, amount int, payload string) models.StarTransaction {
	return models.StarTransaction{
		ID: id, Amount: amount,
		Source: &models.TransactionPartner{User: &models.TransactionPartnerUser{
			TransactionType: "invoice_payment",
			User:            models.User{ID: payer},
			InvoicePayload:  payload,
		}},
	}
}

// Оплата, апдейт о которой не дожил до выдачи, догоняется сверкой.
// Telegram подтверждает получение апдейта сразу, а выдача ходит в панель
// секундами — перезапуск в это окно раньше убивал оплату навсегда.
func TestStarsRecon_CatchesLostPayment(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const uid int64 = 555
	_ = fs.UpsertUser(ctx, uid)
	a.botCfg.Stars = model.StarsConfig{Enabled: true}
	a.botCfg.Pricing.Stars = map[int]int{1: 100}
	fm := &fakeMsg{starTx: []models.StarTransaction{starTx("chg-1", uid, 100, "stars:1")}}
	a.msg = fm

	a.reconcileStars(ctx)

	if done, _ := fs.PaymentByExtID(ctx, "chg-1"); !done {
		t.Fatal("потерянная оплата не догнана сверкой")
	}
	if patched == nil {
		t.Fatal("подписка не выдана")
	}

	// Повторный проход не выдаёт второй раз.
	before := len(fs.pays)
	a.reconcileStars(ctx)
	if len(fs.pays) != before {
		t.Fatalf("повторный проход выдал ещё раз: было %d, стало %d", before, len(fs.pays))
	}
}

// Возврат, о котором апдейт не дошёл, подхватывается тем же проходом.
func TestStarsRecon_MarksRefund(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const uid int64 = 555
	a.botCfg.Stars = model.StarsConfig{Enabled: true}
	_ = fs.AddPayment(ctx, &model.Payment{
		TelegramID: uid, Method: model.PayMethodStars, Months: 1,
		Amount: "100 ⭐", Status: model.PaymentPaid, ExtID: "chg-2",
	})
	a.msg = &fakeMsg{starTx: []models.StarTransaction{{
		ID: "chg-2", Amount: -100,
		Receiver: &models.TransactionPartner{User: &models.TransactionPartnerUser{User: models.User{ID: uid}}},
	}}}

	if paid, _ := fs.HasPaidPayment(ctx, uid); !paid {
		t.Fatal("платёж не записан")
	}
	a.reconcileStars(ctx)
	if paid, _ := fs.HasPaidPayment(ctx, uid); paid {
		t.Fatal("возврат не помечен: платёж всё ещё считается оплаченным")
	}
}

// Админ может вернуть звёзды по застрявшей оплате.
func TestStarsRefund_ByAdmin(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	const uid int64 = 555
	_ = fs.AddPayment(ctx, &model.Payment{
		TelegramID: uid, Method: model.PayMethodStars, Months: 1,
		Amount: "100 ⭐", Status: model.PaymentPaid, ExtID: "chg-3",
	})

	a.handleCallback(ctx, cb(planAdmin, "star:refund:555:chg-3"))

	if len(fm.refunds) != 1 || fm.refunds[0] != "555:chg-3" {
		t.Fatalf("возврат не запрошен у Telegram: %v", fm.refunds)
	}
	if paid, _ := fs.HasPaidPayment(ctx, uid); paid {
		t.Fatal("после возврата платёж всё ещё оплачен")
	}
}

// Ссылку-счёт можно переслать, но чужой плательщик проходит те же гейты.
func TestStarsPayload_CarriesAddressee(t *testing.T) {
	months, forID := starsPayload("stars:3:777")
	if months != 3 || forID != 777 {
		t.Fatalf("разбор нового формата: %d, %d", months, forID)
	}
	// Старый формат из переписки продолжает работать.
	months, forID = starsPayload("stars:1")
	if months != 1 || forID != 0 {
		t.Fatalf("разбор старого формата: %d, %d", months, forID)
	}
	if m, _ := starsPayload("мусор"); m != 0 {
		t.Fatal("чужой payload не должен разбираться")
	}
}

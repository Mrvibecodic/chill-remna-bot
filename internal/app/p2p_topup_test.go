package app

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"remnabot/internal/config"
	"remnabot/internal/model"
)

func p2pTopUpApp(t *testing.T, openForAll bool) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		wiz:   map[int64]*wizard{},
		ui:    map[int64]*uiState{},
		store: fs,
	}
	a.botCfg = &model.BotConfig{
		Installed: true, Language: "ru",
		Pricing: model.Pricing{Currency: "₽", Base: map[int]string{1: "150", 12: "1400"}},
		Wallet:  model.WalletConfig{TopUp: true, Init: true},
		P2P:     model.P2PConfig{Enabled: true, OpenForAll: openForAll, Cards: []string{"CARD-1"}, Prices: map[int]string{1: "150"}},
	}
	a.botCfg.NormalizePricing()
	return a, fm, fs
}

func lastReq(fs *fakeStore) *model.P2PRequest {
	var last *model.P2PRequest
	for _, r := range fs.reqs {
		if last == nil || r.ID > last.ID {
			last = r
		}
	}
	return last
}

// Весь путь в чате: реквизиты → чек → правка суммы админом → одобрение
// зачисляет исправленную сумму, подписку не выдаёт, повтор не начисляет.
func TestP2PTopUp_ChatFlowWithAmountChange(t *testing.T) {
	a, fm, fs := p2pTopUpApp(t, true)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)

	a.getUI(u).topUpKopecks = 50000
	a.showTopUpMethods(ctx, u)
	if !hasCB(fm.cbData, "top:m:p2p") {
		t.Fatalf("нет кнопки перевода в пополнении: %v", fm.cbData)
	}
	a.handleCallback(ctx, cb(u, "top:m:p2p"))
	r := lastReq(fs)
	if r == nil || !r.IsTopUp() || r.Kopecks != 50000 || r.Months != 0 || r.Card != "CARD-1" {
		t.Fatalf("заявка на пополнение: %+v", r)
	}
	if !strings.Contains(fm.joined(), "Пополнение баланса") || !strings.Contains(fm.joined(), "CARD-1") {
		t.Fatalf("реквизиты не показаны:\n%s", fm.joined())
	}
	id := strconv.FormatInt(r.ID, 10)

	a.handleCallback(ctx, cb(u, "p2p:paid:"+id))
	a.handlePhoto(ctx, photoMsg(u, "file_1"))
	if got, _ := fs.GetP2PRequest(ctx, r.ID); got == nil || got.Status != model.P2PSubmitted {
		t.Fatalf("чек не принят: %+v", got)
	}
	if !strings.Contains(fm.joined(), "Зачислим деньги на баланс") {
		t.Fatalf("клиенту не тот ответ на чек:\n%s", fm.joined())
	}
	if !strings.Contains(fm.joined(), "Пополнение баланса переводом") || !hasCB(fm.cbData, "adm:pamt:"+id) {
		t.Fatalf("админу не пришла карточка пополнения с правкой суммы:\n%s\n%v", fm.joined(), fm.cbData)
	}

	a.handleCallback(ctx, cb(100, "adm:pamt:"+id))
	a.handleMessage(ctx, msgText(100, "abc"))
	if got, _ := fs.GetP2PRequest(ctx, r.ID); got.Kopecks != 50000 {
		t.Fatalf("неверная сумма изменила заявку: %+v", got)
	}
	a.handleMessage(ctx, msgText(100, "449,90"))
	if got, _ := fs.GetP2PRequest(ctx, r.ID); got.Kopecks != 44990 || got.Price != "449.90" || got.Status != model.P2PSubmitted {
		t.Fatalf("сумма не исправлена: %+v", got)
	}

	for i := 0; i < 2; i++ {
		a.handleCallback(ctx, cb(100, "adm:pok:"+id))
	}
	if got, _ := fs.GetUser(ctx, u); got == nil || got.Balance != 44990 {
		t.Fatalf("на баланс должна прийти исправленная сумма один раз: %+v", got)
	}
	if got, _ := fs.GetP2PRequest(ctx, r.ID); got.Status != model.P2PApproved {
		t.Fatalf("заявка не одобрена: %+v", got)
	}
	for _, p := range fs.pays {
		if p.TelegramID == u && p.Months > 0 {
			t.Fatalf("пополнение записано покупкой подписки: %+v", p)
		}
	}
}

// Допуск общий: без одобрения реквизитов нет, после одобрения «для оплаты»
// пополнение открывается само.
func TestP2PTopUp_SharedApproval(t *testing.T) {
	a, fm, fs := p2pTopUpApp(t, false)
	ctx := context.Background()
	const u int64 = 556
	_ = fs.UpsertUser(ctx, u)

	a.getUI(u).topUpKopecks = 30000
	a.handleCallback(ctx, cb(u, "top:m:p2p"))
	if strings.Contains(fm.joined(), "CARD-1") || len(fs.reqs) != 0 {
		t.Fatalf("реквизиты выданы без одобрения:\n%s", fm.joined())
	}
	if !hasCB(fm.cbData, "adm:uok:556") {
		t.Fatalf("админа не позвали одобрить: %v", fm.cbData)
	}
	a.handleCallback(ctx, cb(100, "adm:uok:556"))
	a.handleCallback(ctx, cb(u, "top:m:p2p"))
	if r := lastReq(fs); r == nil || !r.IsTopUp() || r.Kopecks != 30000 {
		t.Fatalf("после одобрения пополнение не открылось: %+v\n%s", r, fm.joined())
	}
}

// Покупка и пополнение не подменяют друг друга: деньги за одно не должны
// уйти в другое.
func TestP2PTopUp_OpenRequestOfOtherKindIsNotReused(t *testing.T) {
	ctx := context.Background()
	const u int64 = 557

	// Висит покупка → пополнение не создаётся.
	a, fm, fs := p2pTopUpApp(t, true)
	_ = fs.UpsertUser(ctx, u)
	_ = fs.CreateP2PRequest(ctx, &model.P2PRequest{TelegramID: u, Months: 1, Price: "150", Status: model.P2PAwaiting, Card: "CARD-1"})
	a.getUI(u).topUpKopecks = 30000
	a.handleCallback(ctx, cb(u, "top:m:p2p"))
	if len(fs.reqs) != 1 || lastReq(fs).IsTopUp() {
		t.Fatalf("пополнение подменило заявку на покупку: %+v", lastReq(fs))
	}
	if !strings.Contains(fm.joined(), "незакрытая заявка") {
		t.Fatalf("клиенту не сказали про незакрытую заявку:\n%s", fm.joined())
	}
	if r := a.MiniTopUp(ctx, u, 30000, model.PayMethodP2P, false); r.OK || r.P2PCard != "" {
		t.Fatalf("мини-апп выдал реквизиты поверх покупки: %+v", r)
	}

	// Висит пополнение → покупка его не подхватывает.
	a, fm, fs = p2pTopUpApp(t, true)
	_ = fs.UpsertUser(ctx, u)
	_ = fs.CreateP2PRequest(ctx, &model.P2PRequest{TelegramID: u, Price: "300", Status: model.P2PAwaiting, Card: "CARD-1",
		Purpose: model.P2PPurposeTopUp, Kopecks: 30000})
	if _, _, _, err := a.prepareP2PCard(ctx, u, 1); err == nil {
		t.Fatal("покупка подхватила заявку на пополнение")
	}
	a.handleCallback(ctx, cb(u, "buy:1"))
	a.handleCallback(ctx, cb(u, "method:p2p"))
	if len(fs.reqs) != 1 || !lastReq(fs).IsTopUp() {
		t.Fatalf("покупка создала вторую заявку: %d", len(fs.reqs))
	}
	if !strings.Contains(fm.joined(), "незакрытая заявка") {
		t.Fatalf("клиенту не сказали про незакрытую заявку:\n%s", fm.joined())
	}
}

// Мини-апп и кабинет: реквизиты на странице, чек загрузкой, карточка админу —
// пополнения. E-mail-аккаунту — только после ручного одобрения.
func TestP2PTopUp_MiniAndCabinet(t *testing.T) {
	a, fm, fs := p2pTopUpApp(t, true)
	ctx := context.Background()
	const u int64 = 558
	_ = fs.UpsertUser(ctx, u)

	if got := a.MiniTopUpOptions(ctx, u).Methods; len(got) == 0 || got[0] != model.PayMethodP2P {
		t.Fatalf("мини-апп не предлагает перевод: %v", got)
	}
	r := a.MiniTopUp(ctx, u, 25000, model.PayMethodP2P, true)
	if !r.OK || r.P2PCard != "CARD-1" || r.P2PAmount != "250 ₽" || r.P2PReqID == 0 {
		t.Fatalf("реквизиты: %+v", r)
	}
	png := []byte("\x89PNG\r\n\x1a\n0000000000000000")
	if err := a.CabinetP2PScreenshot(ctx, u, r.P2PReqID, "r.png", png); err != nil {
		t.Fatalf("чек: %v", err)
	}
	if !strings.Contains(fm.joined(), "Пополнение баланса переводом") {
		t.Fatalf("админу не карточка пополнения:\n%s", fm.joined())
	}

	const email int64 = -1000000001
	_ = fs.UpsertUser(ctx, email)
	if r := a.MiniTopUp(ctx, email, 25000, model.PayMethodP2P, true); r.OK || r.P2PCard != "" {
		t.Fatalf("e-mail-аккаунт получил реквизиты без одобрения: %+v", r)
	}
	_ = fs.SetP2PApproved(ctx, email, true)
	if r := a.MiniTopUp(ctx, email, 25000, model.PayMethodP2P, true); !r.OK || r.P2PCard == "" {
		t.Fatalf("одобренный e-mail-аккаунт не получил реквизиты: %+v", r)
	}
}

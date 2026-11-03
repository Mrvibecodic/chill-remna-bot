package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func TestCopyableText(t *testing.T) {
	cases := []struct {
		name, text string
		want       bool
	}{
		{"обычный экран", "💼 <b>Кошелёк</b>\n\nНа счету: <b>1 500 ₽</b>", false},
		{"дата и время", "Подписка активна до 01.12.2030 03:00 МСК", false},
		{"премиум-эмодзи", `<tg-emoji emoji-id="5368324170671202286">👋</tg-emoji> Привет`, false},
		{"код", "Ваша ссылка:\n<code>abc</code>", true},
		{"ссылка тегом", `<a href="https://example.com">тут</a>`, true},
		{"адрес текстом", "Пишите на help.example.com", true},
		{"ссылка текстом", "https://sub.example.com/x", true},
		{"ник", "Поддержка: @support_bot", true},
		{"карта с пробелами", "Карта: 2200 1234 5678 9012", true},
		{"телефон", "СБП: +7 (999) 123-45-67", true},
		{"длинное число", "ID 1000000001", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := copyableText(c.text); got != c.want {
				t.Fatalf("copyableText(%q) = %v, ждали %v", c.text, got, c.want)
			}
		})
	}
}

func protectApp(t *testing.T) (*App, *fakeMsg) {
	t.Helper()
	srv := fullPanel(t)
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
	a.botCfg = fullyConfigured()
	a.botCfg.Referral = model.ReferralConfig{Enabled: true, BonusKind: "days", BonusValue: 3, Init: true}
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	_ = fs.UpsertUser(context.Background(), protUser)
	return a, fm
}

const protUser = int64(1000000001)

// cbUnder — нажатие кнопки под сообщением msgID, закрытым или открытым.
func cbUnder(uid int64, data string, msgID int, prot bool) *models.CallbackQuery {
	q := cb(uid, data)
	q.Message = models.MaybeInaccessibleMessage{Message: &models.Message{ID: msgID, HasProtectedContent: prot}}
	return q
}

// Экраны пользователя без того, что нужно копировать, уходят закрытыми;
// экраны со ссылками, админка и уведомления — открытыми.
func TestUserScreensProtect(t *testing.T) {
	ctx := context.Background()
	a, fm := protectApp(t)

	tap := func(uid int64, data string) int {
		t.Helper()
		n := fm.seq
		a.handleCallback(ctx, cb(uid, data))
		if fm.seq == n {
			t.Fatalf("%s: экран не пришёл", data)
		}
		return fm.seq
	}
	if id := tap(protUser, "menu:home"); !fm.isProt(id) {
		t.Fatal("главное меню пользователя ушло без запрета пересылки")
	}
	if id := tap(protUser, "menu:topup"); !fm.isProt(id) {
		t.Fatal("экран пополнения ушёл без запрета пересылки")
	}
	if id := tap(protUser, "menu:mysubs"); fm.isProt(id) {
		t.Fatal("экран подписки со ссылкой закрыт: ссылку не скопировать")
	}
	if id := tap(protUser, "menu:ref"); fm.isProt(id) {
		t.Fatal("реферальный экран закрыт: ссылку не скопировать")
	}
	if id := tap(100, "menu:home"); fm.isProt(id) {
		t.Fatal("меню админа закрыто, а его чат не трогаем")
	}
	n := fm.seq
	a.notify(ctx, protUser, "уведомление без ссылок")
	if fm.seq == n || fm.isProt(fm.seq) {
		t.Fatal("уведомление ушло с запретом пересылки")
	}
}

// Запрет ставится только при отправке: экран другого режима не правит
// сообщение на месте, а уходит новым.
func TestProtectEditMatchesMode(t *testing.T) {
	ctx := context.Background()
	a, fm := protectApp(t)
	fm.editOK = true

	cases := []struct {
		name       string
		data       string
		targetProt bool
		wantEdit   bool
	}{
		{"закрытый экран под закрытым", "menu:topup", true, true},
		{"закрытый экран под открытым", "menu:topup", false, false},
		{"открытый экран под закрытым", "menu:mysubs", true, false},
		{"открытый экран под открытым", "menu:mysubs", false, true},
		{"текстовый экран под закрытым", "pr:enter", true, true},
		{"текстовый экран под открытым", "pr:enter", false, false},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := 500 + i
			// Раздел экрана совпадает с прошлым: иначе баннер не правится
			// и без запрета.
			a.handleCallback(ctx, cb(protUser, c.data))
			edits, n := len(fm.edits), fm.seq
			a.handleCallback(ctx, cbUnder(protUser, c.data, target, c.targetProt))
			edited := len(fm.edits) > edits && fm.edits[len(fm.edits)-1] == target
			if edited != c.wantEdit {
				t.Fatalf("правка на месте: %v, ждали %v", edited, c.wantEdit)
			}
			if !c.wantEdit && fm.seq == n {
				t.Fatal("экран не ушёл новым сообщением")
			}
		})
	}
}

// Экран согласия с текстом документов остаётся открытым, даже если в тексте
// нет ни ссылок, ни кода.
func TestLegalAcceptScreenOpen(t *testing.T) {
	a, fm := protectApp(t)
	a.botCfg.Legal = model.LegalConfig{Terms: model.LegalDoc{Text: "Сервис оказывается как есть."}, GateBuy: true, Migrated: true}
	n := fm.seq
	a.handleCallback(context.Background(), cb(protUser, "menu:topup"))
	if fm.seq == n {
		t.Fatal("экран согласия не пришёл")
	}
	if !strings.Contains(fm.last(), "как есть") {
		t.Fatalf("пришёл не экран согласия с текстом: %q", fm.last())
	}
	if fm.isProt(fm.seq) {
		t.Fatal("экран согласия с текстом документа закрыт от копирования")
	}
}

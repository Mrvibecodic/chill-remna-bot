package app

import (
	"context"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
)

func kbHas(rows [][]models.InlineKeyboardButton, data string) bool {
	for _, r := range rows {
		for _, b := range r {
			if b.CallbackData == data {
				return true
			}
		}
	}
	return false
}

func privText(uid int64, text string) *models.Message {
	m := msgText(uid, text)
	m.Chat.Type = models.ChatTypePrivate
	return m
}

func previewChecks(t *testing.T, fm *fakeMsg) (client, admin func(string)) {
	client = func(step string) {
		t.Helper()
		if !kbHas(fm.lastKB, "menu:manage") || !kbHas(fm.lastKB, "menu:buy") || kbHas(fm.lastKB, "menu:system") {
			t.Fatalf("%s: не меню клиента: %v", step, fm.lastKB)
		}
		if !strings.Contains(fm.last(), "Превью: так бот видит клиент") {
			t.Fatalf("%s: нет строки превью: %q", step, fm.last())
		}
	}
	admin = func(step string) {
		t.Helper()
		if !kbHas(fm.lastKB, "menu:system") || !kbHas(fm.lastKB, "menu:preview") || kbHas(fm.lastKB, "menu:manage") {
			t.Fatalf("%s: не меню админки: %v", step, fm.lastKB)
		}
	}
	return client, admin
}

func TestAdminPreview_HomeStaysClient(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	client, admin := previewChecks(t, fm)

	a.handleCallback(ctx, cb(planAdmin, "menu:home"))
	admin("админка")
	a.handleCallback(ctx, cb(planAdmin, "menu:preview"))
	client("вход в превью")
	for _, step := range []func(){
		func() { a.handleCallback(ctx, cb(planAdmin, "menu:home")) },
		func() { a.handleCallback(ctx, cb(planAdmin, cbClose+":home")) },
		func() { a.handleMessage(ctx, privText(planAdmin, "🏠 Главная")) },
		func() { a.getUI(planAdmin).inputBack = ""; a.handleCallback(ctx, cb(planAdmin, "inp:cancel")) },
		func() { a.handleCallback(ctx, cb(planAdmin, "p2p:cancel:0")) },
		func() {
			a.handleCallback(ctx, cb(planAdmin, "menu:buy"))
			a.handleCallback(ctx, cb(planAdmin, "menu:home"))
		},
		func() {
			a.handleCallback(ctx, cb(planAdmin, cbClose+":close"))
			a.handleCallback(ctx, cb(planAdmin, "menu:home"))
		},
	} {
		step()
		client("путь «Главная»")
	}
	a.getUI(planAdmin).inputBack = "menu:balance"
	a.handleCallback(ctx, cb(planAdmin, "inp:cancel"))
	if !a.inPreview(planAdmin) {
		t.Fatal("отмена ввода клиента вывела из превью")
	}
	if u, _ := fs.GetUser(ctx, planAdmin); u != nil {
		t.Fatal("админ зарегистрирован как новый пользователь")
	}
	a.handleCallback(ctx, cb(planAdmin, "menu:trial"))
	if kbHas(fm.lastKB, "trial:days") {
		t.Fatal("«Пробный период» в превью открыл настройки триала")
	}
}

func TestAdminPreview_Exits(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	_, admin := previewChecks(t, fm)
	for name, exit := range map[string]func(){
		"«В админку»": func() { a.handleCallback(ctx, cb(planAdmin, "menu:manage")) },
		"/start":      func() { a.handleMessage(ctx, privText(planAdmin, "/start")) },
		"/buy":        func() { a.handleMessage(ctx, privText(planAdmin, "/buy")) },
		"раздел":      func() { a.handleCallback(ctx, cb(planAdmin, "menu:system")) },
		"отмена старого вопроса админки": func() {
			a.getUI(planAdmin).inputBack = "menu:cabinet"
			a.handleCallback(ctx, cb(planAdmin, "inp:cancel"))
		},
	} {
		a.handleCallback(ctx, cb(planAdmin, "menu:preview"))
		exit()
		if a.inPreview(planAdmin) {
			t.Fatalf("%s: превью не закрыто", name)
		}
		a.handleCallback(ctx, cb(planAdmin, "menu:home"))
		admin(name)
	}
}

func TestAdminPreview_InputIsClient(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	ui := a.getUI(planAdmin)
	ui.adminInput, ui.welcomeAwait, ui.awaitBackup = "cab_title", "img", true
	a.handleCallback(ctx, cb(planAdmin, "menu:preview"))

	a.handleMessage(ctx, privText(planAdmin, "Новый заголовок"))
	if !strings.Contains(fm.last(), "ничего не жду") || a.botCfg.Cabinet.Title == "Новый заголовок" {
		t.Fatalf("текст в превью ушёл в ввод админки: %q", fm.last())
	}
	n := len(fm.texts)
	a.handlePhoto(ctx, &models.Message{Chat: models.Chat{ID: planAdmin}, From: &models.User{ID: planAdmin},
		Photo: []models.PhotoSize{{FileID: "pic"}}})
	a.handleDocument(ctx, &models.Message{Chat: models.Chat{ID: planAdmin}, From: &models.User{ID: planAdmin},
		Document: &models.Document{FileID: "f", FileName: "backup.bin"}})
	if len(fm.texts) != n || !ui.awaitBackup || ui.welcomeAwait != "img" || ui.adminInput != "cab_title" {
		t.Fatalf("файлы в превью ушли в ввод админки: %q", fm.texts[n:])
	}

	ui.awaitPromo = true
	a.handleMessage(ctx, privText(planAdmin, "NOSUCHCODE"))
	if ui.awaitPromo || strings.Contains(fm.last(), "ничего не жду") {
		t.Fatalf("промокод в превью не принят как ввод клиента: %q", fm.last())
	}
}

func TestAdminPreview_LegalGate(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	client, _ := previewChecks(t, fm)
	a.botCfg.Legal = model.LegalConfig{Terms: model.LegalDoc{Text: "правила"}, GateStart: true}
	a.handleCallback(ctx, cb(planAdmin, "menu:preview"))
	if !kbHas(fm.lastKB, "terms:accept") {
		t.Fatalf("в превью нет экрана документов: %v", fm.lastKB)
	}
	a.handleCallback(ctx, cb(planAdmin, "terms:accept"))
	client("после согласия")
	a.handleCallback(ctx, cb(planAdmin, "menu:manage"))
	a.handleCallback(ctx, cb(planAdmin, "menu:preview"))
	client("повторный вход после согласия")
}

func TestPreviewMenuShiftsEntities(t *testing.T) {
	a, _, _ := planAdminApp(t)
	ents := []models.MessageEntity{{Type: models.MessageEntityTypeBold, Offset: 0, Length: 5}}
	caption, got, rows := a.previewMenu("ru", "Привет", ents, nil)
	prefix := strings.TrimSuffix(caption, "Привет")
	if got[0].Offset != len(utf16.Encode([]rune(prefix))) || !kbHas(rows, "menu:manage") {
		t.Fatalf("смещение %d при префиксе %q", got[0].Offset, prefix)
	}
}

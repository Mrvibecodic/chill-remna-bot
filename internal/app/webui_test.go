package app

import (
	"context"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
)

// Админка дизайна проверяется через настоящий вход: колбэк и текст ответа.
func TestWebUIAdmin_Settings(t *testing.T) {
	ctx := context.Background()
	a, _ := planApp(t)

	if got := a.WebUI().Design; got != model.WebDesignClassic {
		t.Fatalf("по умолчанию дизайн классический, получено %q", got)
	}
	a.handleCallback(ctx, cb(100, "wui:design"))
	if got := a.WebUI().Design; got != model.WebDesignMinimal {
		t.Fatalf("переключение дизайна: %q", got)
	}
	a.handleCallback(ctx, cb(100, "wui:theme"))
	if got := a.WebUI().Theme; got != model.WebThemeLight {
		t.Fatalf("тема после auto: %q", got)
	}

	a.handleCallback(ctx, cb(100, "wui:accent"))
	a.handleMessage(ctx, msgText(100, "red"))
	if got := a.WebUI().Accent; got != "" {
		t.Fatalf("негодный цвет сохранён: %q", got)
	}
	if a.getUI(100).adminInput != "wui_accent" {
		t.Fatal("после негодного цвета ввод обязан ждать повтора")
	}
	a.handleMessage(ctx, msgText(100, "#ABC"))
	if got := a.WebUI().Accent; got != "#aabbcc" {
		t.Fatalf("цвет: %q", got)
	}

	a.handleCallback(ctx, cb(100, "wui:name"))
	a.handleMessage(ctx, msgText(100, "Acme"))
	if got := a.WebUI().Name; got != "Acme" {
		t.Fatalf("название: %q", got)
	}

	a.handleCallback(ctx, cb(100, "wui:logourl"))
	a.handleMessage(ctx, msgText(100, "javascript:alert(1)"))
	if got := a.WebUI().Logo; got != "" {
		t.Fatalf("ссылка не https сохранена: %q", got)
	}
	a.handleMessage(ctx, msgText(100, "https://example.com/logo.svg"))
	if got := a.WebUI().Logo; got != "https://example.com/logo.svg" {
		t.Fatalf("логотип ссылкой: %q", got)
	}

	// Не админ настройки не меняет.
	a.handleCallback(ctx, cb(555, "wui:design"))
	if got := a.WebUI().Design; got != model.WebDesignMinimal {
		t.Fatalf("колбэк не админа сработал: %q", got)
	}
}

// Логотип загружается файлом: скачивается, проверяется и отдаётся веб-серверу.
func TestWebUIAdmin_LogoUpload(t *testing.T) {
	ctx := context.Background()
	a, _ := planApp(t)
	fm := a.msg.(*fakeMsg)
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01")
	fm.downloads = map[string][]byte{"good": png, "bad": []byte("<html>no</html>")}
	doc := func(id string) *models.Message {
		return &models.Message{From: &models.User{ID: 100}, Chat: models.Chat{ID: 100},
			Document: &models.Document{FileID: id, FileSize: 100, MimeType: "image/png"}}
	}

	a.handleCallback(ctx, cb(100, "wui:logo"))
	a.handleDocument(ctx, doc("bad"))
	if got := a.webUICfg().LogoFile; got != "" {
		t.Fatalf("не картинка сохранена логотипом: %q", got)
	}
	if a.getUI(100).awaitLogo != "light" {
		t.Fatal("после негодного файла бот обязан ждать повтора")
	}
	a.handleDocument(ctx, doc("good"))
	if got := a.webUICfg().LogoFile; got != "good" {
		t.Fatalf("логотип не сохранён: %q", got)
	}
	if src := a.WebUI().Logo; !strings.HasPrefix(src, "/brand/logo?v=") {
		t.Fatalf("адрес загруженного логотипа: %q", src)
	}
	if data, ok := a.BrandLogo(ctx, false); !ok || string(data) != string(png) {
		t.Fatal("логотип не отдаётся")
	}
	if _, ok := a.BrandLogo(ctx, true); ok {
		t.Fatal("тёмного логотипа не загружали")
	}

	// Тёмный — отдельный слот; ссылка перекрывается загрузкой.
	a.handleCallback(ctx, cb(100, "wui:logod"))
	a.handleDocument(ctx, doc("good"))
	if a.webUICfg().LogoDarkFile != "good" || a.WebUI().LogoDark == "" {
		t.Fatal("тёмный логотип не сохранён")
	}
	a.handleCallback(ctx, cb(100, "wui:logoreset"))
	if c := a.webUICfg(); c.LogoFile != "" || c.LogoDarkFile != "" {
		t.Fatal("сброс логотипов не сработал")
	}
}

// Ожидание логотипа снимается уходом с экрана: иначе оно перехватило бы
// баннер раздела, отказ по переводу и любой другой админский ввод.
func TestWebUIAdmin_AwaitLogoClearedOnLeave(t *testing.T) {
	ctx := context.Background()
	a, _ := planApp(t)
	fm := a.msg.(*fakeMsg)
	fm.downloads = map[string][]byte{"pic": []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")}
	a.handleCallback(ctx, cb(100, "wui:logo"))
	a.handleCallback(ctx, cb(100, "menu:home"))
	if a.getUI(100).awaitLogo != "" {
		t.Fatal("ожидание логотипа пережило уход с экрана")
	}
	a.handlePhoto(ctx, &models.Message{From: &models.User{ID: 100}, Chat: models.Chat{ID: 100},
		Photo: []models.PhotoSize{{FileID: "pic", FileSize: 10}}})
	if a.webUICfg().LogoFile != "" {
		t.Fatal("фото после ухода с экрана стало логотипом")
	}
}

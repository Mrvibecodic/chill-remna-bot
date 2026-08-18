package app

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Настройку баннера снимает только отказ Telegram на самой картинке, причём
// негодная ссылка на файл — сразу, а неудачная выкачка — после нескольких
// отказов подряд.
func TestBanner_PhotoErrKind(t *testing.T) {
	perm := []string{
		"bad request, Bad Request: wrong remote file identifier specified: Wrong string length",
		"Bad Request: invalid file id",
		"Bad Request: wrong padding in the string",
	}
	for _, m := range perm {
		if got := photoErrKind(errors.New(m)); got != "id" {
			t.Fatalf("негодный file_id не распознан: %q -> %q", m, got)
		}
	}
	fetch := []string{
		"Bad Request: failed to get HTTP URL content",
		"Bad Request: wrong type of the web page content",
		"Bad Request: IMAGE_PROCESS_FAILED",
	}
	for _, m := range fetch {
		if got := photoErrKind(errors.New(m)); got != "fetch" {
			t.Fatalf("сбой выкачки не распознан: %q -> %q", m, got)
		}
	}
	alien := []string{
		"Forbidden: bot was blocked by the user",
		"context deadline exceeded",
		"Bad Request: message caption is too long",
		"Too Many Requests: retry after 5",
	}
	for _, m := range alien {
		if got := photoErrKind(errors.New(m)); got != "" {
			t.Fatalf("чужой сбой принят за негодную картинку: %q -> %q", m, got)
		}
	}
	if photoErrKind(nil) != "" {
		t.Fatal("nil — не отказ")
	}
}

// Мусор вместо ссылки на баннер не сохраняется: Telegram принял бы его за
// file_id и отказал бы в отправке всего экрана.
func TestBanner_RejectsJunkURL(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	a.botCfg.Welcome.ImageURL = "https://example.com/old.jpg"

	a.handleCallback(ctx, cb(planAdmin, "wel:img"))
	a.handleMessage(ctx, msgText(planAdmin, "фывфыв"))
	if a.botCfg.Welcome.ImageURL != "https://example.com/old.jpg" {
		t.Fatalf("мусор попал в настройку: %q", a.botCfg.Welcome.ImageURL)
	}
	if !strings.Contains(fm.last(), "не похоже на картинку") {
		t.Fatalf("нет подсказки о неверном вводе: %q", fm.last())
	}
	if a.getUI(planAdmin).welcomeAwait != "img" {
		t.Fatal("ожидание картинки не должно сбрасываться на неверном вводе")
	}

	a.handleMessage(ctx, msgText(planAdmin, "example.com/banner.jpg"))
	if a.botCfg.Welcome.ImageURL != "https://example.com/banner.jpg" {
		t.Fatalf("нормальная ссылка не сохранена: %q", a.botCfg.Welcome.ImageURL)
	}

	a.handleCallback(ctx, cb(planAdmin, "wel:img"))
	a.handleMessage(ctx, msgText(planAdmin, "-"))
	if a.botCfg.Welcome.ImageURL != "" || a.botCfg.Welcome.ImageFileID != "" {
		t.Fatalf("«-» должно возвращать встроенную картинку: %+v", a.botCfg.Welcome)
	}
}

// Битая картинка в настройках не оставляет пользователя без меню: экран уходит
// на встроенном баннере, а настройка снимается.
func TestBanner_BrokenImageFallsBack(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	uid := int64(601)
	_ = fs.UpsertUser(ctx, uid)
	a.botCfg.Welcome.ImageFileID = "сломанный-id"
	fm.failBanner = "сломанный-id"

	a.handleMessage(ctx, msgText(uid, "/start"))
	if fm.last() == "" {
		t.Fatal("меню не отправлено вовсе")
	}
	if a.botCfg.Welcome.ImageFileID != "" {
		t.Fatalf("негодная картинка осталась в настройках: %q", a.botCfg.Welcome.ImageFileID)
	}
	if !liveHas(fm, "не принял вашу картинку") {
		t.Fatal("администратору не сообщили о снятой картинке")
	}
}

// Если не уходит вообще ничего (сеть, бот заблокирован), настройку не трогаем.
func TestBanner_KeepsImageWhenNothingSends(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	uid := int64(602)
	_ = fs.UpsertUser(ctx, uid)
	a.botCfg.Welcome.ImageFileID = "живой-id"
	fm.failAllBanners = true

	a.handleMessage(ctx, msgText(uid, "/start"))
	if a.botCfg.Welcome.ImageFileID != "живой-id" {
		t.Fatalf("картинку сняли из-за общего сбоя отправки: %q", a.botCfg.Welcome.ImageFileID)
	}
	if fm.last() == "" {
		t.Fatal("экран должен уйти хотя бы текстом")
	}
}

// Текст вместо картинки баннера раздела — подсказка, а не тишина.
func TestBanner_SectionNeedsPhoto(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)

	a.getUI(planAdmin).awaitSectionBanner = "main_menu"
	a.handleMessage(ctx, msgText(planAdmin, "картинка"))
	if !strings.Contains(fm.last(), "Нужна картинка") {
		t.Fatalf("нет подсказки: %q", fm.last())
	}
	if a.getUI(planAdmin).awaitSectionBanner == "" {
		t.Fatal("ожидание картинки раздела не должно сбрасываться")
	}
}

// Незакрытое ожидание картинки раздела не должно съедать чужой ввод.
func TestBanner_SectionWaitDoesNotEatOtherInput(t *testing.T) {
	ctx := context.Background()
	a, _, _ := planAdminApp(t)

	a.getUI(planAdmin).awaitSectionBanner = "main_menu"
	a.handleCallback(ctx, cb(planAdmin, "wel:txt"))
	a.handleMessage(ctx, msgText(planAdmin, "Привет!"))
	if a.botCfg.Welcome.Text != "Привет!" {
		t.Fatalf("текст приветствия потерян: %q", a.botCfg.Welcome.Text)
	}

	// Ввод поля тарифа тоже важнее подсказки про картинку.
	a.getUI(planAdmin).awaitSectionBanner = "main_menu"
	a.handleCallback(ctx, cb(planAdmin, "menu:home"))
	if a.getUI(planAdmin).awaitSectionBanner != "" {
		t.Fatal("возврат на главную должен закрывать ожидание картинки")
	}
}

// Разовая недоступность сайта с картинкой не должна стирать ссылку: настройка
// снимается только после нескольких отказов подряд.
func TestBanner_TransientFetchKeepsImage(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	uid := int64(603)
	_ = fs.UpsertUser(ctx, uid)
	a.botCfg.Welcome.ImageURL = "https://example.com/banner.jpg"
	fm.failBanner = "https://example.com/banner.jpg"
	fm.failBannerErr = "Bad Request: failed to get HTTP URL content"

	for i := 0; i < bannerFailLimit-1; i++ {
		a.handleMessage(ctx, msgText(uid, "/start"))
		if a.botCfg.Welcome.ImageURL == "" {
			t.Fatalf("ссылку сняли после %d отказа — должно быть после %d", i+1, bannerFailLimit)
		}
	}
	a.handleMessage(ctx, msgText(uid, "/start"))
	if a.botCfg.Welcome.ImageURL != "" {
		t.Fatal("после серии отказов ссылку надо снять")
	}
}

// Удачная отправка обнуляет счётчик: разовые сбои не копятся вечно.
func TestBanner_SuccessResetsFailCounter(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	uid := int64(604)
	_ = fs.UpsertUser(ctx, uid)
	a.botCfg.Welcome.ImageURL = "https://example.com/banner.jpg"
	fm.failBanner = "https://example.com/banner.jpg"
	fm.failBannerErr = "Bad Request: failed to get HTTP URL content"

	a.handleMessage(ctx, msgText(uid, "/start"))
	fm.failBanner = ""
	a.handleMessage(ctx, msgText(uid, "/start"))
	fm.failBanner = "https://example.com/banner.jpg"
	for i := 0; i < bannerFailLimit-1; i++ {
		a.handleMessage(ctx, msgText(uid, "/start"))
	}
	if a.botCfg.Welcome.ImageURL == "" {
		t.Fatal("счётчик не обнулился после удачной отправки")
	}
}

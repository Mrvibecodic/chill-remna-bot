package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

func searchApp(t *testing.T) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	a, msg, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	return a, msg, fs
}

// Поиск по нику: админ жмёт кнопку, присылает запрос, получает выдачу с
// кнопкой на карточку найденного.
func TestUserSearch_ByUsername(t *testing.T) {
	a, msg, fs := searchApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 1001)
	_ = fs.SetUserInfo(ctx, 1001, "PetrovIvan", "Иван")
	_ = fs.UpsertUser(ctx, 1002)
	_ = fs.SetUserInfo(ctx, 1002, "sidorov", "Пётр")

	a.handleCallback(ctx, cb(100, "usr:find"))
	if a.getUI(100).adminInput != "user_find" {
		t.Fatal("ввод поискового запроса не запрошен")
	}
	a.handleMessage(ctx, msgText(100, "petrov"))

	var found, extra bool
	for _, d := range msg.cbData {
		switch d {
		case "usr:view:1001":
			found = true
		case "usr:view:1002":
			extra = true
		}
	}
	if !found {
		t.Fatalf("совпадение не показано: %v", msg.cbData)
	}
	if extra {
		t.Fatalf("в выдачу попал посторонний: %v", msg.cbData)
	}
}

// Чистый Telegram ID существующего пользователя открывает карточку сразу.
func TestUserSearch_ExactIDOpensCard(t *testing.T) {
	a, msg, fs := searchApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 2002)

	a.handleCallback(ctx, cb(100, "usr:find"))
	a.handleMessage(ctx, msgText(100, "2002"))

	var hasBlock bool
	for _, d := range msg.cbData {
		if strings.HasPrefix(d, "usr:block:2002") {
			hasBlock = true
		}
	}
	if !hasBlock {
		t.Fatalf("карточка не открыта: %v", msg.cbData)
	}
}

// «Назад» из карточки возвращает в выдачу поиска, а не на первую страницу
// полного списка — иначе найденный человек стоил бы нового поиска.
func TestUserSearch_BackReturnsToResults(t *testing.T) {
	a, msg, fs := searchApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 3003)
	_ = fs.SetUserInfo(ctx, 3003, "targetnick", "Цель")

	a.handleCallback(ctx, cb(100, "usr:find"))
	a.handleMessage(ctx, msgText(100, "targetnick"))
	msg.cbData = nil
	a.handleCallback(ctx, cb(100, "usr:view:3003"))

	var back string
	for _, d := range msg.cbData {
		if strings.HasPrefix(d, "usr:fpage:") || d == "usr:list" {
			back = d
		}
	}
	if back != "usr:fpage:0" {
		t.Fatalf("«Назад» ведёт в %q, ожидалась выдача поиска", back)
	}
}

// Пустая выдача не молчит: админ видит, что не нашлось, и может искать снова.
func TestUserSearch_EmptyResult(t *testing.T) {
	a, msg, fs := searchApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 4004)

	a.handleCallback(ctx, cb(100, "usr:find"))
	a.handleMessage(ctx, msgText(100, "такогонет"))

	var again bool
	for _, d := range msg.cbData {
		if d == "usr:find" {
			again = true
		}
	}
	if !again {
		t.Fatalf("на пустой выдаче нет кнопки повторного поиска: %v", msg.cbData)
	}
}

// Один символ «@» — не запрос: раньше он превращался в пустую подстроку и
// выдавал всю базу.
func TestUserSearch_AtSignIsNotEveryone(t *testing.T) {
	a, msg, fs := searchApp(t)
	ctx := context.Background()
	for _, id := range []int64{5001, 5002} {
		_ = fs.UpsertUser(ctx, id)
	}
	a.handleCallback(ctx, cb(100, "usr:find"))
	a.handleMessage(ctx, msgText(100, "@"))
	for _, d := range msg.cbData {
		if strings.HasPrefix(d, "usr:view:") {
			t.Fatalf("пустой запрос выдал пользователей: %v", msg.cbData)
		}
	}
}

// Запрос уходит в HTML-сообщение: спецсимволы обязаны экранироваться, иначе
// Telegram отвергает сообщение целиком и экран не появляется.
func TestUserSearch_QueryIsEscaped(t *testing.T) {
	a, msg, fs := searchApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 5003)

	a.handleCallback(ctx, cb(100, "usr:find"))
	a.handleMessage(ctx, msgText(100, "R&D <тест>"))
	var seen bool
	for _, txt := range msg.texts {
		if strings.Contains(txt, "R&amp;D") {
			seen = true
		}
		if strings.Contains(txt, "<тест>") {
			t.Fatalf("сырой ввод попал в HTML: %q", txt)
		}
	}
	if !seen {
		t.Fatalf("экран поиска не показан: %v", msg.texts)
	}
}

// «Снять доступ у всех» обязано чистить и предзаполненные ID: иначе человек
// вернёт себе доступ при первом же входе.
func TestWhitelist_ClearAlsoDropsPrefilledIDs(t *testing.T) {
	a, _, fs := searchApp(t)
	ctx := context.Background()
	if err := fs.AddWhitelistID(ctx, 6001); err != nil {
		t.Fatal(err)
	}
	a.handleCallback(ctx, cb(100, "usr:wlclearok"))
	if ok, _ := fs.IsWhitelistID(ctx, 6001); ok {
		t.Fatal("предзаполненный ID пережил снятие доступа у всех")
	}
}

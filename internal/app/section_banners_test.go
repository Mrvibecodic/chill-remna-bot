package app

import (
	"context"
	"testing"

	"remnabot/internal/assets"
)

// В «Баннерах разделов» — все разделы клиента с картинкой, и у каждого
// открывается своя карточка.
func TestSectionBannersListsClientScreens(t *testing.T) {
	a, _ := planApp(t)
	fm := a.msg.(*fakeMsg)
	ctx := context.Background()

	a.handleCallback(ctx, cb(planAdmin, "menu:welcome_sections"))
	for _, key := range []string{
		assets.SectionBuySubscription, assets.SectionMySubscription,
		assets.SectionReferral, assets.SectionPromoCode,
	} {
		if !hasCB(fm.allCallbackData(), "sec:open:"+key) {
			t.Fatalf("в списке нет раздела %q: %v", key, fm.allCallbackData())
		}
		fm.cbData = nil
		a.handleCallback(ctx, cb(planAdmin, "sec:open:"+key))
		if !hasCB(fm.allCallbackData(), "sec:upload:"+key) {
			t.Fatalf("у раздела %q нет кнопки загрузки: %v", key, fm.allCallbackData())
		}
		a.handleCallback(ctx, cb(planAdmin, "menu:welcome_sections"))
	}
}

// Картинка раздела уходит на все его экраны клиента и на раздел админки с той
// же встроенной картинкой: у администратора и клиента она одна.
func TestSectionBannerOnClientAndAdminScreens(t *testing.T) {
	const uid int64 = 1000000001
	cases := []struct {
		section string
		client  []string
		admin   []string
	}{
		{assets.SectionReferral, []string{"menu:ref"}, []string{"menu:users"}},
		{assets.SectionPromoCode, []string{"menu:promo"}, []string{"menu:marketing"}},
		{assets.SectionBuySubscription, []string{"menu:balance", "menu:topup", "menu:buy"}, []string{"menu:pay", "menu:buy"}},
		{assets.SectionMySubscription, []string{"menu:mysubs", "menu:autopay"}, nil},
	}
	for _, c := range cases {
		t.Run(c.section, func(t *testing.T) {
			a, fs := planApp(t)
			fm := a.msg.(*fakeMsg)
			ctx := context.Background()
			_ = fs.UpsertUser(ctx, uid)
			if err := fs.SaveMediaFileID(ctx, c.section, "own-picture"); err != nil {
				t.Fatal(err)
			}
			tap := func(who int64, action string) {
				t.Helper()
				n := len(fm.photoIDs)
				a.handleCallback(ctx, cb(who, action))
				if len(fm.photoIDs) != n+1 {
					t.Fatalf("%s: экран ушёл без баннера\n%s", action, fm.joined())
				}
				if got := fm.photoIDs[n]; got != "own-picture" {
					t.Fatalf("%s: экран ушёл не с картинкой раздела: %q", action, got)
				}
			}
			for _, action := range c.client {
				tap(uid, action)
			}
			for _, action := range c.admin {
				tap(planAdmin, action)
			}
		})
	}
}

// Telegram отверг сохранённую картинку клиентского раздела, и бот заменил её
// встроенной: администратор узнаёт об этом. Первая отправка встроенной
// картинки и разделы без своей картинки проходят молча.
func TestRejectedSectionBannerIsReported(t *testing.T) {
	const uid int64 = 1000000001
	cases := []struct {
		name    string
		section string
		saved   string
		who     int64
		action  string
		report  bool
	}{
		{"своя картинка отвергнута", assets.SectionReferral, "own-picture", uid, "menu:ref", true},
		{"первая отправка", assets.SectionReferral, "", uid, "menu:ref", false},
		{"раздел без своей картинки", assets.SectionMainMenu, "cached-builtin", planAdmin, "menu:iface", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, fs := planApp(t)
			fm := a.msg.(*fakeMsg)
			ctx := context.Background()
			_ = fs.UpsertUser(ctx, uid)
			if c.saved != "" {
				if err := fs.SaveMediaFileID(ctx, c.section, c.saved); err != nil {
					t.Fatal(err)
				}
			}
			fm.photoNewID = "builtin-id"
			a.handleCallback(ctx, cb(c.who, c.action))

			if id, _, _ := fs.LoadMediaFileID(ctx, c.section); id != "builtin-id" {
				t.Fatalf("в ячейке раздела осталась негодная картинка: %q", id)
			}
			reported := hasCB(fm.allCallbackData(), "sec:open:"+c.section)
			if reported != c.report {
				t.Fatalf("сообщение администратору: %v, ожидалось %v\n%s", reported, c.report, fm.joined())
			}
			if c.report && countContains(fm.texts, "не принял сохранённую картинку раздела «🎉 Пригласить друга»") != 1 {
				t.Fatalf("нет текста о снятой картинке:\n%s", fm.joined())
			}
		})
	}
}

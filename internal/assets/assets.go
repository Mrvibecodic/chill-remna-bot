package assets

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed sections/*.jpg
var sectionsFS embed.FS

const (
	SectionMainMenu        = "main_menu"
	SectionBuySubscription = "buy_subscription"
	SectionMySubscription  = "my_subscription"
	SectionReferral        = "referral"
	SectionPromoCode       = "promo_code"
	SectionAdminStats      = "admin_stats"
)

type Section struct {
	Key     string
	LabelRU string
	LabelEN string
}

var AllSections = []Section{
	{SectionMainMenu, "🏠 Меню «Интерфейс»", "🏠 Menu «Interface»"},
	{SectionBuySubscription, "🛒 Купить / Оплата", "🛒 Buy / Payments menu"},
	{SectionMySubscription, "📦 Мои подписки", "📦 My subscriptions"},
	{SectionReferral, "👥 Пользователи / реферал", "👥 Users / referral"},
	{SectionPromoCode, "💸 Платежи / промокод", "💸 Payments / promo"},
	{SectionAdminStats, "⚙️ Управление", "⚙️ Manage"},
}

var userFacingSections = map[string]bool{
	SectionBuySubscription: true,
	SectionMySubscription:  true,
}

func UserSections() []Section {
	out := make([]Section, 0, len(userFacingSections))
	for _, s := range AllSections {
		if userFacingSections[s.Key] {
			out = append(out, s)
		}
	}
	return out
}

func LabelByKey(key, lang string) string {
	for _, sec := range AllSections {
		if sec.Key == key {
			if lang == "en" {
				return sec.LabelEN
			}
			return sec.LabelRU
		}
	}
	return key
}

// Has reports whether the section ships with a built-in banner image.
func Has(section string) bool { return len(Bytes(section)) > 0 }

func Bytes(section string) []byte {

	name := "sections/" + strings.TrimSpace(section) + ".jpg"
	data, err := fs.ReadFile(sectionsFS, name)
	if err != nil {
		return nil
	}
	return data
}

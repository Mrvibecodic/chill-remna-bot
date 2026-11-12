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

var userSections = []Section{
	{SectionBuySubscription, "🛒 Покупка и кошелёк", "🛒 Purchase and wallet"},
	{SectionMySubscription, "📲 Мои подписки", "📲 My subscriptions"},
	{SectionReferral, "🎉 Пригласить друга", "🎉 Invite a friend"},
	{SectionPromoCode, "🎟 Промокод", "🎟 Promo code"},
}

// UserSections — разделы с экранами клиента: их картинку администратор
// меняет на свою.
func UserSections() []Section {
	return append([]Section(nil), userSections...)
}

func LabelByKey(key, lang string) string {
	for _, sec := range userSections {
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

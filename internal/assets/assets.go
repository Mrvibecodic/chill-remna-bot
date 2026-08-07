package assets

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed sections/*.jpg
var sectionsFS embed.FS

const (
	SectionWizardWelcome       = "wizard_welcome"
	SectionWizardDBChoose      = "wizard_db_choose"
	SectionWizardDBPostgresUp  = "wizard_db_pg_up"
	SectionWizardLocation      = "wizard_location"
	SectionWizardInstallChoice = "wizard_install_choice"
	SectionWizardToken         = "wizard_token"
	SectionWizardCookie        = "wizard_cookie"
	SectionWizardVerifyOK      = "wizard_verify_ok"

	SectionMainMenu        = "main_menu"
	SectionBuySubscription = "buy_subscription"
	SectionMySubscription  = "my_subscription"
	SectionTrial           = "trial"
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

	{SectionWizardWelcome, "👋 Приветствие мастера", "👋 Wizard welcome"},
	{SectionWizardDBChoose, "🗄 Шаг: выбор БД", "🗄 Step: DB choice"},
	{SectionWizardDBPostgresUp, "🐘 Шаг: PostgreSQL up", "🐘 Step: PostgreSQL up"},
	{SectionWizardLocation, "📍 Шаг: локально/удалённо", "📍 Step: local/remote"},
	{SectionWizardInstallChoice, "🧩 Шаг: способ установки", "🧩 Step: install type"},
	{SectionWizardToken, "🔑 Шаг: API-токен", "🔑 Step: API token"},
	{SectionWizardCookie, "🍪 Шаг: nginx-кука", "🍪 Step: nginx cookie"},
	{SectionWizardVerifyOK, "✅ Шаг: проверка успешна", "✅ Step: verify OK"},

	{SectionMainMenu, "🏠 Меню «Интерфейс»", "🏠 Menu «Interface»"},
	{SectionBuySubscription, "🛒 Купить / Оплата", "🛒 Buy / Payments menu"},
	{SectionMySubscription, "📦 Мои подписки", "📦 My subscriptions"},
	{SectionTrial, "🎁 Триал", "🎁 Trial"},
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

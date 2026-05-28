// Package assets — каталог иллюстраций для разделов бота.
//
// Принцип:
//   - На каждый «раздел» (шаг мастера / экран магазина / админ-карточка) —
//     одна URL-ссылка на Unsplash CDN в формате 2:1 (1280×640): параметры
//     ?w=1280&h=640&fit=crop&auto=format&q=80 кропают любое исходное фото до
//     точных пропорций, поэтому Telegram-карточка получается плоской
//     горизонтальной, а не «портрет 720 в высоту».
//   - В рантайме файл скачивается Telegram'ом ОДИН раз: код вызывает
//     SendPhoto с URL, получает обратно file_id, кладёт его в media_cache
//     (см. storage). На втором и далее вызовах отдаётся file_id —
//     это не требует повторного скачивания.
//
// Атрибуция Unsplash License не обязательна, но в комментариях рядом указано
// короткое описание — чтобы было понятно, что заменять при ребрендинге.
package assets

import (
	"embed"
	"io/fs"
	"strings"
)

// Локальная папка дефолтных картинок 1280×640 — единственный «источник правды»
// для дефолтов. Хочешь поменять дефолт раздела — кладёшь свой .jpg в assets/sections/
// с тем же именем (см. ключи ниже), пересобираешь образ. SectionImages-URL'ы
// остаются как «второй уровень» fallback'а, если бинарь собран без embed-папки
// (например, custom-сборка из обрезанного контекста).
//
//go:embed sections/*.jpg
var sectionsFS embed.FS

// Ключи разделов. Используются и как ключи мапы, и как PK в таблице media_cache.
const (
	// --- Шаги мастера установки ---
	SectionWizardWelcome       = "wizard_welcome"
	SectionWizardDBChoose      = "wizard_db_choose"
	SectionWizardDBPostgresUp  = "wizard_db_pg_up"
	SectionWizardLocation      = "wizard_location"
	SectionWizardInstallChoice = "wizard_install_choice"
	SectionWizardToken         = "wizard_token"
	SectionWizardCookie        = "wizard_cookie"
	SectionWizardVerifyOK      = "wizard_verify_ok"

	// --- Экраны магазина и админки ---
	SectionMainMenu        = "main_menu"
	SectionBuySubscription = "buy_subscription"
	SectionMySubscription  = "my_subscription"
	SectionTrial           = "trial"
	SectionReferral        = "referral"
	SectionPromoCode       = "promo_code"
	SectionAdminStats      = "admin_stats"
)

// SectionImages — карта «раздел → исходный URL картинки 1280×640».
// Все ссылки проверены: возвращают 200 OK на images.unsplash.com / plus.unsplash.com.
// Стиль выдержан тёмный/сине-фиолетовый/неоновый, где это уместно.
var SectionImages = map[string]string{
	// Шаги мастера (8).
	SectionWizardWelcome:       "https://plus.unsplash.com/premium_photo-1674476932936-80a969879ec2?w=1280&h=640&fit=crop&auto=format&q=80", // sci-fi градиент
	SectionWizardDBChoose:      "https://plus.unsplash.com/premium_photo-1661386261378-8ed99f4e37ba?w=1280&h=640&fit=crop&auto=format&q=80", // тёмная серверка
	SectionWizardDBPostgresUp:  "https://images.unsplash.com/photo-1775616788028-ce670411dff7?w=1280&h=640&fit=crop&auto=format&q=80",       // ракета на старте
	SectionWizardLocation:      "https://images.unsplash.com/photo-1778452419724-1f605dc17c46?w=1280&h=640&fit=crop&auto=format&q=80",       // Земля ночью с огнями
	SectionWizardInstallChoice: "https://images.unsplash.com/photo-1518773553398-650c184e0bb3?w=1280&h=640&fit=crop&auto=format&q=80",       // код на мониторе
	SectionWizardToken:         "https://images.unsplash.com/photo-1608390063578-8dcd6c1995e8?w=1280&h=640&fit=crop&auto=format&q=80",       // замок на цепи
	SectionWizardCookie:        "https://images.unsplash.com/photo-1497051788611-2c64812349fa?w=1280&h=640&fit=crop&auto=format&q=80",       // печеньки (юмор про nginx-куку)
	SectionWizardVerifyOK:      "https://images.unsplash.com/photo-1767260408878-4566afa38b9c?w=1280&h=640&fit=crop&auto=format&q=80",       // синий салют успеха

	// Экраны магазина и админки (7).
	SectionMainMenu:        "https://plus.unsplash.com/premium_photo-1733306489269-449d1e8ae119?w=1280&h=640&fit=crop&auto=format&q=80", // 3D щит / firewall
	SectionBuySubscription: "https://images.unsplash.com/photo-1757185389479-6f9c6d02b96d?w=1280&h=640&fit=crop&auto=format&q=80",       // банковские карты
	SectionMySubscription:  "https://images.unsplash.com/photo-1744782211816-c5224434614f?w=1280&h=640&fit=crop&auto=format&q=80",       // дашборд с графиками
	SectionTrial:           "https://images.unsplash.com/photo-1764385827253-3d0a5eb813fe?w=1280&h=640&fit=crop&auto=format&q=80",       // подарок с конфетти
	SectionReferral:        "https://images.unsplash.com/photo-1761075666032-7540b8c58de7?w=1280&h=640&fit=crop&auto=format&q=80",       // сеть связей
	SectionPromoCode:       "https://plus.unsplash.com/premium_photo-1681398745480-151fc6addaaf?w=1280&h=640&fit=crop&auto=format&q=80", // неоновая тележка
	SectionAdminStats:      "https://images.unsplash.com/photo-1745270917233-65e776a47547?w=1280&h=640&fit=crop&auto=format&q=80",       // растущий график
}

// Section — описание раздела для админ-редактора баннеров.
type Section struct {
	Key     string // assets.Section* константа (PK в media_cache)
	LabelRU string // подпись на кнопке (RU)
	LabelEN string // подпись на кнопке (EN)
}

// AllSections — порядок отображения в админ-разделе «Баннеры разделов».
// Сначала мастер установки, потом экраны магазина/админки.
var AllSections = []Section{
	// Мастер установки.
	{SectionWizardWelcome, "👋 Приветствие мастера", "👋 Wizard welcome"},
	{SectionWizardDBChoose, "🗄 Шаг: выбор БД", "🗄 Step: DB choice"},
	{SectionWizardDBPostgresUp, "🐘 Шаг: PostgreSQL up", "🐘 Step: PostgreSQL up"},
	{SectionWizardLocation, "📍 Шаг: локально/удалённо", "📍 Step: local/remote"},
	{SectionWizardInstallChoice, "🧩 Шаг: способ установки", "🧩 Step: install type"},
	{SectionWizardToken, "🔑 Шаг: API-токен", "🔑 Step: API token"},
	{SectionWizardCookie, "🍪 Шаг: nginx-кука", "🍪 Step: nginx cookie"},
	{SectionWizardVerifyOK, "✅ Шаг: проверка успешна", "✅ Step: verify OK"},
	// Экраны магазина и админки.
	{SectionMainMenu, "🏠 Меню «Интерфейс»", "🏠 Menu «Interface»"},
	{SectionBuySubscription, "🛒 Купить / Оплата", "🛒 Buy / Payments menu"},
	{SectionMySubscription, "📦 Мои подписки", "📦 My subscriptions"},
	{SectionTrial, "🎁 Триал", "🎁 Trial"},
	{SectionReferral, "👥 Пользователи / реферал", "👥 Users / referral"},
	{SectionPromoCode, "💸 Платежи / промокод", "💸 Payments / promo"},
	{SectionAdminStats, "⚙️ Управление", "⚙️ Manage"},
}

// LabelByKey ищет человеческую подпись раздела по ключу. Если ключ
// не зарегистрирован — возвращает сам ключ (заметно в UI = надо добавить).
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

// URL возвращает исходную ссылку для раздела. Если раздел не зарегистрирован
// — пустая строка; вызывающий код решает, что показать (например, дефолтный
// баннер или текстовое сообщение без картинки).
func URL(section string) string {
	return SectionImages[section]
}

// Bytes возвращает байты дефолтной картинки раздела из embed-папки assets/sections/.
// Если файла нет (например, ключ не зарегистрирован или картинка не добавлена) —
// возвращает nil. Используется как ПЕРВЫЙ источник дефолта: если есть локальный
// файл — отправляем его, при отсутствии — фолбэк на URL.
func Bytes(section string) []byte {
	// Ключи у нас в snake_case, файл — sections/<key>.jpg.
	name := "sections/" + strings.TrimSpace(section) + ".jpg"
	data, err := fs.ReadFile(sectionsFS, name)
	if err != nil {
		return nil
	}
	return data
}

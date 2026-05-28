package app

import (
	"context"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
)

// --- админ: раздел «Баннеры разделов» ---
//
// Поток UI:
//   1) showSectionBanners — список 15 разделов (по 1 кнопке на строку с
//      понятной подписью), внизу кнопки «Назад» (к showIface) и «Главная».
//   2) showSectionBanner(section) — показывает ТЕКУЩУЮ картинку раздела
//      (из media_cache → file_id, либо дефолтный URL из assets) c подписью
//      и кнопками «📤 Отправить новую» / «↩️ Сбросить к дефолту» / «❌ Отмена».
//   3) При нажатии «Отправить новую» — пишем chatID→section в uiState
//      и просим прислать фото; handlePhoto (p2p.go) распознаёт состояние и
//      зовёт setSectionBannerFile, который сохраняет file_id в media_cache.
//   4) «Сбросить» удаляет запись из media_cache (DeleteMediaFileID);
//      следующая отправка пойдёт по дефолтному URL из assets.SectionImages.

func (a *App) showSectionBanners(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	rows := make([][]models.InlineKeyboardButton, 0, len(assets.AllSections)+2)
	for _, sec := range assets.AllSections {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(assets.LabelByKey(sec.Key, lang), "sec:open:"+sec.Key),
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:iface"),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	})
	a.sendKB(ctx, chatID, i18n.T(lang, "banners.title"), rows)
}

// onSectionBanner — диспетчер callback-ов с префиксом "sec:":
//
//	sec:open:<key>    — показать карточку раздела (текущая картинка + кнопки)
//	sec:upload:<key>  — попросить прислать новое фото
//	sec:reset:<key>   — удалить кэш (вернуть дефолт из assets)
//	sec:cancel:<key>  — отмена ожидания фото, возврат к карточке раздела
func (a *App) onSectionBanner(ctx context.Context, chatID int64, val string) {
	action, key, _ := cut3(val)
	switch action {
	case "open":
		a.showSectionBanner(ctx, chatID, key)
	case "upload":
		a.askSectionBanner(ctx, chatID, key)
	case "reset":
		a.resetSectionBanner(ctx, chatID, key)
	case "cancel":
		a.getUI(chatID).awaitSectionBanner = ""
		a.showSectionBanner(ctx, chatID, key)
	}
}

// cut3 разрезает "action:key" → (action, key, true). Если ":" нет — (val, "", false).
func cut3(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// showSectionBanner отправляет ТЕКУЩУЮ картинку раздела (как у пользователя
// её увидят) с кнопками управления. Если раздела нет в каталоге — мягкий
// fallback на текст с подсказкой.
func (a *App) showSectionBanner(ctx context.Context, chatID int64, section string) {
	lang := a.lang(chatID)
	label := assets.LabelByKey(section, lang)
	url := assets.URL(section)
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "banners.btn_upload"), "sec:upload:"+section)},
		{btn(i18n.T(lang, "banners.btn_reset"), "sec:reset:"+section)},
		{btn(i18n.T(lang, "btn.back"), "menu:welcome_sections"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	}
	caption := i18n.T(lang, "banners.section_caption", label)

	if url == "" {
		// Раздел незарегистрирован — показываем только инструкцию.
		a.sendKB(ctx, chatID, caption, rows)
		return
	}

	// Берём свежую картинку «как у пользователя»: либо закэшированный file_id,
	// либо дефолтный URL. Кэшируем file_id, если он пришёл из URL.
	var cached string
	if a.store != nil {
		if id, ok, _ := a.store.LoadMediaFileID(ctx, section); ok {
			cached = id
		}
	}
	var newFileID string
	embed := assets.Bytes(section)
	a.emit(ctx, chatID, func() int {
		id, nf := a.msg.SendPhotoCacheable(ctx, chatID, cached, embed, url, caption, rows)
		newFileID = nf
		return id
	})
	if a.store != nil && newFileID != "" && newFileID != cached {
		_ = a.store.SaveMediaFileID(ctx, section, newFileID)
	}
}

// askSectionBanner ставит uiState в ожидание фото и просит его прислать.
// Из этого экрана можно вернуться через «Отмена».
func (a *App) askSectionBanner(ctx context.Context, chatID int64, section string) {
	lang := a.lang(chatID)
	a.getUI(chatID).awaitSectionBanner = section
	label := assets.LabelByKey(section, lang)
	a.sendKB(ctx, chatID, i18n.T(lang, "banners.ask_upload", label), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.cancel"), "sec:cancel:"+section)},
	})
}

// setSectionBannerFile принимает file_id присланного админом фото и сохраняет
// его в media_cache. Вызывается из handlePhoto (p2p.go) когда uiState ждёт.
func (a *App) setSectionBannerFile(ctx context.Context, chatID int64, section, fileID string) {
	if a.store == nil {
		return
	}
	if err := a.store.SaveMediaFileID(ctx, section, fileID); err != nil {
		a.send(ctx, chatID, "❌ "+err.Error())
		return
	}
	// Возвращаемся к карточке — там сразу видно новую картинку.
	a.showSectionBanner(ctx, chatID, section)
}

// resetSectionBanner стирает закэшированный file_id; следующий показ раздела
// пойдёт по дефолтному URL из assets.SectionImages, который восстановит
// первичный file_id.
func (a *App) resetSectionBanner(ctx context.Context, chatID int64, section string) {
	if a.store == nil {
		return
	}
	if err := a.store.DeleteMediaFileID(ctx, section); err != nil {
		a.send(ctx, chatID, "❌ "+err.Error())
		return
	}
	a.showSectionBanner(ctx, chatID, section)
}

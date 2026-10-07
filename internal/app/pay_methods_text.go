package app

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Названия и описания способов оплаты: одно название на способ — в кнопке
// чата, в списке способов мини-аппа и кабинета и в уведомлениях; описание
// необязательно.

type payMethodTexts struct {
	name, note, btn string
}

// payMethodOrder — способы в порядке экрана «Способы оплаты».
var payMethodOrder = []string{
	model.PayMethodP2P, model.PayMethodStars, model.PayMethodYooKassa, model.PayMethodCryptoBot,
	model.PayMethodPlatega, model.PayMethodHeleket, model.PayMethodTribute,
}

var payMethodKeys = map[string]payMethodTexts{
	model.PayMethodP2P:       {"method.p2p_name", "method.p2p_note", "method.p2p_btn"},
	model.PayMethodStars:     {"method.stars_name", "method.stars_note", "method.stars_btn"},
	model.PayMethodYooKassa:  {"method.yk_name", "method.yk_note", "method.yk_btn"},
	model.PayMethodCryptoBot: {"method.cb_name", "method.cb_note", "method.cb_btn"},
	model.PayMethodPlatega:   {"method.pl_name", "method.pl_note", "method.pl_btn"},
	model.PayMethodHeleket:   {"method.hl_name", "method.hl_note", "method.hl_btn"},
	model.PayMethodTribute:   {"method.trb_name", "method.trb_note", "method.trb_btn"},
}

// methodBtnKey — шаблон кнопки чата для ключа названия способа ("" — ключ не
// название способа). В шаблоне живёт значок способа (🎁, 💳…): из карточки
// названия к нему ведёт отдельная кнопка, иначе значок не найти.
func methodBtnKey(nameKey string) string {
	for _, k := range payMethodKeys {
		if k.name == nameKey {
			return k.btn
		}
	}
	return ""
}

// methodName — название способа для кнопки чата.
func methodName(lang, method string) string {
	if k, ok := payMethodKeys[method]; ok {
		return i18n.T(lang, k.name)
	}
	return methodLabel(method)
}

// ownMethodName — название, заданное админом ("" — не задано). Там, где до
// этой настройки стояло своё имя (мини-апп, уведомления), оно остаётся, пока
// админ название не поменял.
func ownMethodName(lang, method string) string {
	k, ok := payMethodKeys[method]
	if !ok {
		return ""
	}
	if _, own := i18n.Effective(lang, k.name); !own {
		return ""
	}
	return i18n.T(lang, k.name)
}

// methodNote — описание способа; "" — описания нет.
func methodNote(lang, method string) string {
	if k, ok := payMethodKeys[method]; ok {
		return strings.TrimSpace(i18n.T(lang, k.note))
	}
	return ""
}

// withPayNote добавляет описание способа к экрану оплаты.
func withPayNote(lang, method, text string) string {
	if note := methodNote(lang, method); note != "" {
		return text + "\n\n" + note
	}
	return text
}

// payMethodTexts — названия и описания для мини-аппа и кабинета. Описание
// там показывается обычным текстом, разметка чата снимается. Ключи — и
// полные коды способов, и короткие коды пополнения (yk, cb, hl).
func payMethodWebTexts(lang string) (labels, notes map[string]string) {
	labels, notes = map[string]string{}, map[string]string{}
	short := map[string]string{
		model.PayMethodYooKassa: "yk", model.PayMethodCryptoBot: "cb", model.PayMethodPlatega: "pl", model.PayMethodHeleket: "hl",
	}
	for _, m := range payMethodOrder {
		name := ownMethodName(lang, m)
		note := stripHTMLTags(methodNote(lang, m))
		codes := []string{m}
		if s, ok := short[m]; ok {
			codes = append(codes, s)
		}
		for _, c := range codes {
			if name != "" {
				labels[c] = name
			}
			if note != "" {
				notes[c] = note
			}
		}
	}
	return labels, notes
}

// methodEnabled — способ включён в настройках.
func (a *App) methodEnabled(method string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.botCfg
	if c == nil {
		return false
	}
	switch method {
	case model.PayMethodP2P:
		return c.P2P.Enabled
	case model.PayMethodStars:
		return c.Stars.Enabled
	case model.PayMethodYooKassa:
		return c.YooKassa.Enabled
	case model.PayMethodCryptoBot:
		return c.CryptoBot.Enabled
	case model.PayMethodPlatega:
		return c.Platega.Enabled
	case model.PayMethodHeleket:
		return c.Heleket.Enabled
	case model.PayMethodTribute:
		return c.Tribute.Enabled
	}
	return false
}

// showPayMethodTexts — экран «Способы оплаты: названия и описания». Включённые
// способы сверху.
// from == "t" — пришли из «Тексты бота», иначе — из настроек продаж.
func (a *App) showPayMethodTexts(ctx context.Context, chatID int64, from string) {
	lang := a.lang(chatID)
	bl := a.botLang()
	back := "menu:pay"
	if from == "t" {
		back = "tx:home"
	}
	a.getUI(chatID).txtBack = "tx:pm:" + from
	var on, off [][]models.InlineKeyboardButton
	for _, m := range payMethodOrder {
		k := payMethodKeys[m]
		label := methodName(bl, m)
		enabled := a.methodEnabled(m)
		if enabled {
			label = "✅ " + label
		}
		noteBtn := i18n.T(lang, "tx.btn_note")
		if methodNote(bl, m) != "" {
			noteBtn = i18n.T(lang, "tx.btn_note_set")
		}
		row := []models.InlineKeyboardButton{btn(label, "tx:k:"+k.name), btn(noteBtn, "tx:k:"+k.note)}
		if enabled {
			on = append(on, row)
		} else {
			off = append(off, row)
		}
	}
	rows := append(on, off...)
	rows = append(rows, navBack(lang, back))
	text := i18n.T(lang, "tx.methods_title")
	// Кнопка со своим текстом без {способ_оплаты} название не подхватит.
	var fixed []string
	for _, m := range payMethodOrder {
		k := payMethodKeys[m]
		if canon, own := i18n.Effective(bl, k.btn); own && !strings.Contains(canon, "{payment_method}") {
			fixed = append(fixed, methodName(bl, m))
		}
	}
	if len(fixed) > 0 {
		text += "\n\n" + i18n.T(lang, "tx.methods_btn_fixed", escapeHTMLText(strings.Join(fixed, ", ")))
	}
	a.sendIfaceKB(ctx, chatID, text, rows)
}

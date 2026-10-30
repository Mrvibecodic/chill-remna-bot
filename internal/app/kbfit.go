package app

import (
	"unicode"

	"github.com/go-telegram/bot/models"
)

// Укладка кнопок под телефон. Telegram делит ряд поровну между кнопками и
// обрезает подпись многоточием, если она не влезла в свою долю: на экране
// шириной 360dp ряд из трёх кнопок вмещает около десяти букв на кнопку.
// Подписи правятся админом в редакторе текстов, в них попадают названия
// тарифов и способов оплаты, поэтому ряды раскладываются на выходе, а не
// только в коде экранов.
//
// Размеры — в dp для Telegram на Android: клавиатура около 330dp на экране
// 360dp, зазор между кнопками 5dp, поля внутри кнопки 10dp, шрифт 15sp
// полужирный.
const (
	kbWidth      = 330.0
	kbGap        = 5.0
	kbPad        = 10.0
	kbFont       = 15.0 * 1.04
	kbCornerIcon = 20.0 // значок в углу у ссылок и мини-аппа съедает место с обеих сторон центрированной подписи
	kbMaxSplit   = 8
)

// rowCap — сколько dp достаётся подписи каждой кнопки в ряду из n кнопок.
func rowCap(n int) float64 {
	return (kbWidth-kbGap*float64(n-1))/float64(n) - kbPad
}

func runeEm(r rune) float64 {
	switch {
	case r == 0xFE0F || r == 0xFE0E || r == 0x200D || (r >= 0x1F3FB && r <= 0x1F3FF):
		return 0
	case r >= 0x1F1E6 && r <= 0x1F1FF: // половина флага
		return 0.6
	case r >= 0x1F000, r >= 0x2600 && r <= 0x27BF, r >= 0x2190 && r <= 0x21FF,
		r >= 0x2300 && r <= 0x23FF, r >= 0x2B00 && r <= 0x2BFF, r >= 0x25A0 && r <= 0x25FF,
		r == 0x2139, r == 0x24C2, r == 0x3030, r == 0x303D, r == 0x00A9, r == 0x00AE, r == 0x2122:
		return 1.25
	case r == ' ':
		return 0.25
	}
	switch r {
	case 'i', 'l', 'j', 'I', '.', ',', ':', ';', '\'', '|', '!':
		return 0.26
	case 'f', 't', 'r':
		return 0.36
	case 'm', 'w', 'M', 'W':
		return 0.86
	case '(', ')', '[', ']':
		return 0.34
	case '-':
		return 0.33
	case '/':
		return 0.4
	case '«', '»':
		return 0.48
	case '—', '…':
		return 0.8
	case '%', '@', '№':
		return 0.85
	case 'ж', 'ш', 'щ', 'ю', 'ы', 'Ж', 'Ш', 'Щ', 'Ю', 'Ы', 'М', 'Ф':
		if unicode.IsUpper(r) {
			return 0.9
		}
		return 0.78
	case 'м', 'ф':
		return 0.66
	case 'г', 'т':
		return 0.46
	case 'Г':
		return 0.52
	}
	switch {
	case r >= 'a' && r <= 'z':
		return 0.53
	case r >= 'A' && r <= 'Z':
		return 0.64
	case r >= '0' && r <= '9':
		return 0.56
	case r >= 'а' && r <= 'я', r == 'ё':
		return 0.55
	case r >= 'А' && r <= 'Я', r == 'Ё':
		return 0.68
	case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hangul, r),
		unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r):
		return 1.0
	}
	return 0.56
}

// btnWidth — сколько dp нужно подписи кнопки, чтобы не обрезаться.
func btnWidth(b models.InlineKeyboardButton) float64 {
	em, joined := 0.0, false
	for _, r := range b.Text {
		if joined { // вторая половина составного эмодзи рисуется одним значком
			joined = false
			continue
		}
		if r == 0x200D {
			joined = true
			continue
		}
		em += runeEm(r)
	}
	w := em * kbFont
	if b.IconCustomEmojiID != "" {
		w += 1.25 * kbFont
	}
	if b.URL != "" || b.WebApp != nil || b.LoginURL != nil || b.SwitchInlineQuery != nil ||
		b.SwitchInlineQueryCurrentChat != nil || b.SwitchInlineQueryChosenChat != nil || b.CopyText != nil {
		w += kbCornerIcon
	}
	return w
}

// rowFits — влезают ли все подписи ряда без многоточия. Одна кнопка «влезает»
// всегда: делить её некуда.
func rowFits(row []models.InlineKeyboardButton) bool {
	if len(row) <= 1 {
		return true
	}
	c := rowCap(len(row))
	for _, b := range row {
		if btnWidth(b) > c {
			return false
		}
	}
	return true
}

// fitKeyboard разбивает ряды, которые не влезают в ширину телефона. Порядок
// кнопок не меняется, соседние ряды не сливаются. Если всё влезает, отдаётся
// тот же срез.
func fitKeyboard(rows [][]models.InlineKeyboardButton) [][]models.InlineKeyboardButton {
	bad := false
	for _, row := range rows {
		if !rowFits(row) {
			bad = true
			break
		}
	}
	if !bad {
		return rows
	}
	out := make([][]models.InlineKeyboardButton, 0, len(rows)+2)
	for _, row := range rows {
		if rowFits(row) {
			out = append(out, row)
			continue
		}
		out = append(out, splitRow(row)...)
	}
	return out
}

// splitRow делит ряд на наименьшее число подряд идущих кусков, каждый из
// которых влезает; из равных по числу вариантов берётся самый свободный.
func splitRow(row []models.InlineKeyboardButton) [][]models.InlineKeyboardButton {
	n := len(row)
	w := make([]float64, n)
	for i, b := range row {
		w[i] = btnWidth(b)
	}
	if n > kbMaxSplit {
		return greedySplit(row, w)
	}
	bestK, bestCost, bestMask := n+1, 0.0, 0
	for mask := 1; mask < 1<<(n-1); mask++ { // бит i — разрез после кнопки i
		k, cost, ok := 1, 0.0, true
		start := 0
		for i := 0; i < n && ok; i++ {
			if i == n-1 || mask&(1<<i) != 0 {
				m, widest := i-start+1, 0.0
				for _, x := range w[start : i+1] {
					widest = max(widest, x)
				}
				c := rowCap(m)
				if m > 1 && widest > c {
					ok = false
				}
				cost = max(cost, widest/c)
				if i < n-1 {
					k++
				}
				start = i + 1
			}
		}
		if !ok {
			continue
		}
		if k < bestK || (k == bestK && cost < bestCost) {
			bestK, bestCost, bestMask = k, cost, mask
		}
	}
	var out [][]models.InlineKeyboardButton
	start := 0
	for i := 0; i < n; i++ {
		if i == n-1 || bestMask&(1<<i) != 0 {
			out = append(out, row[start:i+1:i+1])
			start = i + 1
		}
	}
	return out
}

func greedySplit(row []models.InlineKeyboardButton, w []float64) [][]models.InlineKeyboardButton {
	var out [][]models.InlineKeyboardButton
	start, widest := 0, 0.0
	for i := range row {
		nw := max(widest, w[i])
		if i > start && nw > rowCap(i-start+1) {
			out = append(out, row[start:i:i])
			start, nw = i, w[i]
		}
		widest = nw
	}
	return append(out, row[start:len(row):len(row)])
}

// fitMarkup — то же для разметки, переданной целиком.
func fitMarkup(rm models.ReplyMarkup) models.ReplyMarkup {
	switch kb := rm.(type) {
	case models.InlineKeyboardMarkup:
		kb.InlineKeyboard = fitKeyboard(kb.InlineKeyboard)
		return kb
	case *models.InlineKeyboardMarkup:
		if kb == nil {
			return rm
		}
		return &models.InlineKeyboardMarkup{InlineKeyboard: fitKeyboard(kb.InlineKeyboard)}
	}
	return rm
}

package i18n

import "fmt"

const Fallback = "ru"

var bundles = map[string]map[string]string{
	"ru": ru,
	"en": en,
}

func T(lang, key string, args ...any) string {
	if c, ok := override(lang, key); ok {
		if s, ok := c.render(lang, key, args); ok {
			return s
		}
	}
	tmpl, ok := lookupOK(lang, key)
	if !ok {
		tmpl, ok = lookupOK(Fallback, key)
	}
	if !ok {
		return key
	}
	// Пустое значение — законный текст: необязательные тексты (описание
	// способа оплаты) по умолчанию пусты и просто не показываются.
	if tmpl == "" {
		return ""
	}
	if len(args) == 0 {
		return tmpl
	}
	return fmt.Sprintf(tmpl, args...)
}

func lookupOK(lang, key string) (string, bool) {
	if b, ok := bundles[lang]; ok {
		if v, ok := b[key]; ok {
			return v, true
		}
	}
	return "", false
}

func lookup(lang, key string) string {
	if b, ok := bundles[lang]; ok {
		if v, ok := b[key]; ok {
			return v
		}
	}
	return ""
}

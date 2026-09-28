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
	tmpl := lookup(lang, key)
	if tmpl == "" {
		tmpl = lookup(Fallback, key)
	}
	if tmpl == "" {
		return key
	}
	if len(args) == 0 {
		return tmpl
	}
	return fmt.Sprintf(tmpl, args...)
}

func lookup(lang, key string) string {
	if b, ok := bundles[lang]; ok {
		if v, ok := b[key]; ok {
			return v
		}
	}
	return ""
}

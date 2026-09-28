package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Свои тексты админа (раздел «Тексты бота»): хранятся в конфиге, применяются
// в i18n. Конфиг выбран намеренно — тексты сами едут в бэкап, в переезд базы и
// в переустановку.

// applyTexts переносит свои тексты конфига в i18n. Не берёт a.mu: зовётся и
// под ним, и без него.
func (a *App) applyTexts(cfg *model.BotConfig) {
	texts := map[string]map[string]string{}
	if cfg != nil {
		for lang, m := range cfg.Texts.Overrides {
			for key, o := range m {
				if texts[lang] == nil {
					texts[lang] = map[string]string{}
				}
				texts[lang][key] = o.Text
			}
		}
	}
	rejected := i18n.SetOverrides(texts)
	a.txtMu.Lock()
	a.txtRejected = rejected
	a.txtMu.Unlock()
}

// textRejected — причина, по которой свой текст не применён (nil — применён).
func (a *App) textRejected(lang, key string) error {
	a.txtMu.Lock()
	defer a.txtMu.Unlock()
	return a.txtRejected[lang][key]
}

// textOverride — свой текст ключа из конфига.
func (a *App) textOverride(lang, key string) (model.TextOverride, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.TextOverride{}, false
	}
	o, ok := a.botCfg.Texts.Overrides[lang][key]
	return o, ok
}

// textOverrides — копия своих текстов языка.
func (a *App) textOverrides(lang string) map[string]model.TextOverride {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]model.TextOverride{}
	if a.botCfg == nil {
		return out
	}
	for k, v := range a.botCfg.Texts.Overrides[lang] {
		if _, ok := i18n.EditableByKey(k); ok {
			out[k] = v
		}
	}
	return out
}

// textStale — после обновления бота стандартный вариант своего текста
// поменялся.
func textStale(lang, key string, o model.TextOverride) bool {
	return o.Base != "" && o.Base != i18n.DefaultHash(lang, key)
}

// updateTexts меняет свои тексты и пишет конфиг. Если запись не удалась,
// изменение откатывается: иначе пользователи видели бы текст, который
// исчезнет после перезапуска.
func (a *App) updateTexts(ctx context.Context, change func(tc *model.TextsConfig)) error {
	a.mu.Lock()
	if a.botCfg == nil {
		a.mu.Unlock()
		return nil
	}
	prev := cloneTexts(a.botCfg.Texts)
	change(&a.botCfg.Texts)
	a.applyTexts(a.botCfg)
	a.mu.Unlock()
	err := a.saveConfigOnly(ctx)
	if err != nil {
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Texts = prev
			a.applyTexts(a.botCfg)
		}
		a.mu.Unlock()
	}
	return err
}

func cloneTexts(tc model.TextsConfig) model.TextsConfig {
	out := model.TextsConfig{NotifiedStale: tc.NotifiedStale}
	if tc.Overrides != nil {
		out.Overrides = map[string]map[string]model.TextOverride{}
		for lang, m := range tc.Overrides {
			cp := make(map[string]model.TextOverride, len(m))
			for k, v := range m {
				cp[k] = v
			}
			out.Overrides[lang] = cp
		}
	}
	return out
}

// setTextOverride сохраняет свой текст (canonical == "" — вернуть стандартный).
func (a *App) setTextOverride(ctx context.Context, lang, key, canonical string) error {
	return a.updateTexts(ctx, func(tc *model.TextsConfig) {
		if canonical == "" {
			delete(tc.Overrides[lang], key)
			if len(tc.Overrides[lang]) == 0 {
				delete(tc.Overrides, lang)
			}
			return
		}
		if tc.Overrides == nil {
			tc.Overrides = map[string]map[string]model.TextOverride{}
		}
		if tc.Overrides[lang] == nil {
			tc.Overrides[lang] = map[string]model.TextOverride{}
		}
		tc.Overrides[lang][key] = model.TextOverride{
			Text: canonical,
			Base: i18n.DefaultHash(lang, key),
			At:   time.Now().UTC().Format(time.RFC3339),
		}
	})
}

// keepTextOverride — «оставить мой»: свой текст сверен с новым стандартным.
func (a *App) keepTextOverride(ctx context.Context, lang, key string) error {
	return a.updateTexts(ctx, func(tc *model.TextsConfig) {
		if o, ok := tc.Overrides[lang][key]; ok {
			o.Base = i18n.DefaultHash(lang, key)
			tc.Overrides[lang][key] = o
		}
	})
}

// resetAllTexts возвращает стандартные тексты языка.
func (a *App) resetAllTexts(ctx context.Context, lang string) error {
	return a.updateTexts(ctx, func(tc *model.TextsConfig) {
		delete(tc.Overrides, lang)
	})
}

// textsAttention — свои тексты, требующие внимания: у них обновился
// стандартный вариант или они не применяются.
func (a *App) textsAttention(lang string) (stale, broken []string) {
	for key, o := range a.textOverrides(lang) {
		if a.textRejected(lang, key) != nil {
			broken = append(broken, key)
		} else if textStale(lang, key, o) {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	sort.Strings(broken)
	return stale, broken
}

// sendTextsNotice один раз после обновления говорит админу, что у его текстов
// поменялся стандартный вариант или они перестали применяться.
func (a *App) sendTextsNotice(ctx context.Context) {
	if a.cfg.AdminID == 0 {
		return
	}
	lang := a.botLang()
	stale, broken := a.textsAttention(lang)
	if len(stale) == 0 && len(broken) == 0 {
		return
	}
	// Отпечаток включает стандартный текст и свой: новое обновление того же
	// ключа — новое предупреждение.
	var parts []string
	for _, k := range stale {
		parts = append(parts, "s:"+k+":"+i18n.DefaultHash(lang, k))
	}
	own := a.textOverrides(lang)
	for _, k := range broken {
		parts = append(parts, "b:"+k+":"+own[k].Text)
	}
	sum := sha256.Sum256([]byte(lang + "|" + strings.Join(parts, "|")))
	mark := hex.EncodeToString(sum[:6])
	a.mu.Lock()
	done := a.botCfg == nil || a.botCfg.Texts.NotifiedStale == mark
	a.mu.Unlock()
	if done {
		return
	}
	var lines []string
	if len(stale) > 0 {
		lines = append(lines, i18n.T(lang, "tx.notice_stale", len(stale)))
	}
	if len(broken) > 0 {
		lines = append(lines, i18n.T(lang, "tx.notice_broken", len(broken)))
	}
	if a.notifyKB(ctx, a.cfg.AdminID, i18n.T(lang, "tx.notice_title")+"\n\n"+strings.Join(lines, "\n\n"),
		[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "tx.btn_check"), "tx:att:0")}}) == 0 {
		// Не доставлено — повторим на следующем запуске.
		return
	}
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.Texts.NotifiedStale = mark
	}
	a.mu.Unlock()
	_ = a.saveConfigOnly(ctx)
}

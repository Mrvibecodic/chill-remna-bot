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

// setTextOverride сохраняет свой текст (canonical == "" — вернуть стандартный).
func (a *App) setTextOverride(ctx context.Context, lang, key, canonical string) error {
	a.mu.Lock()
	if a.botCfg == nil {
		a.mu.Unlock()
		return nil
	}
	tc := &a.botCfg.Texts
	if canonical == "" {
		delete(tc.Overrides[lang], key)
		if len(tc.Overrides[lang]) == 0 {
			delete(tc.Overrides, lang)
		}
	} else {
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
	}
	a.applyTexts(a.botCfg)
	a.mu.Unlock()
	return a.saveConfigOnly(ctx)
}

// keepTextOverride — «оставить мой»: свой текст сверен с новым стандартным.
func (a *App) keepTextOverride(ctx context.Context, lang, key string) error {
	a.mu.Lock()
	if a.botCfg == nil {
		a.mu.Unlock()
		return nil
	}
	if o, ok := a.botCfg.Texts.Overrides[lang][key]; ok {
		o.Base = i18n.DefaultHash(lang, key)
		a.botCfg.Texts.Overrides[lang][key] = o
	}
	a.mu.Unlock()
	return a.saveConfigOnly(ctx)
}

// resetAllTexts возвращает стандартные тексты языка.
func (a *App) resetAllTexts(ctx context.Context, lang string) error {
	a.mu.Lock()
	if a.botCfg == nil {
		a.mu.Unlock()
		return nil
	}
	delete(a.botCfg.Texts.Overrides, lang)
	a.applyTexts(a.botCfg)
	a.mu.Unlock()
	return a.saveConfigOnly(ctx)
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
	sum := sha256.Sum256([]byte(lang + "|" + strings.Join(stale, ",") + "|" + strings.Join(broken, ",")))
	mark := hex.EncodeToString(sum[:6])
	a.mu.Lock()
	if a.botCfg == nil || a.botCfg.Texts.NotifiedStale == mark {
		a.mu.Unlock()
		return
	}
	a.botCfg.Texts.NotifiedStale = mark
	a.mu.Unlock()
	_ = a.saveConfigOnly(ctx)
	var parts []string
	if len(stale) > 0 {
		parts = append(parts, i18n.T(lang, "tx.notice_stale", len(stale)))
	}
	if len(broken) > 0 {
		parts = append(parts, i18n.T(lang, "tx.notice_broken", len(broken)))
	}
	a.notifyKB(ctx, a.cfg.AdminID, i18n.T(lang, "tx.notice_title")+"\n\n"+strings.Join(parts, "\n\n"),
		[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "tx.btn_check"), "tx:att:0")}})
}

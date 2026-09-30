package app

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"
)

// entitiesToHTML собирает HTML Telegram из текста сообщения и его разметки
// (entities). Смещения entities — в единицах UTF-16. Пересекающиеся
// диапазоны закрываются и открываются заново, чтобы теги были вложены
// правильно. Автоссылки, упоминания и хэштеги Telegram распознаёт сам — их
// нет смысла переносить.
func entitiesToHTML(text string, ents []models.MessageEntity) string {
	u := utf16.Encode([]rune(text))
	type span struct {
		e          models.MessageEntity
		start, end int
		idx        int
	}
	var spans []span
	for i, e := range ents {
		if openTag(e) == "" || e.Length <= 0 || e.Offset < 0 || e.Offset+e.Length > len(u) {
			continue
		}
		spans = append(spans, span{e: e, start: e.Offset, end: e.Offset + e.Length, idx: i})
	}
	starts := map[int][]int{}
	for i, s := range spans {
		starts[s.start] = append(starts[s.start], i)
	}
	for p := range starts {
		l := starts[p]
		// Длинный диапазон открывается первым — он внешний.
		sort.SliceStable(l, func(a, b int) bool {
			if spans[l[a]].end != spans[l[b]].end {
				return spans[l[a]].end > spans[l[b]].end
			}
			return spans[l[a]].idx < spans[l[b]].idx
		})
		starts[p] = l
	}

	var b strings.Builder
	var stack []int
	flush := func(from, to int) {
		if from < to {
			b.WriteString(escapeHTMLText(string(utf16.Decode(u[from:to]))))
		}
	}
	last := 0
	for p := 0; p <= len(u); p++ {
		ending := false
		for _, s := range stack {
			if spans[s].end == p {
				ending = true
				break
			}
		}
		_, starting := starts[p]
		if !ending && !starting {
			continue
		}
		flush(last, p)
		last = p
		if ending {
			var reopen []int
			for {
				done := true
				for _, s := range stack {
					if spans[s].end == p {
						done = false
						break
					}
				}
				if done {
					break
				}
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				b.WriteString(closeTag(spans[top].e))
				if spans[top].end != p {
					reopen = append(reopen, top)
				}
			}
			for i := len(reopen) - 1; i >= 0; i-- {
				stack = append(stack, reopen[i])
				b.WriteString(openTag(spans[reopen[i]].e))
			}
		}
		for _, s := range starts[p] {
			// Открытые диапазоны, которые кончаются раньше нового, уходят
			// внутрь него: так режется короткий (жирный), а не длинный
			// (цитата) — иначе одна цитата распалась бы на две.
			k := len(stack)
			for k > 0 && spans[stack[k-1]].end < spans[s].end {
				k--
			}
			inner := append([]int(nil), stack[k:]...)
			for i := len(inner) - 1; i >= 0; i-- {
				b.WriteString(closeTag(spans[inner[i]].e))
			}
			stack = append(stack[:k], s)
			b.WriteString(openTag(spans[s].e))
			for _, in := range inner {
				stack = append(stack, in)
				b.WriteString(openTag(spans[in].e))
			}
		}
	}
	flush(last, len(u))
	return b.String()
}

func escapeHTMLText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func escapeAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func openTag(e models.MessageEntity) string {
	switch e.Type {
	case models.MessageEntityTypeBold:
		return "<b>"
	case models.MessageEntityTypeItalic:
		return "<i>"
	case models.MessageEntityTypeUnderline:
		return "<u>"
	case models.MessageEntityTypeStrikethrough:
		return "<s>"
	case models.MessageEntityTypeSpoiler:
		return "<tg-spoiler>"
	case models.MessageEntityTypeCode:
		return "<code>"
	case models.MessageEntityTypePre:
		if e.Language != "" {
			return `<pre><code class="language-` + escapeAttr(e.Language) + `">`
		}
		return "<pre>"
	case models.MessageEntityTypeBlockquote:
		return "<blockquote>"
	case models.MessageEntityTypeExpandableBlockquote:
		return "<blockquote expandable>"
	case models.MessageEntityTypeTextLink:
		if e.URL == "" {
			return ""
		}
		return `<a href="` + escapeAttr(e.URL) + `">`
	case models.MessageEntityTypeTextMention:
		if e.User == nil {
			return ""
		}
		return `<a href="tg://user?id=` + strconv.FormatInt(e.User.ID, 10) + `">`
	}
	// Премиум-эмодзи не переносятся: бот сам подменяет эмодзи на премиум
	// (раздел «Эмодзи»), и вложенный тег сломал бы разметку сообщения.
	return ""
}

func closeTag(e models.MessageEntity) string {
	switch e.Type {
	case models.MessageEntityTypeBold:
		return "</b>"
	case models.MessageEntityTypeItalic:
		return "</i>"
	case models.MessageEntityTypeUnderline:
		return "</u>"
	case models.MessageEntityTypeStrikethrough:
		return "</s>"
	case models.MessageEntityTypeSpoiler:
		return "</tg-spoiler>"
	case models.MessageEntityTypeCode:
		return "</code>"
	case models.MessageEntityTypePre:
		if e.Language != "" {
			return "</code></pre>"
		}
		return "</pre>"
	case models.MessageEntityTypeBlockquote, models.MessageEntityTypeExpandableBlockquote:
		return "</blockquote>"
	case models.MessageEntityTypeTextLink, models.MessageEntityTypeTextMention:
		return "</a>"
	}
	return ""
}

// varLinkRe — ссылка на переменную в тексте админа: [текст]({ссылка}).
// Настоящую ссылку с адресом «{ссылка}» в Telegram не набрать, поэтому
// такая запись и есть способ поставить переменную в href.
var varLinkRe = regexp.MustCompile(`\[([^\[\]<>\n]+)\]\((\{[\p{L}\p{N}_ ]{1,40}\})\)`)

func varLinksToHTML(s string) string {
	// Внутри кода и внутри ссылки запись остаётся текстом: вложенная ссылка
	// сломала бы разметку.
	var b strings.Builder
	depth := 0
	last := 0
	for _, m := range tagOrLinkRe.FindAllStringIndex(s, -1) {
		tok := s[m[0]:m[1]]
		if tok[0] == '<' {
			name := strings.ToLower(strings.TrimLeft(tok[1:len(tok)-1], "/"))
			if k := strings.IndexAny(name, " \t\n"); k >= 0 {
				name = name[:k]
			}
			if name == "code" || name == "pre" || name == "a" {
				if strings.HasPrefix(tok, "</") {
					if depth > 0 {
						depth--
					}
				} else {
					depth++
				}
			}
			continue
		}
		if depth > 0 {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(varLinkRe.ReplaceAllString(tok, `<a href="$2">$1</a>`))
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// tagOrLinkRe — тег или запись [текст]({переменная}).
var tagOrLinkRe = regexp.MustCompile(`<[^<>]+>|` + varLinkRe.String())

// hrefVarRe — ссылка на переменную в шаблоне: обратное превращение для
// текста, который админ копирует. Сообщение со ссылкой «{ссылка}» Telegram
// не примет.
var hrefVarRe = regexp.MustCompile(`<a href="(\{[^"{}]+\})">([^<]*)</a>`)

func varLinksToMarkup(s string) string {
	return hrefVarRe.ReplaceAllString(s, `[$2]($1)`)
}

// visibleText — текст без разметки, как его увидит человек.
func visibleText(s string) string { return stripHTMLTags(s) }

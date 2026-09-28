package app

import "testing"

func TestApplyPremiumEmojiOff(t *testing.T) {
	if got := applyPremiumEmoji("✅ готово", nil); got != "✅ готово" {
		t.Fatalf("без карты текст не должен меняться: %q", got)
	}
}

func TestApplyPremiumEmojiOn(t *testing.T) {
	m := map[string]string{"✅": "123"}
	got := applyPremiumEmoji("✅ готово", m)
	want := `<tg-emoji emoji-id="123">✅</tg-emoji> готово`
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestApplyPremiumEmojiEmptyID(t *testing.T) {
	m := map[string]string{"✅": ""}
	if got := applyPremiumEmoji("✅ ok", m); got != "✅ ok" {
		t.Fatalf("пустой id -> без изменений: %q", got)
	}
}

// Премиум-эмодзи не подставляются внутрь тегов, <code>, <pre> и уже стоящего
// <tg-emoji>: иначе разметка ломается и сообщение уходит без оформления.
func TestApplyPremiumEmojiSkipsMarkup(t *testing.T) {
	m := map[string]string{"🔥": "1", "❤️": "2", "❤": "3"}
	in := `🔥 <code>🔥</code> <a href="https://e.test/🔥">🔥</a> <tg-emoji emoji-id="9">🔥</tg-emoji> ❤️`
	want := `<tg-emoji emoji-id="1">🔥</tg-emoji> <code>🔥</code> <a href="https://e.test/🔥"><tg-emoji emoji-id="1">🔥</tg-emoji></a> <tg-emoji emoji-id="9">🔥</tg-emoji> <tg-emoji emoji-id="2">❤️</tg-emoji>`
	if got := applyPremiumEmoji(in, m); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

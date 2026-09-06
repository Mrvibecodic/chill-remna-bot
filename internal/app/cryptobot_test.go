package app

import "testing"

// Валюта счёта CryptoBot: только то, что провайдер реально принимает.
//
// Раньше любая нераспознанная строка молча превращалась в RUB: прайс в «$» с
// ценой «10» уходил счётом на 10 ₽, и увидеть это было негде — пользователю
// рисовался тот же рубль.
func TestCbFiat(t *testing.T) {
	for _, in := range []string{"", "₽", "руб", "р", "RUB", "rur"} {
		if got, ok := cbFiat(in); !ok || got != "RUB" {
			t.Fatalf("cbFiat(%q) = (%q, %v), ожидалось (RUB, true)", in, got, ok)
		}
	}
	for _, in := range []string{"USD", "usd", "EUR", "KZT", "TRY"} {
		if _, ok := cbFiat(in); !ok {
			t.Fatalf("cbFiat(%q): поддерживаемая валюта отклонена", in)
		}
	}
	// Не ISO-код и код вне списка провайдера — отказ, а не тихий рубль.
	for _, in := range []string{"$", "€", "XYZ", "долларов", "12"} {
		if got, ok := cbFiat(in); ok {
			t.Fatalf("cbFiat(%q) = (%q, true), ожидался отказ", in, got)
		}
	}
}

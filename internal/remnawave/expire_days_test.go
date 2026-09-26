package remnawave

import (
	"testing"
	"time"
)

// Срок в днях продлевается от текущего конца с поправкой зачёта, но не
// раньше «сейчас + купленные дни» — как продление месяцами.
func TestNextExpireDays(t *testing.T) {
	now := time.Now().UTC()
	in30 := &panelUser{ExpireAt: now.Add(30 * 24 * time.Hour).Format(time.RFC3339)}
	days := func(s string) float64 {
		t.Helper()
		e, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return e.Sub(now).Hours() / 24
	}
	cases := []struct {
		name     string
		u        *panelUser
		d, extra int
		min, max float64
	}{
		{"продление", in30, 7, 0, 36.9, 37.1},
		{"апгрейд", in30, 7, -25, 11.9, 12.1},
		{"поправка больше остатка", in30, 7, -40, 6.9, 7.1},
		{"даунгрейд", in30, 7, 10, 46.9, 47.1},
		{"нет пользователя", nil, 7, 0, 6.9, 7.1},
		{"истёкший", &panelUser{ExpireAt: now.Add(-48 * time.Hour).Format(time.RFC3339)}, 7, 0, 6.9, 7.1},
	}
	for _, c := range cases {
		if got := days(nextExpireDays(c.u, c.d, c.extra)); got < c.min || got > c.max {
			t.Errorf("%s: %.2f дн., ожидалось %.1f–%.1f", c.name, got, c.min, c.max)
		}
	}
}

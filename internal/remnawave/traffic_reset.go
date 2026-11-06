package remnawave

import "time"

// Минуты после полуночи, в которые панель обнуляет счётчик по каждой
// стратегии (getNextTrafficResetAt в исходниках панели).
const (
	resetOffDay     = 5 * time.Minute
	resetOffRolling = 10 * time.Minute
	resetOffWeek    = 15 * time.Minute
	resetOffMonth   = 20 * time.Minute
)

// NextTrafficReset — когда панель в следующий раз обнулит трафик: тот же
// расчёт, что у панели (время сервера панели — UTC). Нулевое время — сброса
// не будет (NO_RESET) или стратегия неизвестна; для MONTH_ROLLING без даты
// создания аккаунта — тоже нулевое.
func NextTrafficReset(strategy string, created, now time.Time) time.Time {
	now = now.UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	switch strategy {
	case "DAY":
		next := day.Add(resetOffDay)
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		return next
	case "WEEK":
		next := day.AddDate(0, 0, (int(time.Monday)-int(now.Weekday())+7)%7).Add(resetOffWeek)
		if !next.After(now) {
			next = next.AddDate(0, 0, 7)
		}
		return next
	case "MONTH":
		next := month.Add(resetOffMonth)
		if !next.After(now) {
			next = month.AddDate(0, 1, 0).Add(resetOffMonth)
		}
		return next
	case "MONTH_ROLLING":
		if created.IsZero() {
			return time.Time{}
		}
		created = created.UTC()
		anchor := created.Day()
		at := func(m time.Time) time.Time {
			last := m.AddDate(0, 1, -1).Day()
			return time.Date(m.Year(), m.Month(), min(anchor, last), 0, 0, 0, 0, time.UTC).Add(resetOffRolling)
		}
		next := at(month)
		if !next.After(now) {
			next = at(month.AddDate(0, 1, 0))
		}
		if first := at(time.Date(created.Year(), created.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)); next.Before(first) {
			next = first
		}
		return next
	}
	return time.Time{}
}

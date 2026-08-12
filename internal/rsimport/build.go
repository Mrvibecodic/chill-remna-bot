package rsimport

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Tables — таблицы remnashop, которые нам интересны. Всё остальное в дампе
// (настройки, рассылки, шлюзы, oauth) не переносится: у нашего бота своя
// модель конфигурации.
var Tables = []string{
	"users",
	"subscriptions",
	"referrals",
	"referral_rewards",
	"promocodes",
	"promocode_activations",
	"transactions",
}

// Поддерживается схема remnashop v0.8.0+: дочерние таблицы ссылаются на
// пользователя числовым внешним ключом (user_id / referrer_id / referred_id),
// снимок тарифа лежит в plan_snapshot. В версиях до v0.8.0 на месте этих
// колонок были *_telegram_id — такой дамп мы отклоняем с понятным текстом,
// а не разбираем наполовину (см. checkSchema).
var legacyColumns = []struct {
	table string
	cols  []string
}{
	{"subscriptions", []string{"user_telegram_id"}},
	{"transactions", []string{"user_telegram_id"}},
	{"referrals", []string{"referrer_telegram_id", "referred_telegram_id"}},
	{"referral_rewards", []string{"user_telegram_id"}},
	{"promocode_activations", []string{"user_telegram_id"}},
}

// User — пользователь remnashop, приведённый к нашей модели.
type User struct {
	TelegramID  int64
	Username    string
	FirstName   string
	Blocked     bool
	TrialUsed   bool
	SubExpireAt string // RFC3339, пусто — подписки не было
	// Balance — remnashop не имеет кошелька, но имеет реферальные баллы
	// (points). Переносим их как баланс: 1 балл = 1 ₽.
	BalanceKopecks   int64
	RefEarnedKopecks int64
	ReferredBy       int64
	CreatedAt        string
}

// Promo — промокод remnashop, сведённый к нашим видам (дни / баланс).
type Promo struct {
	Code      string
	Kind      string // "days" | "balance"
	Value     int
	MaxUses   int
	ExpiresAt string
	CreatedAt string
}

// PromoUse — факт активации промокода конкретным пользователем.
type PromoUse struct {
	Code       string
	TelegramID int64
	CreatedAt  string
}

// Payment — оплаченная транзакция remnashop (только история, деньги не трогаем).
type Payment struct {
	TelegramID int64
	Method     string
	Months     int
	// Days — исходная длительность из дампа remnashop: там сроки лежат в
	// днях, и Months — это огрубление (дни+15)/30. 0 — источник дней не знал.
	Days      int
	Amount    string
	ExtID     string
	CreatedAt string
}

// Data — всё, что удалось вытащить из дампа.
type Data struct {
	Users     []User
	Promos    []Promo
	PromoUses []PromoUse
	Payments  []Payment

	// Счётчики для предпросмотра.
	TotalUsers  int // строк в users
	SkippedWeb  int // пользователи без telegram_id (email/oauth) — не переносим
	WithSub     int
	WithBalance int
	Referrals   int

	Warnings []string
}

// gatewayMethods переводит платёжные шлюзы remnashop в методы нашего бота.
// Незнакомое имя остаётся как есть — история платежей всё равно read-only.
var gatewayMethods = map[string]string{
	"TELEGRAM_STARS": "stars",
	"YOOKASSA":       "yookassa",
	"CRYPTOPAY":      "cryptobot",
	"PLATEGA":        "platega",
}

// Load читает дамп и сразу собирает Data.
func Load(r io.Reader) (*Data, error) {
	tables, err := ParseDump(r, Tables...)
	if err != nil {
		return nil, err
	}
	return Build(tables)
}

// binder связывает строки дочерних таблиц с пользователями по внутреннему id
// remnashop (users.id) — именно им ссылаются все таблицы начиная с v0.8.0.
type binder map[int64]*User

func (b binder) user(row []*string, idx int) *User {
	v, ok := cellInt(row, idx)
	if !ok {
		return nil
	}
	return b[v]
}

// checkSchema отсекает дампы версий до v0.8.0: там дочерние таблицы ссылались
// на пользователя через *_telegram_id. Разбирать такой дамп наполовину хуже,
// чем честно сказать, что нужно сделать.
func checkSchema(tables map[string]*Table) error {
	for _, l := range legacyColumns {
		t := tables[l.table]
		if t == nil {
			continue
		}
		for _, c := range l.cols {
			if t.Col(c) >= 0 {
				return fmt.Errorf(
					"дамп снят с remnashop старее v0.8.0 (в таблице %s ещё колонка %s). "+
						"Поддерживаются v0.8.0–v0.8.2: обновите remnashop до 0.8.x — он сам прогонит миграции базы — "+
						"и снимите бэкап заново", l.table, c)
			}
		}
	}
	return nil
}

// Build раскладывает таблицы remnashop по нашим сущностям.
func Build(tables map[string]*Table) (*Data, error) {
	ut := tables["users"]
	if ut == nil || len(ut.Rows) == 0 {
		return nil, fmt.Errorf("в дампе нет таблицы users — это не дамп базы remnashop")
	}
	if err := checkSchema(tables); err != nil {
		return nil, err
	}

	d := &Data{}
	b := binder{}
	var order []*User

	idIdx := ut.Col("id")
	tgIdx := ut.Col("telegram_id")
	if idIdx < 0 || tgIdx < 0 {
		return nil, fmt.Errorf("в таблице users нет колонок id/telegram_id — это не дамп базы remnashop")
	}
	unameIdx := ut.Col("username")
	nameIdx := ut.Col("name")
	pointsIdx := ut.Col("points")
	blockedIdx := ut.Col("is_blocked")
	trialIdx := ut.Col("is_trial_available")
	createdIdx := ut.Col("created_at")

	for _, row := range ut.Rows {
		d.TotalUsers++
		tgID, ok := cellInt(row, tgIdx)
		if !ok || tgID == 0 {
			// Пользователь веб-кабинета remnashop (email/oauth): у нас пароли
			// в другом формате, восстановить вход нельзя — пропускаем.
			d.SkippedWeb++
			continue
		}
		u := &User{
			TelegramID: tgID,
			Username:   strings.TrimPrefix(cellStr(row, unameIdx), "@"),
			FirstName:  cellStr(row, nameIdx),
			Blocked:    cellBool(row, blockedIdx),
			CreatedAt:  cellTS(row, createdIdx),
		}
		// is_trial_available=false означает, что триал уже израсходован.
		if trialIdx >= 0 {
			u.TrialUsed = !cellBool(row, trialIdx)
		}
		if pts, ok := cellInt(row, pointsIdx); ok && pts > 0 {
			u.BalanceKopecks = pts * 100
			u.RefEarnedKopecks = pts * 100
		}
		if id, ok := cellInt(row, idIdx); ok {
			b[id] = u
		}
		order = append(order, u)
	}

	applySubscriptions(tables["subscriptions"], b, d)
	applyReferrals(tables["referrals"], tables["referral_rewards"], b, d)

	for _, u := range order {
		if u.SubExpireAt != "" {
			d.WithSub++
		}
		if u.BalanceKopecks > 0 {
			d.WithBalance++
		}
		d.Users = append(d.Users, *u)
	}

	applyPromos(tables["promocodes"], tables["promocode_activations"], b, d)
	applyTransactions(tables["transactions"], b, d)

	return d, nil
}

// applySubscriptions проставляет каждому пользователю самый поздний срок
// действия подписки. Статус DELETED игнорируем — такой подписки уже нет.
func applySubscriptions(st *Table, b binder, d *Data) {
	if st == nil {
		d.Warnings = append(d.Warnings, "в дампе нет таблицы subscriptions — сроки подписок не перенесутся")
		return
	}
	userIdx := st.Col("user_id")
	expIdx := st.Col("expire_at")
	statusIdx := st.Col("status")
	if userIdx < 0 || expIdx < 0 {
		d.Warnings = append(d.Warnings, "в subscriptions нет ссылки на пользователя или expire_at — сроки подписок пропущены")
		return
	}
	for _, row := range st.Rows {
		u := b.user(row, userIdx)
		if u == nil {
			continue
		}
		if strings.EqualFold(cellStr(row, statusIdx), "DELETED") {
			continue
		}
		exp := cellTS(row, expIdx)
		if exp == "" {
			continue
		}
		if u.SubExpireAt == "" || exp > u.SubExpireAt {
			u.SubExpireAt = exp
		}
		// Была подписка — значит, стартовый триал у нас тоже считается
		// использованным (иначе после переезда его выдадут второй раз).
		u.TrialUsed = true
	}
}

// applyReferrals переносит связи «кто кого привёл» и сумму уже выплаченных
// реферальных наград.
func applyReferrals(rt, rw *Table, b binder, d *Data) {
	if rt != nil {
		refByIdx := rt.Col("referrer_id")
		refToIdx := rt.Col("referred_id")
		if refByIdx >= 0 && refToIdx >= 0 {
			for _, row := range rt.Rows {
				referrer := b.user(row, refByIdx)
				referred := b.user(row, refToIdx)
				if referrer == nil || referred == nil || referrer == referred {
					continue
				}
				referred.ReferredBy = referrer.TelegramID
				d.Referrals++
			}
		}
	}

	if rw == nil {
		return
	}
	// referral_rewards в remnashop хранит награды и в баллах, и в днях;
	// «заработано» у нас в деньгах, поэтому берём только баллы.
	userIdx := rw.Col("user_id")
	typeIdx := rw.Col("type")
	amtIdx := rw.Col("amount")
	issuedIdx := rw.Col("is_issued")
	if userIdx < 0 || amtIdx < 0 {
		return
	}
	earned := map[*User]int64{}
	for _, row := range rw.Rows {
		u := b.user(row, userIdx)
		if u == nil {
			continue
		}
		if typeIdx >= 0 && !strings.EqualFold(cellStr(row, typeIdx), "POINTS") {
			continue
		}
		if issuedIdx >= 0 && !cellBool(row, issuedIdx) {
			continue
		}
		amt, ok := cellInt(row, amtIdx)
		if !ok || amt <= 0 {
			continue
		}
		earned[u] += amt * 100
	}
	for u, sum := range earned {
		if sum > u.RefEarnedKopecks {
			u.RefEarnedKopecks = sum
		}
	}
}

// applyPromos переносит промокоды. У нас всего два вида награды (дни и баланс),
// поэтому переносим DURATION и SUBSCRIPTION (по длительности из снимка плана),
// а про остальные честно пишем в предупреждениях.
func applyPromos(pt, at *Table, b binder, d *Data) {
	if pt == nil {
		return
	}
	idIdx := pt.Col("id")
	codeIdx := pt.Col("code")
	activeIdx := pt.Col("is_active")
	rtIdx := pt.Col("reward_type")
	rewardIdx := pt.Col("reward")
	snapIdx := pt.Col("plan_snapshot")
	expIdx := pt.Col("expires_at")
	maxIdx := pt.Col("max_activations")
	createdIdx := pt.Col("created_at")
	if codeIdx < 0 {
		return
	}

	codeByID := map[int64]string{}
	skipped := map[string]int{}
	for _, row := range pt.Rows {
		code := strings.ToUpper(strings.TrimSpace(cellStr(row, codeIdx)))
		if code == "" {
			continue
		}
		id, _ := cellInt(row, idIdx)
		kind, value := promoReward(cellStr(row, rtIdx), row, rewardIdx, snapIdx)
		if kind == "" {
			skipped[strings.ToUpper(cellStr(row, rtIdx))]++
			continue
		}
		if activeIdx >= 0 && !cellBool(row, activeIdx) {
			skipped["INACTIVE"]++
			continue
		}
		maxUses := 0
		if v, ok := cellInt(row, maxIdx); ok {
			maxUses = int(v)
		}
		codeByID[id] = code
		d.Promos = append(d.Promos, Promo{
			Code:      code,
			Kind:      kind,
			Value:     value,
			MaxUses:   maxUses,
			ExpiresAt: cellTS(row, expIdx),
			CreatedAt: cellTS(row, createdIdx),
		})
	}
	for kind, n := range skipped {
		label := kind
		if label == "" {
			label = "без типа"
		}
		d.Warnings = append(d.Warnings, fmt.Sprintf("промокодов пропущено (%s): %d", label, n))
	}

	if at == nil {
		return
	}
	pidIdx := at.Col("promocode_id")
	userIdx := at.Col("user_id")
	tsIdx := at.Col("activated_at")
	if pidIdx < 0 || userIdx < 0 {
		return
	}
	for _, row := range at.Rows {
		pid, ok := cellInt(row, pidIdx)
		if !ok {
			continue
		}
		code, okC := codeByID[pid]
		u := b.user(row, userIdx)
		if !okC || u == nil {
			continue
		}
		d.PromoUses = append(d.PromoUses, PromoUse{Code: code, TelegramID: u.TelegramID, CreatedAt: cellTS(row, tsIdx)})
	}
}

func promoReward(rewardType string, row []*string, rewardIdx, snapIdx int) (kind string, value int) {
	switch strings.ToUpper(strings.TrimSpace(rewardType)) {
	case "DURATION":
		if v, ok := cellInt(row, rewardIdx); ok && v > 0 {
			return "days", int(v)
		}
	case "SUBSCRIPTION":
		if snapIdx >= 0 && snapIdx < len(row) && row[snapIdx] != nil {
			if days := snapshotDuration(*row[snapIdx]); days > 0 {
				return "days", days
			}
		}
	}
	return "", 0
}

func snapshotDuration(raw string) int {
	var snap struct {
		Duration int `json:"duration"`
	}
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return 0
	}
	return snap.Duration
}

// applyTransactions переносит завершённые платежи в нашу историю.
func applyTransactions(tt *Table, b binder, d *Data) {
	if tt == nil {
		return
	}
	userIdx := tt.Col("user_id")
	statusIdx := tt.Col("status")
	gwIdx := tt.Col("gateway_type")
	priceIdx := tt.Col("pricing")
	snapIdx := tt.Col("plan_snapshot")
	payIdx := tt.Col("payment_id")
	curIdx := tt.Col("currency")
	createdIdx := tt.Col("created_at")
	testIdx := tt.Col("is_test")
	if userIdx < 0 || statusIdx < 0 {
		return
	}
	for _, row := range tt.Rows {
		if !strings.EqualFold(cellStr(row, statusIdx), "COMPLETED") {
			continue
		}
		if testIdx >= 0 && cellBool(row, testIdx) {
			continue
		}
		u := b.user(row, userIdx)
		if u == nil {
			continue
		}
		method := strings.ToUpper(strings.TrimSpace(cellStr(row, gwIdx)))
		if m, ok := gatewayMethods[method]; ok {
			method = m
		} else {
			method = strings.ToLower(method)
		}
		ext := cellStr(row, payIdx)
		if ext == "" {
			continue
		}
		months, days := monthsFromSnapshot(row, snapIdx)
		d.Payments = append(d.Payments, Payment{
			TelegramID: u.TelegramID,
			Method:     method,
			Months:     months,
			Days:       days,
			Amount:     amountString(row, priceIdx, cellStr(row, curIdx)),
			ExtID:      "rs:" + ext,
			CreatedAt:  cellTS(row, createdIdx),
		})
	}
	sort.SliceStable(d.Payments, func(i, j int) bool { return d.Payments[i].CreatedAt < d.Payments[j].CreatedAt })
}

// monthsFromSnapshot — срок сделки из снимка дампа: месяцы огрублением
// (дни+15)/30 (столько понимает остальной бот) и исходные дни — они уезжают в
// снимок сделки, чтобы округление не теряло правду об истории.
func monthsFromSnapshot(row []*string, snapIdx int) (months, days int) {
	if snapIdx < 0 || snapIdx >= len(row) || row[snapIdx] == nil {
		return 0, 0
	}
	days = snapshotDuration(*row[snapIdx])
	if days <= 0 {
		return 0, 0
	}
	months = (days + 15) / 30
	if months < 1 {
		months = 1
	}
	return months, days
}

// amountString достаёт итоговую сумму из JSON-поля pricing.
func amountString(row []*string, priceIdx int, currency string) string {
	amount := ""
	if priceIdx >= 0 && priceIdx < len(row) && row[priceIdx] != nil {
		var p struct {
			FinalAmount json.Number `json:"final_amount"`
		}
		if err := json.Unmarshal([]byte(*row[priceIdx]), &p); err == nil {
			amount = strings.TrimSpace(p.FinalAmount.String())
		}
	}
	if amount == "" {
		return ""
	}
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "RUB":
		return amount + " ₽"
	case "USD":
		return amount + " $"
	case "XTR":
		return amount + " ⭐"
	case "":
		return amount
	default:
		return amount + " " + strings.ToUpper(currency)
	}
}

func cellStr(row []*string, idx int) string {
	if idx < 0 || idx >= len(row) || row[idx] == nil {
		return ""
	}
	return strings.TrimSpace(*row[idx])
}

func cellInt(row []*string, idx int) (int64, bool) {
	s := cellStr(row, idx)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func cellBool(row []*string, idx int) bool {
	switch strings.ToLower(cellStr(row, idx)) {
	case "t", "true", "1", "yes":
		return true
	}
	return false
}

// cellTS приводит метку времени Postgres к RFC3339 в UTC — в этом виде даты
// лежат в нашей базе.
func cellTS(row []*string, idx int) string {
	s := cellStr(row, idx)
	if s == "" {
		return ""
	}
	layouts := []string{
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05.999999Z07:00",
		"2006-01-02 15:04:05.999999",
		"2006-01-02T15:04:05.999999Z07:00",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

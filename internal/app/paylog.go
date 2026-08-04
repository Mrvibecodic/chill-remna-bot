package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func (a *App) payLog(ctx context.Context, method, extID string, telegramID int64, stage, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	// Журналирование не имеет права ронять платёжный путь: логгер может быть не
	// задан (ранний старт, тестовые сборки), а payLog теперь вызывается в том
	// числе из отказных веток оплаты.
	if a.log != nil {
		a.log.Info("paylog", "method", method, "ext_id", extID, "tg_id", telegramID, "stage", stage, "detail", detail)
	}
	if a.store == nil {
		return
	}
	_ = a.store.AddPayLog(ctx, &model.PayLogEntry{
		ExtID:      extID,
		TelegramID: telegramID,
		Method:     method,
		Stage:      stage,
		Detail:     detail,
	})
}

// payLogThrottled пишет запись не чаще раза в 10 минут на ключ. Применяется к
// отказам НЕаутентифицированных вебхуков: сканер, долбящий /webhook/*, иначе
// заливал бы payment_log тысячами одинаковых строк на 90 дней ретенции.
func (a *App) payLogThrottled(ctx context.Context, key, method, extID string, telegramID int64, stage, format string, args ...any) {
	const interval = 10 * time.Minute
	now := time.Now()
	a.thrMu.Lock()
	if a.thrLast == nil {
		a.thrLast = map[string]time.Time{}
	}
	last, seen := a.thrLast[key]
	allow := !seen || now.Sub(last) >= interval
	if allow {
		a.thrLast[key] = now
	}
	a.thrMu.Unlock()
	if allow {
		a.payLog(ctx, method, extID, telegramID, stage, format, args...)
	}
}

func (a *App) adminSendPayLog(ctx context.Context, chatID int64, query string) {
	lang := a.lang(chatID)
	query = strings.TrimSpace(query)
	if query == "" || a.store == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "paylog.empty", query))
		return
	}
	var tg int64
	if n, err := strconv.ParseInt(query, 10, 64); err == nil {
		tg = n
	}
	const payLogQueryLimit = 2000
	entries, err := a.store.PayLogs(ctx, query, tg, payLogQueryLimit)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	if len(entries) == 0 {
		a.sendHome(ctx, chatID, i18n.T(lang, "paylog.empty", query))
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "payment log · query=%s · entries=%d · generated=%s\n\n", query, len(entries), time.Now().UTC().Format(time.RFC3339))
	for _, e := range entries {
		fmt.Fprintf(&sb, "%s [%s] ext=%s tg=%d %s: %s\n", e.CreatedAt, e.Method, e.ExtID, e.TelegramID, e.Stage, e.Detail)
	}
	name := "paylog_" + sanitizeFileName(query) + ".log"
	caption := i18n.T(lang, "paylog.caption", query, len(entries))
	if len(entries) == payLogQueryLimit {
		caption += "\n" + i18n.T(lang, "paylog.limit_hit", payLogQueryLimit)
	}
	a.msg.SendDocument(ctx, chatID, name, []byte(sb.String()), caption)
}

func sanitizeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "query"
	}
	return b.String()
}

// payErrorStages — этапы, означающие сбой оплаты. Держим списком, а не по
// подстроке «error»: «sign_mismatch» и «precheckout_rejected» ошибками тоже
// являются, а «verified» — нет.
var payErrorStages = map[string]bool{
	"error":                true,
	"invoice_error":        true,
	"verify_error":         true,
	"sign_error":           true,
	"sign_mismatch":        true,
	"checkout_error":       true,
	"topup_error":          true,
	"finalize_error":       true,
	"panel_error":          true,
	"reconcile_error":      true,
	"reconcile_giveup":     true,
	"autocharge_error":     true,
	"autopay_disabled":     true,
	"precheckout_rejected": true,
}

const (
	// Пределы выгрузки. На нагруженном боте журнал растёт быстрее всех таблиц,
	// поэтому и сводка, и файл ограничены — но усечение всегда объявляется, а
	// общее число берётся из БД, а не из длины среза.
	payErrSummaryLimit = 5000
	payErrFileLimit    = 20000
	payErrFileMaxBytes = 6 << 20 // файл больше этого бесполезен для разбора
)

func payErrorStageList() []string {
	out := make([]string, 0, len(payErrorStages))
	for st := range payErrorStages {
		out = append(out, st)
	}
	sort.Strings(out)
	return out
}

// payWindowSince переводит окно в днях в границу времени. 0 — без границы.
func payWindowSince(days int) string {
	if days <= 0 {
		return ""
	}
	return time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
}

// showPayErrors показывает СВОДКУ по сбоям, а не вываливает файл: на живом боте
// в файле легко оказывается сто тысяч строк, а ответ на вопрос «что сломалось»
// обычно виден по разбивке способ+этап. Файл — отдельными кнопками с окном.
func (a *App) showPayErrors(ctx context.Context, chatID int64, days int) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	entries, total, err := a.store.PayLogsFiltered(ctx, payErrorStageList(), payWindowSince(days), payErrSummaryLimit)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "paylog.btn_win_1"), "pay:errf:1"), btn(i18n.T(lang, "paylog.btn_win_7"), "pay:errf:7")},
		{btn(i18n.T(lang, "paylog.btn_win_all"), "pay:errf:0")},
		navBack(lang, "menu:payments"),
	}
	if total == 0 {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "paylog.no_errors"), [][]models.InlineKeyboardButton{navBack(lang, "menu:payments")})
		return
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "paylog.errors_summary", days, total, payErrSummaryText(entries, total)), rows)
}

// payErrSummaryText — разбивка «способ · этап · сколько», по убыванию.
func payErrSummaryText(entries []model.PayLogEntry, total int64) string {
	type key struct{ method, stage string }
	cnt := map[key]int{}
	for _, e := range entries {
		cnt[key{e.Method, e.Stage}]++
	}
	keys := make([]key, 0, len(cnt))
	for k := range cnt {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if cnt[keys[i]] != cnt[keys[j]] {
			return cnt[keys[i]] > cnt[keys[j]]
		}
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].stage < keys[j].stage
	})
	var sb strings.Builder
	sb.WriteString("<pre>")
	for i, k := range keys {
		if i >= 15 {
			fmt.Fprintf(&sb, "…ещё %d сочетаний\n", len(keys)-i)
			break
		}
		fmt.Fprintf(&sb, "%-10s %-20s %d\n", escapeName(methodLabel(k.method)), k.stage, cnt[k])
	}
	sb.WriteString("</pre>")
	if int64(len(entries)) < total {
		fmt.Fprintf(&sb, "\n⚠️ Сводка построена по последним %d записям из %d.", len(entries), total)
	}
	return sb.String()
}

// exportPayErrors отдаёт файлом сбои за выбранное окно.
func (a *App) exportPayErrors(ctx context.Context, chatID int64, days int) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	entries, total, err := a.store.PayLogsFiltered(ctx, payErrorStageList(), payWindowSince(days), payErrFileLimit)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	if total == 0 {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "paylog.no_errors"),
			[][]models.InlineKeyboardButton{navBack(lang, "menu:payments")})
		return
	}
	report, n := buildPayErrorReport(entries, total, days, time.Now().UTC().Format(time.RFC3339))
	a.msg.SendDocument(ctx, chatID, "payment_errors.log", []byte(report), i18n.T(lang, "paylog.errors_caption", n, total))
}

// buildPayErrorReport собирает отчёт по сбоям: сами записи плюс сводка по
// способам оплаты, чтобы сразу было видно, какой шлюз сыплется.
func buildPayErrorReport(entries []model.PayLogEntry, total int64, days int, generated string) (string, int) {
	window := "всё время"
	if days > 0 {
		window = fmt.Sprintf("последние %d сут", days)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "payment errors · окно=%s · всего подходит=%d · в файле=%d · generated=%s\n",
		window, total, len(entries), generated)
	if int64(len(entries)) < total {
		fmt.Fprintf(&sb, "ВНИМАНИЕ: показаны последние %d из %d записей — сузьте окно, чтобы увидеть остальные.\n", len(entries), total)
	}
	sb.WriteString("\n")
	n := 0
	written := 0
	truncated := false
	byMethod := map[string]int{}
	for _, e := range entries {
		if !payErrorStages[e.Stage] {
			continue
		}
		n++
		byMethod[e.Method]++
		if sb.Len() < payErrFileMaxBytes {
			written++
			fmt.Fprintf(&sb, "%s [%s] ext=%s tg=%d %s: %s\n", e.CreatedAt, e.Method, e.ExtID, e.TelegramID, e.Stage, e.Detail)
		} else if !truncated {
			truncated = true
			sb.WriteString("… файл обрезан по размеру, сводка ниже посчитана по всем отобранным записям\n")
		}
	}
	if n == 0 {
		return "", 0
	}
	// В подпись идёт число строк, реально попавших в файл, а не отобранных.
	n = written
	sb.WriteString("\nпо способам оплаты:\n")
	methods := make([]string, 0, len(byMethod))
	for m := range byMethod {
		methods = append(methods, m)
	}
	sort.Strings(methods)
	for _, m := range methods {
		fmt.Fprintf(&sb, "  %s: %d\n", m, byMethod[m])
	}
	return sb.String(), n
}

// exportPayLogCSV sends the full payment log (all sources, all stages, including
// incomplete) as a CSV document for offline analysis.
func (a *App) exportPayLogCSV(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	entries, total, err := a.store.PayLogsFiltered(ctx, nil, "", 50000)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM for Excel
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"id", "created_at", "method", "stage", "ext_id", "telegram_id", "detail"})
	for _, e := range entries {
		_ = w.Write([]string{
			strconv.FormatInt(e.ID, 10), e.CreatedAt, e.Method, e.Stage, e.ExtID,
			strconv.FormatInt(e.TelegramID, 10), e.Detail,
		})
	}
	w.Flush()
	caption := i18n.T(lang, "paylog.csv_caption", len(entries))
	if int64(len(entries)) < total {
		caption += "\n" + i18n.T(lang, "paylog.truncated", len(entries), total)
	}
	a.msg.SendDocument(ctx, chatID, "payments_log.csv", buf.Bytes(), caption)
}

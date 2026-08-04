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
	entries, err := a.store.PayLogs(ctx, query, tg, 2000)
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
	a.msg.SendDocument(ctx, chatID, name, []byte(sb.String()), i18n.T(lang, "paylog.caption", query, len(entries)))
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

// exportPayErrors отдаёт только сбойные записи журнала — то, ради чего админ
// обычно и лезет в логи. Полный CSV остаётся отдельной кнопкой.
func (a *App) exportPayErrors(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	entries, err := a.store.AllPayLogs(ctx, 50000)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	report, n := buildPayErrorReport(entries, time.Now().UTC().Format(time.RFC3339))
	if n == 0 {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "paylog.no_errors"),
			[][]models.InlineKeyboardButton{navBack(lang, "menu:payments")})
		return
	}
	a.msg.SendDocument(ctx, chatID, "payment_errors.log", []byte(report), i18n.T(lang, "paylog.errors_caption", n))
}

// buildPayErrorReport собирает отчёт по сбоям: сами записи плюс сводка по
// способам оплаты, чтобы сразу было видно, какой шлюз сыплется.
func buildPayErrorReport(entries []model.PayLogEntry, generated string) (string, int) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "payment errors · generated=%s\n\n", generated)
	n := 0
	byMethod := map[string]int{}
	for _, e := range entries {
		if !payErrorStages[e.Stage] {
			continue
		}
		n++
		byMethod[e.Method]++
		fmt.Fprintf(&sb, "%s [%s] ext=%s tg=%d %s: %s\n", e.CreatedAt, e.Method, e.ExtID, e.TelegramID, e.Stage, e.Detail)
	}
	if n == 0 {
		return "", 0
	}
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
	entries, err := a.store.AllPayLogs(ctx, 50000)
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
	a.msg.SendDocument(ctx, chatID, "payments_log.csv", buf.Bytes(), i18n.T(lang, "paylog.csv_caption", len(entries)))
}

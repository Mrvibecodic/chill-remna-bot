package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
)

var methodLabels = map[string]string{
	"p2p":       "P2P",
	"yookassa":  "ЮKassa",
	"stars":     "Stars",
	"cryptobot": "CryptoBot",
	"platega":   "Platega",
	"tribute":   "Tribute",
	"balance":   "Balance",
}

func methodLabel(m string) string {
	if l, ok := methodLabels[m]; ok {
		return l
	}
	if m == "" {
		return "—"
	}
	return m
}

func (a *App) showAnalytics(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	pays, _ := a.store.PaidPayments(ctx)
	now := time.Now().UTC()
	d7 := now.AddDate(0, 0, -7)
	d30 := now.AddDate(0, 0, -30)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var revAll, rev7, rev30, revToday float64
	byMethod := map[string]int{}
	payers := map[int64]bool{}
	for _, p := range pays {
		amt := parseAmountRub(p.Amount)
		revAll += amt
		byMethod[p.Method]++
		payers[p.TelegramID] = true
		if t, err := time.Parse(time.RFC3339, p.CreatedAt); err == nil {
			if t.After(d7) {
				rev7 += amt
			}
			if t.After(d30) {
				rev30 += amt
			}
			if t.After(dayStart) {
				revToday += amt
			}
		}
	}

	totalUsers := 0
	if ids, err := a.store.AllUserIDs(ctx); err == nil {
		totalUsers = len(ids)
	}
	conv := 0.0
	if totalUsers > 0 {
		conv = float64(len(payers)) / float64(totalUsers) * 100
	}
	popMonths, popCount, _ := a.store.MostPopularPlan(ctx)

	methods := make([]string, 0, len(byMethod))
	for m := range byMethod {
		methods = append(methods, m)
	}
	sort.Slice(methods, func(i, j int) bool { return byMethod[methods[i]] > byMethod[methods[j]] })
	var mb strings.Builder
	for _, m := range methods {
		fmt.Fprintf(&mb, "• %s: %d\n", methodLabel(m), byMethod[m])
	}
	methodsStr := strings.TrimRight(mb.String(), "\n")
	if methodsStr == "" {
		methodsStr = "—"
	}

	text := i18n.T(lang, "stats.title",
		revToday, rev7, rev30, revAll,
		len(pays), len(payers), totalUsers, conv,
		popMonths, popCount, methodsStr)
	a.sendKBSection(ctx, chatID, assets.SectionAdminStats, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "stats.btn_reload"), "menu:analytics")},
		navBack(lang, "menu:manage"),
	})
}

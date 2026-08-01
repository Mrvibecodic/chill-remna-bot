package app

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/rsimport"
)

// maxDumpBytes — Telegram и так не отдаёт боту файлы больше 20 МБ, но лишний
// предохранитель дешевле, чем распухшая память процесса.
const maxDumpBytes = 20 << 20

// dumpDownloadTimeout не даёт зависшей загрузке держать горутину до перезапуска.
const dumpDownloadTimeout = 2 * time.Minute

// showRSImport — экран мастера переезда с remnashop.
func (a *App) showRSImport(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if d := a.rsDumpPeek(chatID); d != nil {
		a.showRSPreview(ctx, chatID, d)
		return
	}
	a.sendSysKB(ctx, chatID, i18n.T(lang, "rsimp.title"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "rsimp.btn_upload"), "rsimp:up")},
		{btn(i18n.T(lang, "btn.back"), "menu:system"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// rsDumpSet кладёт разобранный дамп до подтверждения импорта.
func (a *App) rsDumpSet(chatID int64, d *rsimport.Data) {
	a.rsMu.Lock()
	defer a.rsMu.Unlock()
	if a.rsDump == nil {
		a.rsDump = map[int64]*rsimport.Data{}
	}
	a.rsDump[chatID] = d
}

func (a *App) rsDumpPeek(chatID int64) *rsimport.Data {
	a.rsMu.Lock()
	defer a.rsMu.Unlock()
	return a.rsDump[chatID]
}

// rsDumpTake забирает дамп и снимает его с ожидания: подтвердить импорт
// дважды одной и той же кнопкой нельзя.
func (a *App) rsDumpTake(chatID int64) *rsimport.Data {
	a.rsMu.Lock()
	defer a.rsMu.Unlock()
	d := a.rsDump[chatID]
	delete(a.rsDump, chatID)
	return d
}

func (a *App) onRSImport(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	switch val {
	case "up":
		ui.awaitRSDump = true
		a.sendSysKB(ctx, chatID, i18n.T(lang, "rsimp.await"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.back"), "menu:rsimp")},
		})
	case "cancel":
		ui.awaitRSDump = false
		a.rsDumpTake(chatID)
		a.showRSImport(ctx, chatID)
	case "apply":
		d := a.rsDumpTake(chatID)
		if d == nil {
			a.showRSImport(ctx, chatID)
			return
		}
		ui.awaitRSDump = false
		a.sendSysKB(ctx, chatID, i18n.T(lang, "rsimp.running"), nil)
		bg := a.bgContext()
		a.spawn(func() {
			rep := a.applyRSImport(bg, d)
			a.sendSysKB(bg, chatID, a.rsReportText(lang, d, rep), [][]models.InlineKeyboardButton{
				{btn(i18n.T(lang, "btn.back"), "menu:system"), btn(i18n.T(lang, "btn.home"), "menu:home")},
			})
		})
	default:
		a.showRSImport(ctx, chatID)
	}
}

// handleDocument принимает файл дампа. Другие документы боту не нужны.
func (a *App) handleDocument(ctx context.Context, m *models.Message) {
	chatID := m.Chat.ID
	if m.From == nil || m.From.ID != a.cfg.AdminID {
		return
	}
	ui := a.getUI(chatID)
	if !ui.awaitRSDump {
		return
	}
	ui.awaitRSDump = false
	lang := a.lang(chatID)
	fileID := m.Document.FileID

	if m.Document.FileSize > maxDumpBytes {
		a.sendSysKB(ctx, chatID, i18n.T(lang, "rsimp.too_big"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.back"), "menu:rsimp")},
		})
		return
	}

	a.sendSysKB(ctx, chatID, i18n.T(lang, "rsimp.parsing"), nil)

	// Скачивание и разбор — в фоне: апдейты обрабатываются одним воркером, и
	// на большом дампе бот иначе перестал бы отвечать всем остальным.
	bg := a.bgContext()
	a.spawn(func() {
		dlCtx, cancel := context.WithTimeout(bg, dumpDownloadTimeout)
		defer cancel()
		data, err := a.msg.Download(dlCtx, fileID)
		if err != nil {
			a.log.Warn("rsimport: скачивание дампа", "err", err)
			a.rsFail(bg, chatID, i18n.T(lang, "rsimp.download_fail", err.Error()))
			return
		}
		d, err := rsimport.Load(bytes.NewReader(data))
		if err != nil {
			a.log.Warn("rsimport: разбор дампа", "err", err)
			a.rsFail(bg, chatID, i18n.T(lang, "rsimp.parse_fail", err.Error()))
			return
		}
		a.rsDumpSet(chatID, d)
		a.showRSPreview(bg, chatID, d)
	})
}

func (a *App) rsFail(ctx context.Context, chatID int64, text string) {
	lang := a.lang(chatID)
	a.sendSysKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.back"), "menu:rsimp")},
	})
}

func (a *App) showRSPreview(ctx context.Context, chatID int64, d *rsimport.Data) {
	lang := a.lang(chatID)
	text := i18n.T(lang, "rsimp.preview",
		d.TotalUsers, len(d.Users), d.SkippedWeb, d.WithSub, d.WithBalance,
		d.Referrals, len(d.Promos), len(d.PromoUses), len(d.Payments))
	if len(d.Warnings) > 0 {
		text += "\n\n⚠️ " + strings.Join(d.Warnings, "\n⚠️ ")
	}
	a.sendSysKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "rsimp.btn_apply"), "rsimp:apply")},
		{btn(i18n.T(lang, "rsimp.btn_cancel"), "rsimp:cancel")},
	})
}

// rsReport — что именно сделал импорт.
type rsReport struct {
	usersNew   int
	usersFound int
	subs       int
	trials     int
	blocked    int
	balance    int
	refs       int
	promos     int
	promoUses  int
	payments   int
	errs       int
}

// applyRSImport переносит данные в нашу базу.
//
// Повторный импорт того же дампа безопасен: пользователей мы только дополняем
// (никогда не перетираем то, что уже накопилось у нас), деньги начисляем ровно
// один раз — при первом появлении пользователя, а платежи защищены уникальным
// ext_id вида rs:<payment_id>.
func (a *App) applyRSImport(ctx context.Context, d *rsimport.Data) rsReport {
	var rep rsReport
	if a.store == nil {
		return rep
	}

	for i := range d.Users {
		u := d.Users[i]
		if u.TelegramID == 0 {
			continue
		}
		existing, _ := a.store.GetUser(ctx, u.TelegramID)
		fresh := existing == nil
		if err := a.store.UpsertUser(ctx, u.TelegramID); err != nil {
			rep.errs++
			continue
		}
		if fresh {
			rep.usersNew++
		} else {
			rep.usersFound++
		}

		if (u.Username != "" || u.FirstName != "") && (fresh || existing.Username == "") {
			_ = a.store.SetUserInfo(ctx, u.TelegramID, u.Username, u.FirstName)
		}
		// Блокировку переносим только на заведённых импортом: у того, кто уже
		// пользуется нашим ботом, отбирать доступ из-за старой записи нельзя.
		if fresh && u.Blocked {
			if err := a.store.SetBlocked(ctx, u.TelegramID, true); err == nil {
				rep.blocked++
			}
		}
		if u.TrialUsed && (fresh || existing.TrialUsedAt == "") {
			ts := u.CreatedAt
			if ts == "" {
				ts = time.Now().UTC().Format(time.RFC3339)
			}
			if err := a.store.SetTrialUsed(ctx, u.TelegramID, ts); err == nil {
				rep.trials++
			}
		}
		// Срок подписки не укорачиваем: если у нас уже записан более поздний,
		// он и остаётся.
		if u.SubExpireAt != "" && (fresh || existing.SubExpireAt < u.SubExpireAt) {
			if err := a.store.SetSubExpiry(ctx, u.TelegramID, u.SubExpireAt, "paid"); err == nil {
				rep.subs++
			}
			a.invalidateSubCache(u.TelegramID)
		}
		if fresh && u.BalanceKopecks > 0 {
			if err := a.store.AddBalance(ctx, u.TelegramID, u.BalanceKopecks); err == nil {
				rep.balance++
			}
		}
		if fresh && u.RefEarnedKopecks > 0 {
			_ = a.store.AddRefEarned(ctx, u.TelegramID, u.RefEarnedKopecks)
		}
		if u.ReferredBy != 0 && (fresh || existing.ReferredBy == 0) {
			if err := a.store.SetReferredBy(ctx, u.TelegramID, u.ReferredBy); err == nil {
				rep.refs++
			}
			// Бонус за этого реферала уже выплачен в remnashop — второй раз
			// платить не за что.
			_ = a.store.SetRefBonusPaid(ctx, u.TelegramID)
		}
	}

	for _, p := range d.Promos {
		if existing, _ := a.store.GetPromo(ctx, p.Code); existing != nil {
			continue
		}
		kind := model.PromoKindBalance
		if p.Kind == "days" {
			kind = model.PromoKindDays
		}
		if err := a.store.CreatePromo(ctx, &model.PromoCode{
			Code:      p.Code,
			Kind:      kind,
			Value:     p.Value,
			MaxUses:   p.MaxUses,
			ExpiresAt: p.ExpiresAt,
			CreatedAt: p.CreatedAt,
		}); err == nil {
			rep.promos++
		} else {
			rep.errs++
		}
	}

	for _, use := range d.PromoUses {
		if done, _ := a.store.PromoRedeemedBy(ctx, use.Code, use.TelegramID); done {
			continue
		}
		if p, _ := a.store.GetPromo(ctx, use.Code); p == nil {
			continue
		}
		if err := a.store.RedeemPromo(ctx, use.Code, use.TelegramID); err == nil {
			rep.promoUses++
		}
	}

	for _, p := range d.Payments {
		if seen, _ := a.store.PaymentByExtID(ctx, p.ExtID); seen {
			continue
		}
		if err := a.store.AddPayment(ctx, &model.Payment{
			TelegramID: p.TelegramID,
			Method:     p.Method,
			Months:     p.Months,
			Amount:     p.Amount,
			Status:     model.PaymentPaid,
			Comment:    "remnashop",
			ExtID:      p.ExtID,
			CreatedAt:  p.CreatedAt,
		}); err == nil {
			rep.payments++
		}
	}

	a.log.Info("rsimport: импорт завершён",
		"users_new", rep.usersNew, "users_found", rep.usersFound, "subs", rep.subs,
		"promos", rep.promos, "payments", rep.payments, "errors", rep.errs)
	return rep
}

func (a *App) rsReportText(lang string, d *rsimport.Data, rep rsReport) string {
	text := i18n.T(lang, "rsimp.report",
		rep.usersNew, rep.usersFound, rep.subs, rep.trials, rep.blocked,
		rep.balance, rep.refs, rep.promos, rep.promoUses, rep.payments)
	if rep.errs > 0 {
		text += "\n" + i18n.T(lang, "rsimp.report_errs", rep.errs)
	}
	if d.SkippedWeb > 0 {
		text += "\n" + i18n.T(lang, "rsimp.report_web", d.SkippedWeb)
	}
	return text + "\n\n" + i18n.T(lang, "rsimp.report_hint")
}

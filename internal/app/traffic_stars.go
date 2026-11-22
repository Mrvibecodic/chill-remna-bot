package app

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/storage"
)

const trafficStarsPrefix = "trf:"

func trafficStarsPayload(code, packID string, forID int64) string {
	if packID == "" {
		packID = "-"
	}
	return trafficStarsPrefix + code + ":" + packID + ":" + strconv.FormatInt(forID, 10)
}

func parseTrafficStars(payload string) (code, packID string, forID int64, ok bool) {
	if !strings.HasPrefix(payload, trafficStarsPrefix) {
		return "", "", 0, false
	}
	parts := strings.Split(strings.TrimPrefix(payload, trafficStarsPrefix), ":")
	if len(parts) != 3 || !model.ValidPlanCode(parts[0]) {
		return "", "", 0, false
	}
	code, packID = parts[0], parts[1]
	if packID == "-" {
		packID = ""
	} else if !model.ValidPackID(packID) {
		return "", "", 0, false
	}
	forID, _ = strconv.ParseInt(parts[2], 10, 64)
	return code, packID, forID, true
}

func (a *App) trafficStarsTerms(ctx context.Context, payerID int64, code, packID string) (*model.Plan, *model.PlanPack, int, bool) {
	p, pk, err := a.trafficDeal(ctx, payerID, code, packID)
	if err != nil || p == nil {
		return nil, nil, 0, false
	}
	stars := a.saleStars(&sale{Plan: p, Pack: pk})
	return p, pk, stars, stars > 0
}

const starsTitleMax = 32

func trafficStarsTitle(lang string, snap *model.PlanSnapshot) string {
	title := i18n.T(lang, "stars.title_traffic")
	if snap != nil && snap.PackID != "" {
		title = i18n.T(lang, "stars.title_pack", snap.PackGB)
	}
	return clampRunes(title, starsTitleMax)
}

func (a *App) sendTrafficStarsInvoice(ctx context.Context, chatID int64, s *sale) {
	lang := a.lang(chatID)
	amount := a.saleStars(s)
	if !a.starsConfig().Enabled || amount <= 0 {
		a.sendHome(ctx, chatID, i18n.T(lang, "stars.no_price"))
		return
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	snap := a.saleSnapshot(s)
	desc := trafficInvoiceDesc(lang, snap)
	a.msg.SendInvoice(ctx, chatID, trafficStarsTitle(lang, snap), desc, trafficStarsPayload(s.planCode(), s.packID(), chatID), "XTR", amount)
	a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_sent", "%s %s stars=%d", trafficPurpose(snap), snap.Code, amount)
}

func (a *App) trafficStarsLink(ctx context.Context, chatID int64, s *sale) (string, error) {
	amount := a.saleStars(s)
	if !a.starsConfig().Enabled || amount <= 0 {
		return "", errStarsUnavailable
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	snap := a.saleSnapshot(s)
	lang := a.lang(chatID)
	desc := trafficInvoiceDesc(lang, snap)
	link, err := a.msg.CreateInvoiceLink(ctx, trafficStarsTitle(lang, snap), desc, trafficStarsPayload(s.planCode(), s.packID(), chatID), "XTR", amount)
	if err != nil {
		a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_error", "%s %s stars=%d: %v", trafficPurpose(snap), snap.Code, amount, err)
		return "", err
	}
	a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_link", "%s %s stars=%d", trafficPurpose(snap), snap.Code, amount)
	return link, nil
}

func (a *App) trafficPreCheckout(ctx context.Context, q *models.PreCheckoutQuery) bool {
	code, packID, forID, ok := parseTrafficStars(q.InvoicePayload)
	if !ok && !strings.HasPrefix(q.InvoicePayload, trafficStarsPrefix) {
		return false
	}
	var fromID int64
	if q.From != nil {
		fromID = q.From.ID
	}
	reject := func(reason string) bool {
		a.payLog(ctx, model.PayMethodStars, "", fromID, "precheckout_rejected", "payload=%s total=%d", q.InvoicePayload, q.TotalAmount)
		a.msg.AnswerPreCheckout(ctx, q.ID, false, reason)
		return true
	}
	lang := a.lang(fromID)
	if !ok || !a.starsConfig().Enabled || fromID == 0 {
		return reject(i18n.T(lang, "stars.no_price"))
	}
	if forID != 0 && forID != fromID {
		if reason := a.starsForeignPayerRefusal(ctx, fromID); reason != "" {
			return reject(reason)
		}
	}
	if _, _, stars, ok := a.trafficStarsTerms(ctx, fromID, code, packID); !ok || stars != q.TotalAmount {
		return reject(i18n.T(lang, "stars.no_price"))
	}
	a.msg.AnswerPreCheckout(ctx, q.ID, true, "")
	return true
}

func (a *App) trafficStarsPaid(ctx context.Context, payerID int64, payload, chargeID string, total int, recon bool) bool {
	code, packID, _, ok := parseTrafficStars(payload)
	if !ok {
		return strings.HasPrefix(payload, trafficStarsPrefix)
	}
	amount := strconv.Itoa(total) + " ⭐"
	p, pk, stars, ok := a.trafficStarsTerms(ctx, payerID, code, packID)
	if !ok || stars != total {
		if recon {
			a.payLogThrottled(ctx, "stars-recon-"+chargeID, model.PayMethodStars, chargeID, payerID, "error",
				"сверка: оплата %d⭐ трафика не сопоставлена с условиями", total)
			return true
		}
		a.payLog(ctx, model.PayMethodStars, chargeID, payerID, "error", "оплата %d⭐ (%s) не совпала с условиями трафика — выдача не проводится", total, payload)
		alang := a.lang(a.cfg.AdminID)
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "admin.pay_no_terms_traffic", model.PayMethodStars+" "+chargeID, a.userLabelByID(ctx, payerID)))
		a.starsOfferRefund(ctx, payerID, chargeID, total)
		a.notify(ctx, payerID, i18n.T(a.lang(payerID), "pay.no_terms_traffic"))
		return true
	}
	if recon {
		a.payLog(ctx, model.PayMethodStars, chargeID, payerID, "reconcile", "оплата трафика найдена сверкой: %d ⭐", total)
	}
	snap := a.trafficSnapshotOf(p, pk)
	d, err := a.closeTrafficDeal(ctx, payerID, model.PayMethodStars, amount, chargeID, snap, false)
	switch {
	case errors.Is(err, storage.ErrDuplicateExtID):
		a.payLog(ctx, model.PayMethodStars, chargeID, payerID, "duplicate", "повторная доставка оплаты трафика — уже выдано")
		return true
	case err != nil:
		a.payLog(ctx, model.PayMethodStars, chargeID, payerID, "finalize_error", "%v", err)
		if !recon {
			a.starsOfferRefund(ctx, payerID, chargeID, total)
			a.notify(ctx, payerID, i18n.T(a.lang(payerID), "stars.fail", a.clientErr(ctx, payerID, "выдача по звёздам", err)))
		}
		return true
	}
	if d.manual {
		a.starsOfferRefund(ctx, payerID, chargeID, total)
	}
	if !d.refunded {
		a.notifyTrafficDone(ctx, payerID, snap, d)
	}
	return true
}

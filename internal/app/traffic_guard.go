package app

import (
	"context"
	"strings"
	"time"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

const trafficRollbackLimit = 40

func (a *App) trafficRollbackUsers(ctx context.Context) ([]model.User, error) {
	st := a.storeRef()
	if st == nil {
		return nil, nil
	}
	eternal, _ := time.Parse(time.RFC3339, remnawave.EternalExpire)
	var out []model.User
	const page = 500
	for offset := 0; ; offset += page {
		users, total, err := st.ListUsers(ctx, page, offset)
		if err != nil {
			return nil, err
		}
		for i := range users {
			u := &users[i]
			exp, perr := time.Parse(time.RFC3339, u.SubExpireAt)
			if perr != nil || !exp.After(eternal) || u.Snapshot.IsTraffic() {
				continue
			}
			if a.hadTrafficDeal(ctx, u.TelegramID) {
				out = append(out, *u)
			}
		}
		if len(users) < page || offset+page >= total {
			return out, nil
		}
	}
}

func (a *App) hadTrafficDeal(ctx context.Context, uid int64) bool {
	st := a.storeRef()
	if st == nil {
		return false
	}
	pays, _, err := st.ListUserPayments(ctx, uid, 200, 0)
	if err != nil {
		return false
	}
	for i := range pays {
		if pays[i].Status == model.PaymentPaid && pays[i].Snapshot.IsTraffic() {
			return true
		}
	}
	return false
}

func (a *App) noticeTrafficRollback(ctx context.Context) {
	if a.cfg == nil || a.cfg.AdminID == 0 {
		return
	}
	users, err := a.trafficRollbackUsers(ctx)
	if err != nil {
		a.log.Warn("сторож отката: пользователи не прочитаны", "err", err)
		return
	}
	if len(users) == 0 {
		return
	}
	lang := a.lang(a.cfg.AdminID)
	var lines []string
	for i := range users {
		if i == trafficRollbackLimit {
			lines = append(lines, i18n.T(lang, "trg.more", len(users)-trafficRollbackLimit))
			break
		}
		u := &users[i]
		lines = append(lines, i18n.T(lang, "trg.line", a.userLabelFor(ctx, u), shortDate(u.SubExpireAt)))
	}
	a.log.Warn("сторож отката: срочные подписки со сроком после 2099", "count", len(users))
	a.notify(ctx, a.cfg.AdminID, i18n.T(lang, "trg.title", len(users))+"\n\n"+strings.Join(lines, "\n"))
}

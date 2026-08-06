package app

import (
	"context"
	"html"
	"strconv"
	"strings"
	"time"

	"remnabot/internal/i18n"
)

// errMaxLen — предел на текст ошибки панели в сообщении админу. Через
// escapeName их гнать нельзя: он режет по 48 символов (это эскейпер ИМЁН) и
// обрубает диагностику ровно там, где она нужна, — вплоть до разрыва тега.
const errMaxLen = 300

func escapeErr(err error) string {
	s := strings.TrimSpace(err.Error())
	if r := []rune(s); len(r) > errMaxLen {
		s = string(r[:errMaxLen]) + "…"
	}
	return html.EscapeString(s)
}

// Действия админа по отчёту торрент-блокера. Всё, что тут делается, идёт в
// панель: снятие блокировки — через Executor плагинов, исключение
// пользователя — правкой torrentBlocker.ignoreLists.userId в конфигах плагинов.
// Отключение подписки отдельной кнопки не имеет: отчёт ведёт в готовый
// сценарий usr:block, где админ выбирает, что именно блокировать.

// torrentUnblockIP снимает блокировку адреса на всех подключённых нодах.
// Раз блокировка снята раньше срока, ждать тикера нельзя: записи журнала по
// этому адресу закрываются сразу, а пользователю уходит «блокировка снята».
func (a *App) torrentUnblockIP(ctx context.Context, chatID int64, ip string) {
	lang := a.lang(chatID)
	ip = strings.TrimSpace(ip)
	panel := a.panelClient()
	if panel == nil {
		a.notify(ctx, chatID, i18n.T(lang, "torj.no_panel"))
		return
	}
	if err := panel.UnblockIP(ctx, ip); err != nil {
		a.log.Warn("торрент-блокер: снятие блокировки", "ip", ip, "err", err)
		a.notify(ctx, chatID, i18n.T(lang, "torj.unb_fail", escapeErr(err)))
		return
	}
	a.log.Info("торрент-блокер: блокировка снята вручную", "ip", ip)
	a.closeTorrentBlocks(ctx, ip)
	a.notify(ctx, chatID, i18n.T(lang, "torj.unb_ok", escapeName(ip)))
}

// closeTorrentBlocks помечает записи журнала по адресу отработанными и
// уведомляет пользователей — иначе тикер прислал бы «блокировка снята» ещё раз
// в исходный срок, когда это уже неправда.
func (a *App) closeTorrentBlocks(ctx context.Context, ip string) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return
	}
	pending, err := st.PendingTorrentUnblocksByIP(ctx, ip)
	if err != nil {
		a.log.Warn("торрент-блокер: выборка записей по IP", "err", err)
		return
	}
	notifyUser := a.torrentCfg().NotifyUser
	done := map[int64]bool{}
	for _, r := range pending {
		if err := st.MarkTorrentUnblockNotified(ctx, r.ID); err != nil {
			a.log.Warn("торрент-блокер: пометка разблокировки", "err", err)
			continue
		}
		if !notifyUser || r.TelegramID == 0 || done[r.TelegramID] {
			continue
		}
		if a.subDisabled(ctx, r.TelegramID) {
			continue
		}
		done[r.TelegramID] = true
		a.thrMu.Lock()
		if a.torUnbSeen == nil {
			a.torUnbSeen = map[int64]time.Time{}
		}
		a.torUnbSeen[r.TelegramID] = time.Now()
		a.thrMu.Unlock()
		a.sendTorrentUnblock(ctx, r.TelegramID)
	}
}

// torrentIgnoreUser добавляет пользователя панели в исключения торрент-блокера
// во всех конфигах плагинов, где эта секция есть.
func (a *App) torrentIgnoreUser(ctx context.Context, chatID int64, arg string) {
	lang := a.lang(chatID)
	uid, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
	if err != nil || uid <= 0 {
		a.notify(ctx, chatID, i18n.T(lang, "torj.ign_bad_id"))
		return
	}
	panel := a.panelClient()
	if panel == nil {
		a.notify(ctx, chatID, i18n.T(lang, "torj.no_panel"))
		return
	}
	changed, already, err := panel.TorrentIgnoreUser(ctx, uid)
	switch {
	case err != nil && changed > 0:
		// Часть конфигов уже изменена — молчать об этом нельзя, иначе админ
		// решит, что не применилось ничего.
		a.log.Warn("торрент-блокер: исключение применено частично", "user_id", uid, "configs", changed, "err", err)
		a.notify(ctx, chatID, i18n.T(lang, "torj.ign_partial", changed, escapeErr(err)))
	case err != nil:
		a.log.Warn("торрент-блокер: исключение пользователя", "user_id", uid, "err", err)
		a.notify(ctx, chatID, i18n.T(lang, "torj.ign_fail", escapeErr(err)))
	case already:
		a.notify(ctx, chatID, i18n.T(lang, "torj.ign_already", uid))
	case changed == 0:
		a.notify(ctx, chatID, i18n.T(lang, "torj.ign_none"))
	default:
		a.log.Info("торрент-блокер: пользователь в исключениях", "user_id", uid, "configs", changed)
		a.notify(ctx, chatID, i18n.T(lang, "torj.ign_ok", uid, changed))
	}
}

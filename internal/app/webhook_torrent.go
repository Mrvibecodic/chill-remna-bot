package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
)

// torrentUserCooldown — минимальная пауза между сообщениями одному
// пользователю (отдельно для предупреждений и для «блокировка снята»).
// Панель шлёт отчёт на каждую блокировку IP, а у пользователя может быть
// несколько устройств/адресов — без паузы его завалит одинаковыми
// сообщениями. Админ получает все отчёты без троттлинга.
const torrentUserCooldown = 10 * time.Minute

// torrentRepeatWindow — окно, за которое считаются повторные нарушения.
const torrentRepeatWindow = 30 * 24 * time.Hour

// torrentUnblockTick — период проверки, не пора ли слать «блокировка снята».
const torrentUnblockTick = 30 * time.Second

// torrentUnblockStale — если срок разблокировки прошёл давнее этого (бот был
// выключен), сообщение уже неактуально: запись помечается без отправки.
const torrentUnblockStale = 24 * time.Hour

// torrentRetention — сколько хранится журнал торрент-блокера.
const torrentRetention = 180 * 24 * time.Hour

// rwTorrentReport — payload события torrent_blocker.report: data содержит не
// объект пользователя (как у user.*), а {node, user, report}.
type rwTorrentReport struct {
	Node struct {
		Name        string `json:"name"`
		Address     string `json:"address"`
		CountryCode string `json:"countryCode"`
	} `json:"node"`
	User   rwUserPayload `json:"user"`
	Report struct {
		ActionReport struct {
			Blocked       bool   `json:"blocked"`
			IP            string `json:"ip"`
			BlockDuration int    `json:"blockDuration"`
			WillUnblockAt string `json:"willUnblockAt"`
			UserID        string `json:"userId"`
			ProcessedAt   string `json:"processedAt"`
		} `json:"actionReport"`
		XrayReport struct {
			Email       string `json:"email"`
			Protocol    string `json:"protocol"`
			Source      string `json:"source"`
			Destination string `json:"destination"`
			InboundTag  string `json:"inboundTag"`
		} `json:"xrayReport"`
	} `json:"report"`
}

func (a *App) torrentCfg() model.TorrentConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.TorrentConfig{NotifyAdmin: true, NotifyUser: true, Init: true}
	}
	a.botCfg.NormalizeTorrent()
	return a.botCfg.Torrent
}

// toggleTorrentNotify переключает уведомление админу (admin=true) или
// пользователю (admin=false).
func (a *App) toggleTorrentNotify(admin bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	a.botCfg.NormalizeTorrent()
	if admin {
		a.botCfg.Torrent.NotifyAdmin = !a.botCfg.Torrent.NotifyAdmin
	} else {
		a.botCfg.Torrent.NotifyUser = !a.botCfg.Torrent.NotifyUser
	}
}

// rwSecret — секрет вебхука панели (пустой = подпись не проверяется).
func (a *App) rwSecret() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return ""
	}
	return strings.TrimSpace(a.botCfg.Webhook.RemnawaveSecret)
}

func (a *App) pushTorrentReport(ctx context.Context, data json.RawMessage) {
	// Без секрета подпись вебхука не проверяется, а этот обработчик пишет строки
	// в БД и шлёт сообщения на telegramId ИЗ ТЕЛА ЗАПРОСА — то есть кто угодно,
	// зная адрес бота, рассылал бы «ваш IP заблокирован» произвольным людям и
	// раздувал журнал. Остальные события панели ничего такого не делают.
	if a.rwSecret() == "" {
		a.log.Warn("торрент-блокер: отчёт отброшен — не задан секрет вебхука панели")
		return
	}

	var r rwTorrentReport
	if err := json.Unmarshal(data, &r); err != nil {
		a.log.Warn("торрент-блокер: не разобран payload", "err", err)
		return
	}
	// Отчёт ни о ком: ни телеграма, ни имени, ни id панели — показывать нечего.
	if r.User.TelegramID == 0 && torrentWhoName(&r) == "" {
		a.log.Warn("торрент-блокер: отчёт без идентификации пользователя, отброшен")
		return
	}
	act := r.Report.ActionReport
	a.log.Info("торрент-блокер: отчёт панели",
		"tg_id", r.User.TelegramID, "username", r.User.Username,
		"ip", act.IP, "block_s", act.BlockDuration, "node", r.Node.Name)

	tc := a.torrentCfg()

	// blocked=false — панель отчиталась, но адрес НЕ заблокирован (например,
	// пользователь уже в ignoreLists). Такой отчёт не идёт ни в журнал (по
	// нему считаются страйки), ни пользователю: пугать его сообщением «ваш IP
	// заблокирован на —» не за что.
	if !act.Blocked {
		a.log.Info("торрент-блокер: отчёт без блокировки, нарушением не считаем", "tg_id", r.User.TelegramID)
		if tc.NotifyAdmin {
			a.torrentNotifyAdmin(ctx, &r, "", a.torrentRepeatCount(ctx, &model.TorrentReport{
				TelegramID: r.User.TelegramID, Username: torrentWhoName(&r),
			}), false)
		}
		return
	}

	rep := a.storeTorrentReport(ctx, &r)
	count := a.torrentRepeatCount(ctx, rep)

	if tc.NotifyAdmin {
		a.torrentNotifyAdmin(ctx, &r, rep.WillUnblockAt, count, true)
	}
	if tc.NotifyUser {
		a.torrentNotifyUser(ctx, &r, rep.WillUnblockAt)
	}
	a.torrentApplyStrikes(ctx, &r, tc.StrikeLimit)
}

// torrentStrikeBudget — потолок времени на последствия одного срабатывания
// (обращения к панели и два-три сообщения в Telegram).
const torrentStrikeBudget = 60 * time.Second

// torrentStrikeGrace — минимальная пауза между автоблокировками одного
// пользователя. Отключение подписки доезжает до нод не мгновенно, а панель
// продолжает слать отчёты по уже открытым соединениям: без паузы порог
// добирался бы повторно за минуты.
const torrentStrikeGrace = time.Hour

// lockStrike/unlockStrike сериализуют политику по пользователю в пределах
// процесса: одновременные отчёты не должны читать счётчик до записи отметки.
func (a *App) lockStrike(uid int64) bool {
	a.thrMu.Lock()
	defer a.thrMu.Unlock()
	if a.torStrikeBusy == nil {
		a.torStrikeBusy = map[int64]bool{}
	}
	if a.torStrikeBusy[uid] {
		return false
	}
	a.torStrikeBusy[uid] = true
	return true
}

func (a *App) unlockStrike(uid int64) {
	a.thrMu.Lock()
	delete(a.torStrikeBusy, uid)
	a.thrMu.Unlock()
}

// strikeFailDue — не чаще одной жалобы админу на пользователя за окно.
func (a *App) strikeFailDue(uid int64) bool {
	a.thrMu.Lock()
	defer a.thrMu.Unlock()
	if last, ok := a.torStrikeFail[uid]; ok && time.Since(last) < torrentStrikeGrace {
		return false
	}
	if a.torStrikeFail == nil {
		a.torStrikeFail = map[int64]time.Time{}
	}
	a.torStrikeFail[uid] = time.Now()
	return true
}

// torrentApplyStrikes отключает подписку, когда нарушений набралось не меньше
// порога. Порог 0 — политика выключена.
//
// Окно отсчёта — НЕ просто «30 дней»: оно начинается с момента прошлой
// автоблокировки этого пользователя. Иначе после того, как админ вернул
// доступ, следующий же отчёт снова уводил бы счётчик за порог — и так до
// конца окна повторов. Момент лежит в БД, поэтому переживает рестарт бота.
//
// Действие деструктивное (отключает подписку платящего человека), поэтому оно
// требует настроенного секрета вебхука: без подписи любой, кто знает адрес
// бота, мог бы отключить подписку кому угодно подделанным отчётом.
func (a *App) torrentApplyStrikes(ctx context.Context, r *rwTorrentReport, limit int) {
	uid := r.User.TelegramID
	if limit <= 0 || uid == 0 {
		return
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return
	}
	if a.rwSecret() == "" {
		a.log.Warn("торрент-блокер: автоблокировка пропущена — не задан секрет вебхука", "tg_id", uid)
		return
	}
	panel := a.panelClient()
	if panel == nil {
		return
	}
	// Панель шлёт отдельный отчёт на каждую заблокированную пару (IP, нода), и
	// у человека с несколькими устройствами они прилетают одновременно, каждый
	// в своей горутине. Без этой блокировки два отчёта успевали прочитать
	// счётчик до записи отметки и отключали подписку дважды.
	if !a.lockStrike(uid) {
		return
	}
	defer a.unlockStrike(uid)

	// Дальше идут последствия, а не обслуживание HTTP-запроса: у вебхука
	// дедлайн 15 с, и он истекал ровно между обращением к панели и записью
	// отметки — политика молча разъезжалась. Отвязываемся от него, но свой
	// потолок ставим: без него зависшая панель плюс ретраи Telegram держали бы
	// горутину запроса минутами, и такие обработки копились бы штабелями.
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), torrentStrikeBudget)
	defer cancel()

	since := time.Now().UTC().Add(-torrentRepeatWindow).Format(time.RFC3339)
	last, err := st.TorrentStrikeAt(bg, uid)
	if err != nil {
		a.log.Warn("торрент-блокер: чтение отметки автоблокировки", "err", err)
		return
	}
	var lastAt time.Time
	if last != "" {
		t, perr := time.Parse(time.RFC3339, last)
		if perr != nil {
			// Отметка есть, но нечитаема. Чинимся сами: пишем текущий момент,
			// чтобы отсчёт пошёл с него, а не откатывался к 30 дням (это
			// вернуло бы поведение «отключать снова и снова»).
			a.log.Warn("торрент-блокер: нечитаемая отметка автоблокировки", "tg_id", uid, "value", last)
			_ = st.SetTorrentStrike(bg, uid, time.Now().UTC().Format(time.RFC3339))
			return
		}
		if t.After(time.Now()) {
			// Часы сервера съехали назад или значение правили руками: без этой
			// проверки политика молча спала бы, пока время не догонит отметку.
			a.log.Warn("торрент-блокер: отметка автоблокировки из будущего", "tg_id", uid, "value", last)
			t = time.Now()
			_ = st.SetTorrentStrike(bg, uid, t.UTC().Format(time.RFC3339))
		}
		lastAt = t
	}
	// Страховка в памяти на случай, если запись отметки в БД не удалась:
	// в пределах процесса повтор всё равно не пройдёт.
	a.thrMu.Lock()
	if mem, ok := a.torStrikeSeen[uid]; ok && mem.After(lastAt) {
		lastAt = mem
	}
	a.thrMu.Unlock()

	if !lastAt.IsZero() {
		// Отключение доезжает до нод не мгновенно, и панель продолжает слать
		// отчёты по уже открытым соединениям. Эти отчёты не только не считаются
		// сразу — они целиком выпадают из следующего окна, иначе накопленный за
		// паузу хвост отключал бы подписку с первого же нарушения после того,
		// как админ вернул доступ.
		// Короткое замыкание: пока пауза идёт, окно ниже всё равно уехало бы в
		// будущее и счётчик вернул ноль — просто не ходим лишний раз в БД.
		if time.Since(lastAt) < torrentStrikeGrace {
			return
		}
		if next := lastAt.Add(torrentStrikeGrace).UTC().Format(time.RFC3339); next > since {
			since = next
		}
	}
	count, err := st.CountTorrentReports(bg, uid, torrentWhoName(r), since)
	if err != nil {
		a.log.Warn("торрент-блокер: счётчик для автоблокировки", "err", err)
		return
	}
	if count < limit {
		return
	}

	alang := a.lang(a.cfg.AdminID)
	// found=false означает «на панели такого пользователя нет» — подписка НЕ
	// отключена, и рапортовать об отключении нельзя.
	found, err := panel.DisableByTelegramID(bg, uid)
	if err != nil || !found {
		a.log.Warn("торрент-блокер: автоблокировка не удалась", "tg_id", uid, "found", found, "err", err)
		// Неудача повторится на каждом следующем отчёте (нет прав, нет
		// пользователя), а их у активного нарушителя десятки в час — админу
		// хватит одного сообщения за окно.
		if a.strikeFailDue(uid) {
			reason := i18n.T(alang, "rw.torrent_strike_notfound")
			if err != nil {
				reason = escapeErr(err)
			}
			a.notify(bg, a.cfg.AdminID, i18n.T(alang, "rw.torrent_strike_fail",
				a.userLabelByID(bg, uid), reason))
		}
		return
	}
	// Отметка ставится только после успеха: сорванная попытка не должна
	// сдвигать окно и глушить сообщения о снятии блокировки. Память
	// проставляется первой — она не может отказать.
	a.thrMu.Lock()
	if a.torStrikeSeen == nil {
		a.torStrikeSeen = map[int64]time.Time{}
	}
	a.torStrikeSeen[uid] = time.Now()
	a.thrMu.Unlock()
	if err := st.SetTorrentStrike(bg, uid, time.Now().UTC().Format(time.RFC3339)); err != nil {
		a.log.Error("торрент-блокер: отметка автоблокировки не сохранена — после рестарта отсчёт пойдёт с 30 дней", "tg_id", uid, "err", err)
	}
	a.setAddSubEnabledPanel(bg, uid, false)
	a.invalidateSubCache(uid)
	a.log.Info("торрент-блокер: подписка отключена автоматически", "tg_id", uid, "count", count)

	a.notify(bg, a.cfg.AdminID, i18n.T(alang, "rw.torrent_strike_admin",
		a.userLabelByID(bg, uid), count, limit))
	// Отключение подписки пользователь обязан узнать независимо от тумблера
	// «предупреждать о торрентах»: иначе доступ пропадёт молча.
	ulang := a.lang(uid)
	var rows [][]models.InlineKeyboardButton
	if sup := a.supportURL(); sup != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(ulang, "btn.support"), URL: sup}})
	}
	a.notifyKB(bg, uid, i18n.T(ulang, "rw.torrent_strike_user", count), rows)
}

// torrentPanelUserID — числовой id пользователя ПАНЕЛИ из отчёта (именно им
// оперирует torrentBlocker.ignoreLists.userId). Берём ТОЛЬКО actionReport.userId:
// xrayReport.email — это метка инбаунда, у заведённых вручную аккаунтов там
// может стоять числовой username, и по нему бот вывел бы из-под блокера
// постороннего пользователя. Всё, что не положительное число, отбрасываем.
func torrentPanelUserID(r *rwTorrentReport) string {
	v := strings.TrimSpace(r.Report.ActionReport.UserID)
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
		return v
	}
	return ""
}

// fieldMaxLen — предел на строку из payload перед записью в БД. Тело вебхука
// ограничено 256 КБ, а полей тут восемь: без обрезки один запрос мог положить в
// журнал четверть мегабайта, и так 180 дней.
const fieldMaxLen = 256

// torrentMaxBlockSeconds — год. Больше не бывает, а без потолка умножение на
// time.Second переполняло Duration и давало срок разблокировки в прошлом.
const torrentMaxBlockSeconds = 366 * 24 * 3600

func clipField(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > fieldMaxLen {
		return string(r[:fieldMaxLen])
	}
	return s
}

// torrentReportID — идемпотентный ключ отчёта. Панель переотправляет вебхук,
// если не дождалась 200 (а обработка ходит и в панель, и в Telegram, так что
// это штатный случай), и без ключа каждый ретрай добавлял бы «нарушение» —
// вплоть до отключения подписки по дублям. Момент блокировки панель шлёт с
// точностью до миллисекунд, поэтому пара (кто, адрес, нода, момент) один и тот
// же инцидент опознаёт, а два разных не склеивает.
func torrentReportID(r *rwTorrentReport) int64 {
	act := r.Report.ActionReport
	if strings.TrimSpace(act.ProcessedAt) == "" {
		return 0 // нечем ключевать — пусть хранилище проставит время
	}
	h := sha256.Sum256([]byte(strconv.FormatInt(r.User.TelegramID, 10) + "|" +
		torrentWhoName(r) + "|" + act.IP + "|" + torrentNodeLabel(r) + "|" + act.ProcessedAt))
	id := int64(binary.BigEndian.Uint64(h[:8]) >> 1) // без знака: id уходит в BIGINT
	if id == 0 {
		id = 1
	}
	return id
}

// torrentWhoName — под каким именем нарушитель попадает в журнал. У аккаунтов
// без Telegram считать не по чему, кроме username панели; если и его нет —
// подставляем id пользователя панели. Счётчик повторов обязан использовать
// ровно эту же строку, иначе покажет «1-е нарушение» тому, у кого их десяток.
func torrentWhoName(r *rwTorrentReport) string {
	if u := strings.TrimSpace(r.User.Username); u != "" {
		return u
	}
	return strings.TrimSpace(r.Report.ActionReport.UserID)
}

// torrentNodeLabel — подпись ноды для журнала и отчёта: «имя (страна)».
func torrentNodeLabel(r *rwTorrentReport) string {
	node := strings.TrimSpace(r.Node.Name)
	if cc := strings.TrimSpace(r.Node.CountryCode); cc != "" {
		if node == "" {
			node = cc
		} else {
			node += " (" + cc + ")"
		}
	}
	return node
}

// storeTorrentReport пишет отчёт в журнал и возвращает запись (для счётчика
// повторов). При недоступной БД возвращает запись без ID.
func (a *App) storeTorrentReport(ctx context.Context, r *rwTorrentReport) *model.TorrentReport {
	act := r.Report.ActionReport

	// Момент разблокировки нормализуется к RFC3339 UTC, чтобы сравнение
	// строк в DueTorrentUnblocks совпадало с порядком времён. Если панель
	// его не прислала — выводится из processedAt + blockDuration.
	will := ""
	if t, err := time.Parse(time.RFC3339, act.WillUnblockAt); err == nil {
		will = t.UTC().Format(time.RFC3339)
	} else if act.BlockDuration > 0 && act.BlockDuration <= torrentMaxBlockSeconds {
		base := time.Now()
		if t, err := time.Parse(time.RFC3339, act.ProcessedAt); err == nil {
			base = t
		}
		will = base.UTC().Add(time.Duration(act.BlockDuration) * time.Second).Format(time.RFC3339)
	}

	rep := &model.TorrentReport{
		ID:            torrentReportID(r),
		TelegramID:    r.User.TelegramID,
		Username:      clipField(torrentWhoName(r)),
		Node:          clipField(torrentNodeLabel(r)),
		IP:            clipField(act.IP),
		Protocol:      clipField(r.Report.XrayReport.Protocol),
		Inbound:       clipField(r.Report.XrayReport.InboundTag),
		Source:        clipField(r.Report.XrayReport.Source),
		Destination:   clipField(r.Report.XrayReport.Destination),
		BlockSeconds:  act.BlockDuration,
		WillUnblockAt: will,
		// Уведомлять о разблокировке некого — сразу помечено отправленным.
		UnblockNotified: r.User.TelegramID == 0 || will == "",
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return rep
	}
	if err := st.AddTorrentReport(ctx, rep); err != nil {
		a.log.Warn("торрент-блокер: запись в журнал", "err", err)
	}
	return rep
}

// torrentRepeatCount — номер нарушения за окно (включая текущее).
func (a *App) torrentRepeatCount(ctx context.Context, rep *model.TorrentReport) int {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return 1
	}
	since := time.Now().UTC().Add(-torrentRepeatWindow).Format(time.RFC3339)
	n, err := st.CountTorrentReports(ctx, rep.TelegramID, rep.Username, since)
	if err != nil {
		a.log.Warn("торрент-блокер: счётчик повторов", "err", err)
		return 1
	}
	if n < 1 {
		return 1
	}
	return n
}

func (a *App) torrentNotifyAdmin(ctx context.Context, r *rwTorrentReport, will string, count int, blocked bool) {
	lang := a.lang(a.cfg.AdminID)
	act := r.Report.ActionReport
	xr := r.Report.XrayReport

	who := ""
	if r.User.TelegramID != 0 {
		who = a.userLabelByID(ctx, r.User.TelegramID)
	} else if r.User.Username != "" {
		who = escapeName(r.User.Username)
	} else if act.UserID != "" {
		who = escapeName(act.UserID)
	} else if xr.Email != "" {
		who = escapeName(xr.Email)
	} else {
		who = "—"
	}

	// При blocked=false панель ничего не заблокировала: показывать срок и
	// давать кнопку снятия нельзя — снимать нечего.
	dur, till := fmtBlockDur(lang, act.BlockDuration), torrentTill(will, lang)
	if !blocked {
		dur, till = i18n.T(lang, "rw.torrent_not_blocked"), "—"
	}
	text := i18n.T(lang, "rw.torrent_admin",
		who, i18n.T(lang, "rw.torrent_admin_repeat", count),
		escapeName(orDash(torrentNodeLabel(r))),
		escapeName(orDash(act.IP)), dur, till,
		escapeName(orDash(xr.Protocol)), escapeName(orDash(xr.InboundTag)),
		escapeName(orDash(xr.Source)), escapeName(orDash(xr.Destination)))

	var rows [][]models.InlineKeyboardButton
	if r.User.TelegramID != 0 {
		id := strconv.FormatInt(r.User.TelegramID, 10)
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "rw.torrent_btn_user"), "usr:view:"+id),
			btn(i18n.T(lang, "btn.block"), "usr:block:"+id),
		})
	}
	// Снять блокировку раньше срока и увести пользователя из-под блокера —
	// обе кнопки ходят в панель, поэтому показываются только когда есть чем
	// её адресовать: IP для Executor и числовой id панели для ignoreLists.
	var act2 []models.InlineKeyboardButton
	// Адрес приходит из тела вебхука: мусор длиной под сотню символов выбил бы
	// callback_data за лимит Telegram в 64 байта, и админ не получил бы отчёт
	// вообще (сообщение с битой кнопкой не отправляется).
	if ip := strings.TrimSpace(act.IP); blocked && net.ParseIP(ip) != nil {
		act2 = append(act2, btn(i18n.T(lang, "rw.torrent_btn_unblock"), "torj:unb:"+ip))
	}
	if pid := torrentPanelUserID(r); pid != "" {
		act2 = append(act2, btn(i18n.T(lang, "rw.torrent_btn_ignore"), "torj:ign:"+pid))
	}
	if len(act2) > 0 {
		rows = append(rows, act2)
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "rw.torrent_btn_log"), "torj:log")})
	a.notifyKB(ctx, a.cfg.AdminID, text, rows)
}

func (a *App) torrentNotifyUser(ctx context.Context, r *rwTorrentReport, will string) {
	uid := r.User.TelegramID
	if uid == 0 {
		return
	}
	a.thrMu.Lock()
	if last, ok := a.torSeen[uid]; ok && time.Since(last) < torrentUserCooldown {
		a.thrMu.Unlock()
		return
	}
	if a.torSeen == nil {
		a.torSeen = map[int64]time.Time{}
	}
	a.torSeen[uid] = time.Now()
	a.thrMu.Unlock()

	act := r.Report.ActionReport
	lang := a.lang(uid)
	var rows [][]models.InlineKeyboardButton
	if sup := a.supportURL(); sup != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(lang, "btn.support"), URL: sup}})
	}
	text := i18n.T(lang, "rw.torrent_user",
		fmtBlockDur(lang, act.BlockDuration), torrentTill(will, lang))
	// Обещать сообщение о снятии можно только когда срок известен: без него
	// запись сразу помечается отработанной и уведомление не придёт никогда.
	if will != "" {
		text += i18n.T(lang, "rw.torrent_user_wait")
	}
	// Адрес пира, на который шёл торрент-трафик: пользователю это доказательство,
	// что сработало не «просто так». Панель поле не гарантирует — строку
	// добавляем только когда есть что показать.
	if dst := strings.TrimSpace(r.Report.XrayReport.Destination); dst != "" {
		text += i18n.T(lang, "rw.torrent_user_dest", escapeName(dst))
	}
	a.notifyKB(ctx, uid, text, rows)
	a.log.Info("торрент-блокер: предупреждение отправлено", "tg_id", uid)
}

// RunTorrentUnblocker — фоновый цикл: шлёт «блокировка снята», когда истекает
// срок из журнала. Переживает рестарт: очередь лежит в БД (unblock_notified).
func (a *App) RunTorrentUnblocker(ctx context.Context) {
	t := time.NewTicker(torrentUnblockTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.torrentUnblockOnce(ctx)
		}
	}
}

func (a *App) torrentUnblockOnce(ctx context.Context) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return
	}
	now := time.Now()
	due, err := st.DueTorrentUnblocks(ctx, now.UTC().Format(time.RFC3339))
	if err != nil {
		a.log.Warn("торрент-блокер: выборка разблокировок", "err", err)
		return
	}
	a.pruneStrikeMaps()
	if len(due) == 0 {
		return
	}
	a.deliverUnblocks(ctx, st, due, true)
}

// deliverUnblocks — единственное место, где решается, слать ли «блокировка
// снята». И тикер, и ручное снятие админом ходят сюда: раньше это были две
// почти одинаковые копии, и ручная ветка не проверяла ни паузу, ни статус
// подписки — админ, нажавший кнопку на трёх отчётах подряд, слал человеку три
// одинаковых сообщения.
//
// checkStale отличает тикер (записи могли пролежать, пока бот был выключен) от
// ручного снятия (оно происходит здесь и сейчас).
func (a *App) deliverUnblocks(ctx context.Context, st storage.Storage, due []model.TorrentReport, checkStale bool) {
	now := time.Now()
	notifyUser := a.torrentCfg().NotifyUser
	// Ответ панели про статус подписки кэшируется на проход: у человека с
	// несколькими устройствами записей несколько, а ответ один и тот же.
	subOff := map[int64]bool{}
	subKnown := map[int64]bool{}
	for _, r := range due {
		send := notifyUser && r.TelegramID != 0
		// Срок вышел давно (бот стоял) — сообщение уже неактуально.
		if send && checkStale {
			if t, err := time.Parse(time.RFC3339, r.WillUnblockAt); err != nil || now.Sub(t) > torrentUnblockStale {
				send = false
			}
		}
		// reserved — пауза занята именно этой итерацией: только тогда её можно
		// откатить, если пометка в БД не удалась.
		reserved := false
		if send {
			// Пауза резервируется ДО похода в панель: проверка и установка по
			// разные стороны сетевого вызова давали дубль, когда админ жал
			// кнопку одновременно с тикером.
			a.thrMu.Lock()
			if last, ok := a.torUnbSeen[r.TelegramID]; ok && time.Since(last) < torrentUserCooldown {
				send = false
			} else {
				if a.torUnbSeen == nil {
					a.torUnbSeen = map[int64]time.Time{}
				}
				a.torUnbSeen[r.TelegramID] = time.Now()
				reserved = true
			}
			a.thrMu.Unlock()
		}
		if send {
			known, ok := subKnown[r.TelegramID]
			if !ok {
				var off bool
				off, known = a.subDisabled(ctx, r.TelegramID)
				subOff[r.TelegramID], subKnown[r.TelegramID] = off, known
			}
			// Статус неизвестен (панель молчит или пользователя там нет) —
			// молчим: «доступ снова работает» может оказаться враньём.
			if !known || subOff[r.TelegramID] {
				if !known {
					a.log.Info("торрент-блокер: статус подписки неизвестен, о снятии не пишем", "tg_id", r.TelegramID)
				}
				send = false
			}
		}
		// Пометка ставится в любом случае: иначе запись крутилась бы в очереди
		// вечно. Решение уже принято выше.
		if err := st.MarkTorrentUnblockNotified(ctx, r.ID); err != nil {
			a.log.Warn("торрент-блокер: пометка разблокировки", "err", err)
			// Пауза возвращается: запись осталась в очереди, и на следующем
			// тике решение примут заново — иначе сбой БД съедал бы обещанное
			// «блокировка снята» насовсем (пауза занята, а пометка уже прошла).
			if reserved {
				a.thrMu.Lock()
				delete(a.torUnbSeen, r.TelegramID)
				a.thrMu.Unlock()
			}
			continue
		}
		if send {
			a.sendTorrentUnblock(ctx, r.TelegramID)
		}
	}
}

// sendTorrentUnblock шлёт «блокировка снята»: заданный админом текст с его
// форматированием, иначе — стандартный из i18n.
func (a *App) sendTorrentUnblock(ctx context.Context, chatID int64) {
	tc := a.torrentCfg()
	lang := a.lang(chatID)
	if strings.TrimSpace(tc.UnblockText) != "" {
		var ents []models.MessageEntity
		_ = json.Unmarshal(tc.UnblockEntities, &ents)
		a.msg.SendEnt(ctx, chatID, tc.UnblockText, ents, [][]models.InlineKeyboardButton{backHomeRow(lang)})
	} else {
		a.notifyKB(ctx, chatID, i18n.T(lang, "rw.torrent_unblocked"), nil)
	}
	a.log.Info("торрент-блокер: уведомление о разблокировке", "tg_id", chatID)
}

// torrentTill форматирует момент разблокировки; при пустом/кривом значении — «—».
func torrentTill(raw, lang string) string {
	if raw == "" {
		return "—"
	}
	if _, err := time.Parse(time.RFC3339, raw); err != nil {
		return "—"
	}
	return formatExpire(raw, lang)
}

// fmtBlockDur — компактная длительность блокировки: «1 ч 30 мин», «60 сек».
func fmtBlockDur(lang string, secs int) string {
	if secs <= 0 {
		return "—"
	}
	h, m, s := secs/3600, (secs%3600)/60, secs%60
	var parts []string
	if h > 0 {
		parts = append(parts, strconv.Itoa(h)+" "+i18n.T(lang, "dur.h"))
	}
	if m > 0 {
		parts = append(parts, strconv.Itoa(m)+" "+i18n.T(lang, "dur.m"))
	}
	if s > 0 && h == 0 {
		parts = append(parts, strconv.Itoa(s)+" "+i18n.T(lang, "dur.s"))
	}
	return strings.Join(parts, " ")
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// subDisabled — выключена ли подписка человека прямо сейчас. Спрашиваем панель,
// а не отметку страйка: отметка отвечает на другой вопрос («когда началось
// текущее окно отсчёта») и врала в обе стороны. Панель же знает и про ручное
// отключение админом.
//
// Второе значение — «ответ получен». Панель не различает «нет связи» и «такого
// пользователя нет», поэтому оба случая честнее считать неизвестностью, чем
// молча трактовать как «подписка активна».
func (a *App) subDisabled(ctx context.Context, tgID int64) (disabled, known bool) {
	if tgID == 0 {
		return false, false
	}
	panel := a.panelClient()
	if panel == nil {
		// Панель не настроена — проверять нечем, но и врать нечему: подписками
		// в этом режиме никто не управляет.
		return false, true
	}
	_, _, st, ok := panel.SubscriptionFull(ctx, tgID)
	if !ok {
		return false, false
	}
	return st == remnawave.StatusDisabled, true
}

// pruneStrikeMaps чистит служебные карты: они нужны только на время паузы
// после срабатывания, а росли бы по числу нарушителей без конца.
func (a *App) pruneStrikeMaps() {
	cutoff := 2 * torrentStrikeGrace
	a.thrMu.Lock()
	defer a.thrMu.Unlock()
	for uid, t := range a.torStrikeSeen {
		if time.Since(t) > cutoff {
			delete(a.torStrikeSeen, uid)
		}
	}
	for uid, t := range a.torStrikeFail {
		if time.Since(t) > cutoff {
			delete(a.torStrikeFail, uid)
		}
	}
	// Карты пауз сообщений устаревают ещё быстрее (10 минут) и точно так же
	// росли бы по числу нарушителей без конца.
	seenCutoff := 2 * torrentUserCooldown
	for uid, t := range a.torSeen {
		if time.Since(t) > seenCutoff {
			delete(a.torSeen, uid)
		}
	}
	for uid, t := range a.torUnbSeen {
		if time.Since(t) > seenCutoff {
			delete(a.torUnbSeen, uid)
		}
	}
}

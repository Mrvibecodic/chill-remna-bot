package app

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func (a *App) showBroadcast(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	count := 0
	if a.store != nil {
		if ids, err := a.broadcastIDs(ctx); err == nil {
			count = len(ids)
		}
	}
	a.sendKBSection(ctx, chatID, assets.SectionPromoCode, i18n.T(lang, "bcast.title", count), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "bcast.btn_new"), "bc:new")},
		navBack(lang, "menu:marketing"),
	})
}

func (a *App) onBroadcast(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	switch val {
	case "new":
		ui := a.getUI(chatID)
		ui.adminInput = "bcast"
		ui.bcastSegs = nil
		a.askInput(ctx, chatID, i18n.T(lang, "bcast.ask"), "menu:broadcast")
	case "send":
		ui := a.getUI(chatID)
		text := ui.broadcastText
		segs := ui.bcastSegs.clone()
		ui.broadcastText = ""
		ui.bcastSegs = nil
		if text == "" {
			a.showBroadcast(ctx, chatID)
			return
		}
		if !a.bcastRunning.CompareAndSwap(false, true) {
			a.sendHome(ctx, chatID, i18n.T(lang, "bcast.already_running"))
			return
		}
		a.bcastStop.Store(false)
		a.sendKB(ctx, chatID, i18n.T(lang, "bcast.started"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "bcast.btn_stop"), "bc:stop")},
			navBack(lang, "menu:marketing"),
		})
		plans, _ := a.planList(ctx)
		label := bcastSegsLabel(lang, segs, plans)
		a.runBroadcast(chatID, text, segs, label, a.screenMsgID(chatID))
	case "stop":
		// Флаг на всём боте, а не на экране: рассылка одна, а админов может
		// быть несколько — остановить обязан любой. Раньше остановить её было
		// нельзя вообще, только погасив контейнер.
		if !a.bcastRunning.Load() {
			a.sendHome(ctx, chatID, i18n.T(lang, "bcast.not_running"))
			return
		}
		a.bcastStop.Store(true)
		a.sendHome(ctx, chatID, i18n.T(lang, "bcast.stopping"))
	default:
		seg, ok := strings.CutPrefix(val, "sg:")
		if !ok {
			return
		}
		ui := a.getUI(chatID)
		if ui.broadcastText == "" {
			a.showBroadcast(ctx, chatID)
			return
		}
		ui.bcastSegs = ui.bcastSegs.toggle(seg)
		a.previewBroadcast(ctx, chatID, ui.broadcastText)
	}
}

func (a *App) previewBroadcast(ctx context.Context, chatID int64, text string) {
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	ui.broadcastText = text
	plans, plansErr := a.planList(ctx)
	if plansErr == nil {
		ui.bcastSegs = ui.bcastSegs.keepPlans(plans)
	}
	segs := ui.bcastSegs
	count := 0
	if a.store != nil {
		if ids, err := a.bcastRecipients(ctx, segs, nil); err == nil {
			count = len(ids)
		}
	}
	mark := func(on bool) string {
		if on {
			return "✅ "
		}
		return "⬜ "
	}
	rows := [][]models.InlineKeyboardButton{
		{btn(mark(len(segs) == 0)+i18n.T(lang, "bcast.seg_all"), "bc:sg:"+bcastSegAll)},
		{
			btn(mark(segs[bcastSegTrial])+i18n.T(lang, "bcast.seg_trial"), "bc:sg:"+bcastSegTrial),
			btn(mark(segs[bcastSegNone])+i18n.T(lang, "bcast.seg_none"), "bc:sg:"+bcastSegNone),
		},
	}
	for i := range plans {
		on := segs[bcastSegPlan+plans[i].Code]
		rows = append(rows, []models.InlineKeyboardButton{
			btn(mark(on)+planTitle(lang, &plans[i]), "bc:sg:"+bcastSegPlan+plans[i].Code),
		})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "bcast.btn_send", count), "bc:send")},
		navBack(lang, "menu:broadcast"),
	)
	head := i18n.T(lang, "bcast.preview", count) + "\n" + i18n.T(lang, "bcast.to", html.EscapeString(bcastSegsLabel(lang, segs, plans)))
	a.sendMktKB(ctx, chatID, head+"\n\n"+text, rows)
}

// expandBroadcastVars substitutes per-recipient placeholders in a broadcast
// template: {name}, {username}, {id}, {balance}, {expire}. Every substituted
// value is HTML-escaped so a user's name or username can't break the Telegram
// HTML markup of the message. Empty fields fall back to a neutral word.
func (a *App) expandBroadcastVars(text string, u *model.User, lang string) string {
	if u == nil || !strings.Contains(text, "{") {
		return text
	}
	fallback := i18n.T(lang, "bcast.var_fallback")

	name := strings.TrimSpace(u.FirstName)
	if name == "" {
		name = strings.TrimSpace(u.Username)
	}
	if name == "" {
		name = fallback
	}

	username := strings.TrimSpace(u.Username)
	if username != "" {
		username = "@" + username
	} else {
		username = fallback
	}

	return strings.NewReplacer(
		"{name}", escapeName(name),
		"{username}", escapeName(username),
		"{id}", strconv.FormatInt(u.TelegramID, 10),
		"{balance}", html.EscapeString(kopecksToRub(u.Balance)),
		"{expire}", html.EscapeString(formatExpire(u.SubExpireAt, lang)),
	).Replace(text)
}

func (a *App) runBroadcast(adminChat int64, text string, segs bcastSegs, label string, statusID int) {
	if a.store == nil {
		a.bcastRunning.Store(false)
		return
	}
	job := &bcastJob{Admin: adminChat, Text: text, Label: label, StatusID: statusID,
		Started: time.Now().UTC().Format(time.RFC3339)}
	ctx := a.bgContext()
	go func() {
		defer a.bcastRunning.Store(false)
		lang := a.lang(adminChat)
		if !a.enterWork() {
			a.bcastFinish(ctx, job, i18n.T(lang, "bcast.paused"))
			return
		}
		defer a.leaveWork()
		var alive map[int64]time.Time
		if len(segs) > 0 {
			var err error
			if alive, err = a.panelExpiries(ctx); err != nil {
				a.log.Warn("рассылка: сверка с панелью", "err", err)
				a.bcastFinish(ctx, job, i18n.T(lang, "bcast.panel_failed"))
				return
			}
		}
		ids, err := a.bcastRecipients(ctx, segs, alive)
		if err != nil {
			a.bcastFinish(ctx, job, i18n.T(lang, "bcast.failed"))
			return
		}
		job.IDs = ids
		a.saveBcastJob(job)
		pos := &bcastPos{}
		a.saveBcastPos(pos)
		a.bcastLoop(ctx, job, pos)
	}()
}

// bcastLoop отправляет рассылку с позиции pos. Прогресс пишется на диск после
// каждого сообщения: перезапуск бота посреди рассылки не теряет остаток
// (resumeBroadcast продолжит), а повтор достаётся самое большее одному
// человеку — тому, чьё сообщение было в пути.
func (a *App) bcastLoop(ctx context.Context, job *bcastJob, pos *bcastPos) {
	lang := a.lang(job.Admin)
	// Pace sends to stay well under Telegram's global rate limit and stop
	// promptly on shutdown. Per-message 429s are retried inside sendWithRetry.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	stopped := false
	for pos.Next < len(job.IDs) {
		select {
		case <-ctx.Done():
			// Файлы остаются: после запуска рассылка продолжится.
			a.log.Info("рассылка прервана остановкой бота", "done", pos.Next, "total", len(job.IDs))
			return
		case <-ticker.C:
		}
		// Восстановление базы останавливает рассылку так же, как кнопка «Стоп».
		if a.bcastStop.Load() || a.paused() {
			stopped = true
			break
		}
		id := job.IDs[pos.Next]
		out := job.Text
		if u, err := a.store.GetUser(ctx, id); err == nil && u != nil {
			out = a.expandBroadcastVars(job.Text, u, a.lang(id))
		}
		mid, serr := a.msg.SendErr(ctx, id, a.applyPremium(out))
		if mid == 0 && ctx.Err() != nil {
			// Отправку оборвала остановка бота: позицию не двигаем, после
			// запуска этот человек получит сообщение.
			a.log.Info("рассылка прервана остановкой бота", "done", pos.Next, "total", len(job.IDs))
			return
		}
		switch {
		case mid != 0:
			pos.Sent++
		case chatUnreachable(serr):
			// Человек заблокировал бота, удалил аккаунт или ни разу ему не
			// писал. Помечаем, чтобы следующая рассылка на него не тратилась.
			// Это НЕ бан: доступ к боту не закрывается, метка снимается его же
			// сообщением.
			pos.Gone++
			if err := a.store.SetUnreachable(ctx, id, time.Now().UTC().Format(time.RFC3339)); err != nil {
				a.log.Warn("метка недоступного чата не сохранена", "err", err)
			}
		default:
			pos.Failed++
		}
		pos.Next++
		a.saveBcastPos(pos)
	}
	to := html.EscapeString(job.Label)
	if stopped {
		a.bcastFinish(ctx, job, i18n.T(lang, "bcast.stopped", to, len(job.IDs), pos.Sent, pos.Failed, pos.Gone, len(job.IDs)-pos.Next))
		return
	}
	a.bcastFinish(ctx, job, i18n.T(lang, "bcast.done", to, len(job.IDs), pos.Sent, pos.Failed, pos.Gone))
}

// bcastFinish — итог на месте статуса «запущена» (иначе под ним осталась бы
// кнопка остановки уже не идущей рассылки) и уборка файлов задания.
func (a *App) bcastFinish(ctx context.Context, job *bcastJob, text string) {
	a.dropBcastJob()
	doneText := a.applyPremium(text)
	doneRows := [][]models.InlineKeyboardButton{navBack(a.lang(job.Admin), "menu:marketing")}
	if job.StatusID == 0 || !a.msg.EditText(ctx, job.Admin, job.StatusID, doneText, doneRows) {
		a.msg.SendKB(ctx, job.Admin, doneText, doneRows)
	}
}

// chatUnreachable — Telegram окончательно отказал: чата с человеком нет.
// Повторять бессмысленно, в отличие от сбоя сети или ответа 5xx.
func chatUnreachable(err error) bool {
	if errors.Is(err, bot.ErrorForbidden) {
		return true
	}
	if !errors.Is(err, bot.ErrorBadRequest) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "chat not found") || strings.Contains(msg, "peer_id_invalid")
}

// bcastResumeMaxAge — дольше бот не продолжает прерванную рассылку: через
// сутки её текст мог устареть, решать админу.
const bcastResumeMaxAge = 24 * time.Hour

// bcastJob — рассылка, уже начатая: получатели определены на старте.
// Хранится в DATA_DIR рядом с базой, чтобы пережить перезапуск бота.
type bcastJob struct {
	Admin    int64   `json:"admin"`
	Text     string  `json:"text"`
	Label    string  `json:"label"`
	IDs      []int64 `json:"ids"`
	StatusID int     `json:"status_id"`
	Started  string  `json:"started"`
}

// bcastPos — сколько уже пройдено. Отдельный маленький файл: переписывать
// после каждого сообщения весь список получателей было бы дорого.
type bcastPos struct {
	Next   int `json:"next"`
	Sent   int `json:"sent"`
	Failed int `json:"failed"`
	Gone   int `json:"gone"`
}

const (
	bcastJobFile = "broadcast_job.json"
	bcastPosFile = "broadcast_pos.json"
)

func (a *App) bcastPath(name string) string {
	if a.cfg == nil || a.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(a.cfg.DataDir, name)
}

func (a *App) writeBcastFile(name string, v any) {
	path := a.bcastPath(name)
	if path == "" {
		return
	}
	data, err := json.Marshal(v)
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		a.log.Warn("рассылка: прогресс не сохранён", "file", name, "err", err)
	}
}

func (a *App) saveBcastJob(job *bcastJob) { a.writeBcastFile(bcastJobFile, job) }
func (a *App) saveBcastPos(pos *bcastPos) { a.writeBcastFile(bcastPosFile, pos) }

func (a *App) dropBcastJob() {
	for _, name := range []string{bcastJobFile, bcastPosFile} {
		if path := a.bcastPath(name); path != "" {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				a.log.Warn("рассылка: файл задания не удалён", "file", name, "err", err)
			}
		}
	}
}

// resumeBroadcast продолжает рассылку, прерванную перезапуском бота. Звать из
// Run, когда мессенджер уже есть.
func (a *App) resumeBroadcast(ctx context.Context) {
	path := a.bcastPath(bcastJobFile)
	if path == "" || a.store == nil {
		return
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var job bcastJob
	if err == nil {
		err = json.Unmarshal(raw, &job)
	}
	if err != nil || job.Admin == 0 {
		a.log.Warn("рассылка: задание не прочитано", "err", err)
		a.dropBcastJob()
		return
	}
	var pos bcastPos
	if raw, err := os.ReadFile(a.bcastPath(bcastPosFile)); err == nil {
		if err := json.Unmarshal(raw, &pos); err != nil {
			a.log.Warn("рассылка: прогресс не прочитан", "err", err)
		}
	}
	pos.Next = max(0, min(pos.Next, len(job.IDs)))
	if !a.bcastRunning.CompareAndSwap(false, true) {
		return
	}
	lang := a.lang(job.Admin)
	to := html.EscapeString(job.Label)
	started, perr := time.Parse(time.RFC3339, job.Started)
	if perr != nil || time.Since(started) > bcastResumeMaxAge {
		a.bcastRunning.Store(false)
		job.StatusID = 0
		a.bcastFinish(ctx, &job, i18n.T(lang, "bcast.interrupted", to, len(job.IDs), pos.Sent, pos.Failed, pos.Gone, len(job.IDs)-pos.Next))
		return
	}
	a.bcastStop.Store(false)
	// Статус правится на месте прежнего «запущена»: два сообщения с кнопкой
	// остановки одной рассылки только путают.
	resumed := a.applyPremium(i18n.T(lang, "bcast.resumed", to, len(job.IDs)-pos.Next))
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "bcast.btn_stop"), "bc:stop")},
		navBack(lang, "menu:marketing"),
	}
	if job.StatusID == 0 || !a.msg.EditText(ctx, job.Admin, job.StatusID, resumed, rows) {
		job.StatusID = a.msg.SendKB(ctx, job.Admin, resumed, rows)
		a.saveBcastJob(&job)
	}
	go func() {
		defer a.bcastRunning.Store(false)
		if !a.enterWork() {
			a.bcastFinish(ctx, &job, i18n.T(lang, "bcast.stopped", to, len(job.IDs), pos.Sent, pos.Failed, pos.Gone, len(job.IDs)-pos.Next))
			return
		}
		defer a.leaveWork()
		a.bcastLoop(ctx, &job, &pos)
	}()
}

// broadcastIDs — получатели рассылки: пользователи с чатом в Telegram.
// Аккаунты кабинета по почте (отрицательный id) писать некуда.
func (a *App) broadcastIDs(ctx context.Context) ([]int64, error) {
	ids, err := a.store.AllUserIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := ids[:0]
	for _, id := range ids {
		if hasChat(id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// Группы получателей рассылки. Ключ уходит в callback-данные кнопки
// («bc:sg:» + ключ), код тарифа — не длиннее PlanCodeMaxLen, так что
// в 64 байта влезает.
const (
	bcastSegAll   = "all"
	bcastSegTrial = "trial"
	bcastSegNone  = "none"
	bcastSegPlan  = "p:"
)

// bcastSegs — отмеченные группы; пусто — «Всем».
type bcastSegs map[string]bool

func (s bcastSegs) clone() bcastSegs {
	if len(s) == 0 {
		return nil
	}
	out := make(bcastSegs, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

// toggle — нажатие галочки. «Всем» снимает остальные; снятая последняя
// группа — снова «Всем». Незнакомый ключ (старая кнопка) ничего не меняет.
func (s bcastSegs) toggle(seg string) bcastSegs {
	switch {
	case seg == bcastSegAll:
		return nil
	case seg == bcastSegTrial, seg == bcastSegNone:
	case strings.HasPrefix(seg, bcastSegPlan) && model.ValidPlanCode(strings.TrimPrefix(seg, bcastSegPlan)):
	default:
		return s
	}
	out := s.clone()
	if out[seg] {
		delete(out, seg)
	} else {
		if out == nil {
			out = bcastSegs{}
		}
		out[seg] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// bcastSegsLabel — «кому» словами: для предпросмотра и итога рассылки.
func bcastSegsLabel(lang string, segs bcastSegs, plans []model.Plan) string {
	var to []string
	if segs[bcastSegTrial] {
		to = append(to, i18n.T(lang, "bcast.seg_trial"))
	}
	if segs[bcastSegNone] {
		to = append(to, i18n.T(lang, "bcast.seg_none"))
	}
	for i := range plans {
		if segs[bcastSegPlan+plans[i].Code] {
			to = append(to, planTitle(lang, &plans[i]))
		}
	}
	if len(to) == 0 {
		return i18n.T(lang, "bcast.seg_all")
	}
	return strings.Join(to, ", ")
}

// keepPlans снимает отметки с тарифов, которых больше нет: кнопки у них уже
// не будет, и снять галочку админ не смог бы.
func (s bcastSegs) keepPlans(plans []model.Plan) bcastSegs {
	if len(s) == 0 {
		return s
	}
	known := make(map[string]bool, len(plans))
	for i := range plans {
		known[bcastSegPlan+plans[i].Code] = true
	}
	out := s.clone()
	for k := range out {
		if strings.HasPrefix(k, bcastSegPlan) && !known[k] {
			delete(out, k)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// match — попадает ли человек в отмеченные группы. alive — есть ли у него
// действующая подписка, trial — идёт ли пробный период. Тариф известен по
// последней покупке; подписка без него (выдана вручную, привязана без
// покупки) попадает только во «Всем».
func (s bcastSegs) match(u *model.User, alive, trial bool) bool {
	if len(s) == 0 {
		return true
	}
	if !alive {
		return s[bcastSegNone]
	}
	if trial {
		return s[bcastSegTrial]
	}
	code := planCodeOf(u.Snapshot)
	return code != "" && s[bcastSegPlan+code]
}

// bcastRecipients — получатели с учётом групп. panelExp — сроки из панели
// (panelExpiries); nil — судить по сроку в базе бота (предпросмотр). Кого
// панель не вернула вовсе, судим по базе бота: страницы могли сдвинуться,
// пока их листали, и платящий клиент получил бы «купите подписку».
func (a *App) bcastRecipients(ctx context.Context, segs bcastSegs, panelExp map[int64]time.Time) ([]int64, error) {
	if len(segs) == 0 {
		return a.broadcastIDs(ctx)
	}
	users, err := a.store.BroadcastAudience(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var out []int64
	for i := range users {
		u := &users[i]
		if !hasChat(u.TelegramID) {
			continue
		}
		on := panelSubAlive(u.SubExpireAt)
		trial := u.NotifyKind == "trial"
		if exp, seen := panelExp[u.TelegramID]; seen {
			on = exp.After(now)
			// Срок в панели дальше конца пробного периода — подписку выдали
			// в обход бота, и это уже не триал, хотя бот его так помнит.
			if local, err := time.Parse(time.RFC3339, u.SubExpireAt); trial && (err != nil || exp.After(local.Add(time.Hour))) {
				trial = false
			}
		}
		if segs.match(u, on, trial) {
			out = append(out, u.TelegramID)
		}
	}
	return out, nil
}

// panelExpiries — кого вернула панель (ключ) и до какого времени у него
// действует подписка (самый дальний срок включённых учёток, нулевое время —
// подписки нет; учёток у человека может быть несколько). Срок
// в базе бота отстаёт, если подписку правили в панели руками, а рассылка
// «купите подписку» тому, у кого она есть, хуже, чем не начатая рассылка.
// Поэтому любая ошибка панели — отказ целиком.
func (a *App) panelExpiries(ctx context.Context) (map[int64]time.Time, error) {
	panel := a.panelClient()
	if panel == nil {
		return nil, errors.New("панель не подключена")
	}
	alive := map[int64]time.Time{}
	for start := 0; ; start += panelScanPageSize {
		users, total, err := panel.ListUsersPage(ctx, start, panelScanPageSize)
		if err != nil {
			return nil, err
		}
		for i := range users {
			id := panelUserTgID(&users[i])
			if id <= 0 {
				continue
			}
			cur := alive[id]
			if exp, err := time.Parse(time.RFC3339, users[i].ExpireAt); err == nil && !panelSubOff(users[i].Status) && exp.After(cur) {
				cur = exp
			}
			alive[id] = cur
		}
		if len(users) == 0 || start+len(users) >= total {
			return alive, nil
		}
	}
}

// panelSubOff — учётка в панели выключена, хотя срок ещё не вышел. Чёрный
// список, а не белый: незнакомый статус новой версии панели не должен
// записать всех в «Без подписки». LIMITED (кончился трафик) — подписка есть.
func panelSubOff(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "DISABLED", "EXPIRED":
		return true
	}
	return false
}

// panelUserTgID — чей это аккаунт в панели: telegramId, а без него — имя по
// схеме бота (tg_<id>) или remnashop (rs_<id>).
func panelUserTgID(pu *remnawave.PanelUser) int64 {
	if pu.TelegramID != 0 {
		return pu.TelegramID
	}
	for _, prefix := range []string{"tg_", "rs_"} {
		rest, ok := strings.CutPrefix(pu.Username, prefix)
		if !ok || rest == "" {
			continue
		}
		if id, err := strconv.ParseInt(rest, 10, 64); err == nil && strconv.FormatInt(id, 10) == rest {
			return id
		}
	}
	return 0
}

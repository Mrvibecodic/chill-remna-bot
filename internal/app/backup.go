package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"html"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/backupfile"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/storage"
)

const (
	// backupTick — как часто планировщик смотрит, не пора ли слать бэкап.
	backupTick = time.Minute
	// backupRetry — пауза после неудачной отправки по расписанию. Без неё
	// недоступный чат дёргали бы каждую минуту.
	backupRetry = time.Hour
	// tgUploadLimit — больше Telegram от бота документ не примет. Запас — на
	// заголовок шифрования.
	tgUploadLimit = 50<<20 - 4<<10
	// backupMaxPlain — потолок распакованного бэкапа: защита памяти от
	// подсунутого «сжатого в тысячу раз» файла.
	backupMaxPlain = 2 << 30
	backupPassMin  = 8
	backupPassMax  = 128
	// quietWait — сколько восстановление ждёт начатые оплаты, вебхуки и
	// фоновые проходы. Не дождалось — базу не трогает.
	quietWait = 90 * time.Second
	// backupNetTimeout — проверка токена отдельного бота и пробное сообщение.
	backupNetTimeout = 20 * time.Second
)

// Фазы восстановления: ждём начатое; заменяем базу.
const (
	phaseWait    int32 = 1
	phaseReplace int32 = 2
)

var (
	errRestoring    = errors.New("идёт восстановление базы")
	errBackupTooBig = errors.New("бэкап больше 50 МБ")
)

// bkPending — присланный файл бэкапа до подтверждения восстановления.
// Пока пароль не подошёл, лежит сам файл; после проверки — только
// расшифрованное содержимое и сводка.
//
// Поля после создания меняются только под bkPendMu.
type bkPending struct {
	at       time.Time
	file     []byte
	gz       []byte
	password string
	info     *storage.BackupInfo
	// wantPass — файл ждёт пароль текстом; checking — пароль проверяется.
	wantPass bool
	checking bool
}

// bkPendTTL — сколько присланный файл ждёт пароль и подтверждение.
const bkPendTTL = 30 * time.Minute

// paused — идёт восстановление базы.
func (a *App) paused() bool { return a.restoring.Load() }

// enterWork отмечает начало фоновой работы, которая пишет в базу. false —
// идёт восстановление, работу начинать нельзя. Счётчик растёт ДО проверки
// флага: восстановление поднимает флаг и ждёт нуля, и так ни одна работа не
// проскочит между проверкой и заменой базы.
func (a *App) enterWork() bool {
	a.bgActive.Add(1)
	if a.paused() {
		a.bgActive.Add(-1)
		return false
	}
	return true
}

func (a *App) leaveWork() { a.bgActive.Add(-1) }

// EnterWork и LeaveWork — то же для веб-сервера: вебхуки и запросы API.
func (a *App) EnterWork() bool { return a.enterWork() }
func (a *App) LeaveWork()      { a.leaveWork() }

// SetRestart задаёт, как погасить процесс после восстановления базы.
func (a *App) SetRestart(f func()) { a.restartFn = f }

// RestartRequested — процесс гасится ради перезапуска после восстановления.
func (a *App) RestartRequested() bool { return a.restartReq.Load() }

func (a *App) requestRestart() {
	a.restartReq.Store(true)
	if a.restartFn != nil {
		a.restartFn()
	}
}

// answerMaintenance отвечает на апдейт во время восстановления базы.
func (a *App) answerMaintenance(ctx context.Context, u *models.Update) {
	switch {
	case u.PreCheckoutQuery != nil:
		// Оплату звёздами сейчас не принимаем: записать её некуда.
		var from int64
		if u.PreCheckoutQuery.From != nil {
			from = u.PreCheckoutQuery.From.ID
		}
		a.msg.AnswerPreCheckout(ctx, u.PreCheckoutQuery.ID, false, i18n.T(a.lang(from), "bk.maintenance"))
	case u.CallbackQuery != nil:
		a.msg.AnswerCallbackText(ctx, u.CallbackQuery.ID, i18n.T(a.lang(u.CallbackQuery.From.ID), "bk.maintenance"))
	case u.Message != nil && u.Message.SuccessfulPayment != nil:
		a.starsDuringRestore(u.Message)
	case u.Message != nil && u.Message.RefundedPayment != nil:
		// Возврат догонит сверка звёзд после перезапуска.
		a.log.Warn("возврат звёзд пришёл во время восстановления базы", "user", u.Message.Chat.ID)
	case u.Message != nil && u.Message.Chat.Type == models.ChatTypePrivate:
		a.msg.Send(ctx, u.Message.Chat.ID, i18n.T(a.lang(u.Message.Chat.ID), "bk.maintenance"))
	}
}

// starsDuringRestore — оплата звёздами, подтверждённая за миг до техработ.
// Пока восстановление ждёт начатое, её проводим как обычно: замена дождётся.
// Если замена уже идёт, записать некуда — зовём админа.
func (a *App) starsDuringRestore(m *models.Message) {
	a.moneyActive.Add(1)
	if a.restorePhase.Load() == phaseWait {
		a.trackMoney("финализация оплаты Stars", func(c context.Context) {
			a.handleSuccessfulPayment(c, m)
		})
		a.moneyActive.Add(-1)
		return
	}
	a.moneyActive.Add(-1)
	sp := m.SuccessfulPayment
	a.log.Warn("оплата звёздами пришла во время замены базы", "user", m.Chat.ID, "charge", sp.TelegramPaymentChargeID)
	ctx := a.bgContext()
	a.notify(ctx, a.cfg.AdminID, i18n.T(a.lang(a.cfg.AdminID), "bk.alert_stars",
		m.Chat.ID, sp.TotalAmount, html.EscapeString(sp.TelegramPaymentChargeID)))
}

// errText — текст ошибки для админа: без токенов ботов и экранированный.
func (a *App) errText(err error, tokens ...string) string {
	err = redactToken(err, a.cfg.BotToken)
	for _, t := range tokens {
		err = redactToken(err, t)
	}
	return html.EscapeString(err.Error())
}

func (a *App) backupCfg() model.BackupConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	bc := a.bkState
	bc.Guard = nil
	return bc
}

// loadBackupState читает настройки бэкапа. Раньше они жили в конфиге — при
// первом запуске новой версии переезжают в свою таблицу.
func (a *App) loadBackupState(ctx context.Context, cfg *model.BotConfig) {
	st, ok, err := a.store.LoadBackupState(ctx)
	if err != nil {
		a.log.Warn("настройки бэкапа не прочитаны", "err", err)
		return
	}
	if !ok && cfg.Backup != nil {
		st = cfg.Backup
		if err := a.store.SaveBackupState(ctx, st); err != nil {
			a.log.Warn("настройки бэкапа не перенесены", "err", err)
			return
		}
		cfg.Backup = nil
		if err := a.store.SaveConfig(ctx, cfg); err != nil {
			a.log.Warn("конфиг после переноса настроек бэкапа", "err", err)
		}
	}
	if st == nil {
		return
	}
	st.Normalize()
	a.mu.Lock()
	a.bkState = *st
	a.mu.Unlock()
}

// editBackupState правит настройки бэкапа и сохраняет их. Во время
// восстановления — отказ: в памяти старое состояние.
func (a *App) editBackupState(ctx context.Context, f func(*model.BackupConfig)) error {
	if a.restoring.Load() {
		return errRestoring
	}
	a.mu.Lock()
	f(&a.bkState)
	a.bkState.Normalize()
	st := a.bkState
	store := a.store
	a.mu.Unlock()
	if store == nil {
		return errors.New("база не подключена")
	}
	return store.SaveBackupState(ctx, &st)
}

func fmtBackupTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.In(displayTZ).Format("02.01.2006 15:04")
}

// backupDestText — куда сейчас уходят бэкапы, для экрана.
func (a *App) backupDestText(lang string, bc model.BackupConfig) string {
	where := i18n.T(lang, "bk.dest_me")
	if bc.ChatID != 0 {
		where = i18n.T(lang, "bk.dest_chat", strconv.FormatInt(bc.ChatID, 10))
	}
	if bc.BotToken != "" {
		where += i18n.T(lang, "bk.dest_via", html.EscapeString(bc.BotName))
	}
	return where
}

// openBackupMenu — вход в раздел: брошенное ожидание файла или пароля от
// файла не должно перехватить следующий документ или текст.
func (a *App) openBackupMenu(ctx context.Context, chatID int64) {
	a.getUI(chatID).awaitBackup = false
	a.bkPendTake(chatID)
	a.showBackup(ctx, chatID)
}

func (a *App) showBackup(ctx context.Context, chatID int64) {
	a.showBackupNote(ctx, chatID, "")
}

func (a *App) showBackupNote(ctx context.Context, chatID int64, note string) {
	lang := a.lang(chatID)
	bc := a.backupCfg()
	pass := i18n.T(lang, "bk.pass_none")
	if bc.Password != "" {
		pass = i18n.T(lang, "bk.pass_set")
	}
	sched := i18n.T(lang, "bk.sched_off")
	if bc.Hours > 0 {
		sched = i18n.T(lang, "bk.sched_every", bc.Hours)
	}
	last := i18n.T(lang, "bk.last_none")
	if bc.LastAt != "" {
		last = fmtBackupTime(bc.LastAt)
	}
	body := i18n.T(lang, "bk.title", pass, a.backupDestText(lang, bc), sched, last)
	if bc.Password == "" {
		body += "\n\n" + i18n.T(lang, "bk.need_pass")
	}
	held := a.autoPayHeld()
	if held {
		body += "\n\n" + i18n.T(lang, "bk.ap_held")
	}
	if note != "" {
		body = note + "\n\n" + body
	}
	mark := func(h int) string {
		if bc.Hours == h {
			return "✅ "
		}
		return ""
	}
	r1 := []models.InlineKeyboardButton{btn(mark(0)+i18n.T(lang, "bk.btn_off"), "bk:h:0")}
	var r2 []models.InlineKeyboardButton
	for i, h := range model.BackupIntervals {
		b := btn(mark(h)+i18n.T(lang, "bk.btn_hours", h), "bk:h:"+strconv.Itoa(h))
		if i < 3 {
			r1 = append(r1, b)
		} else {
			r2 = append(r2, b)
		}
	}
	rows := [][]models.InlineKeyboardButton{
		r1, r2,
		{btn(i18n.T(lang, "bk.btn_pass"), "bk:pass"), btn(i18n.T(lang, "bk.btn_dest"), "bk:dest")},
		{btn(i18n.T(lang, "bk.btn_now"), "bk:now"), btn(i18n.T(lang, "bk.btn_restore"), "bk:restore")},
	}
	if held {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "bk.btn_ap_on"), "bk:apon")})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:system"), btn(i18n.T(lang, "btn.home"), "menu:home")})
	a.sendSysKB(ctx, chatID, body, rows)
}

// showBackupDest — экран «Куда слать».
func (a *App) showBackupDest(ctx context.Context, chatID int64, note string) {
	lang := a.lang(chatID)
	bc := a.backupCfg()
	body := i18n.T(lang, "bk.dest_title", a.backupDestText(lang, bc))
	if note != "" {
		body = note + "\n\n" + body
	}
	botRow := []models.InlineKeyboardButton{btn(i18n.T(lang, "bk.btn_dest_bot"), "bk:dbot")}
	if bc.BotToken != "" {
		botRow = append(botRow, btn(i18n.T(lang, "bk.btn_dest_nobot"), "bk:dnobot"))
	}
	a.sendSysKB(ctx, chatID, body, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "bk.btn_dest_me"), "bk:dme"), btn(i18n.T(lang, "bk.btn_dest_chat"), "bk:dchat")},
		botRow,
		{btn(i18n.T(lang, "btn.back"), "menu:backup")},
	})
}

func (a *App) onBackup(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	action, arg, _ := strings.Cut(val, ":")
	switch action {
	case "h":
		h, err := strconv.Atoi(arg)
		if err != nil {
			a.showBackup(ctx, chatID)
			return
		}
		err = a.editBackupState(ctx, func(bc *model.BackupConfig) { bc.Hours = h })
		a.afterBackupEdit(ctx, chatID, false, err)
	case "pass":
		a.askBackupInput(ctx, chatID, "bk_pass", i18n.T(lang, "bk.ask_pass", backupPassMin), "menu:backup")
	case "dest":
		a.showBackupDest(ctx, chatID, "")
	case "dme":
		a.setBackupDest(ctx, chatID, 0, a.backupCfg().BotToken, false)
	case "dchat":
		a.askBackupInput(ctx, chatID, "bk_chat", i18n.T(lang, "bk.ask_chat"), "bk:dest")
	case "dbot":
		a.askBackupInput(ctx, chatID, "bk_bot", i18n.T(lang, "bk.ask_bot"), "bk:dest")
	case "dnobot":
		bc := a.backupCfg()
		a.setBackupDest(ctx, chatID, bc.ChatID, "", false)
	case "now":
		a.backupNow(ctx, chatID)
	case "restore":
		a.bkPendTake(chatID)
		resetPendingInputs(ui)
		a.clearTextInput(ctx, chatID)
		ui.awaitBackup = true
		a.sendSysKB(ctx, chatID, i18n.T(lang, "bk.restore_ask"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.back"), "bk:cancel")},
		})
	case "cancel":
		a.openBackupMenu(ctx, chatID)
	case "apply":
		a.applyRestore(ctx, chatID)
	case "apon":
		a.updateGuard(ctx, func(g *model.RestoreGuard) { g.AutoPayHeld = false })
		a.showBackupNote(ctx, chatID, i18n.T(lang, "bk.ap_on"))
	default:
		a.showBackup(ctx, chatID)
	}
}

// askBackupInput взводит ввод значения раздела. Чужие ожидания снимаются:
// иначе они перехватили бы ответ раньше (см. handleMessage).
func (a *App) askBackupInput(ctx context.Context, chatID int64, kind, text, back string) {
	ui := a.getUI(chatID)
	resetPendingInputs(ui)
	a.clearTextInput(ctx, chatID)
	ui.adminInput = kind
	a.askInput(ctx, chatID, text, back)
}

// afterBackupEdit показывает экран после правки настроек бэкапа.
func (a *App) afterBackupEdit(ctx context.Context, chatID int64, dest bool, err error) {
	note := ""
	if err != nil {
		note = i18n.T(a.lang(chatID), "bk.save_fail", a.errText(err))
	}
	if dest {
		a.showBackupDest(ctx, chatID, note)
		return
	}
	a.showBackupNote(ctx, chatID, note)
}

// setBackupDest меняет получателя и отправителя бэкапов. Проверка идёт в
// фоне: она длится до 20 секунд, а обработчик нажатия держит восстановление
// базы, пока не закончится.
func (a *App) setBackupDest(ctx context.Context, chatID, toChat int64, token string, checkBot bool) {
	a.getUI(chatID).adminInput = ""
	a.sendSysKB(ctx, chatID, i18n.T(a.lang(chatID), "bk.checking_dest"), nil)
	bg := a.bgContext()
	a.spawn(func() { a.applyBackupDest(bg, chatID, toChat, token, checkBot) })
}

// applyBackupDest проверяет новое сочетание пробным сообщением и сохраняет
// его: чат, куда писать нельзя, иначе всплыл бы только на первом бэкапе по
// расписанию.
func (a *App) applyBackupDest(ctx context.Context, chatID, toChat int64, token string, checkBot bool) {
	lang := a.lang(chatID)
	name := a.backupCfg().BotName
	if checkBot {
		s, err := a.backupSenderFor(token)
		if err == nil {
			tctx, cancel := context.WithTimeout(ctx, backupNetTimeout)
			name, err = s.Check(tctx)
			cancel()
		}
		if err != nil {
			a.showBackupDest(ctx, chatID, i18n.T(lang, "bk.bot_fail", a.errText(err, token)))
			return
		}
	}
	if token == "" {
		name = ""
	}
	to := toChat
	if to == 0 {
		to = a.cfg.AdminID
	}
	if toChat != 0 || token != "" {
		tctx, cancel := context.WithTimeout(ctx, backupNetTimeout)
		err := a.sendBackupText(tctx, token, to, i18n.T(lang, "bk.dest_test"))
		cancel()
		if err != nil {
			note := i18n.T(lang, "bk.dest_fail", a.errText(err, token))
			if token != "" && toChat == 0 {
				// Бот не может написать первым тому, кто его не запускал.
				note = i18n.T(lang, "bk.bot_start", html.EscapeString(name))
			}
			a.showBackupDest(ctx, chatID, note)
			return
		}
	}
	err := a.editBackupState(ctx, func(bc *model.BackupConfig) {
		bc.ChatID = toChat
		bc.BotToken = token
		bc.BotName = name
	})
	a.afterBackupEdit(ctx, chatID, true, err)
}

// sendBackupText — текст получателю бэкапов: этим ботом или отдельным.
func (a *App) sendBackupText(ctx context.Context, token string, to int64, text string) error {
	if token == "" {
		_, err := a.msg.SendErr(ctx, to, text)
		return err
	}
	s, err := a.backupSenderFor(token)
	if err != nil {
		return err
	}
	return s.SendText(ctx, to, text)
}

// applyBackupInput — ввод пароля бэкапа, чата, токена отдельного бота и
// пароля присланного файла. Сообщение админа к этому моменту уже удалено.
func (a *App) applyBackupInput(ctx context.Context, chatID int64, kind, text string) {
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	text = strings.TrimSpace(text)
	switch kind {
	case "bk_pass":
		n := len([]rune(text))
		if n < backupPassMin || n > backupPassMax {
			a.askInput(ctx, chatID, i18n.T(lang, "bk.pass_bad", backupPassMin, backupPassMax), "menu:backup")
			return
		}
		ui.adminInput = ""
		if err := a.editBackupState(ctx, func(bc *model.BackupConfig) { bc.Password = text }); err != nil {
			a.showBackupNote(ctx, chatID, i18n.T(lang, "bk.save_fail", a.errText(err)))
			return
		}
		a.showBackupNote(ctx, chatID, i18n.T(lang, "bk.pass_saved"))
	case "bk_chat":
		// Только группа или канал (id отрицательный): опечатка в id человека
		// отправляла бы ему всю базу по расписанию.
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil || id >= 0 {
			a.askInput(ctx, chatID, i18n.T(lang, "bk.chat_bad"), "bk:dest")
			return
		}
		a.setBackupDest(ctx, chatID, id, a.backupCfg().BotToken, false)
	case "bk_bot":
		if !botTokenRe.MatchString(text) {
			a.askInput(ctx, chatID, i18n.T(lang, "bk.bot_bad"), "bk:dest")
			return
		}
		if text == a.cfg.BotToken {
			a.askInput(ctx, chatID, i18n.T(lang, "bk.bot_same"), "bk:dest")
			return
		}
		a.setBackupDest(ctx, chatID, a.backupCfg().ChatID, text, true)
	}
}

// restorePassText принимает пароль от присланного файла. true — текст
// был паролем (или пришёл, пока файл проверяется) и обработан.
func (a *App) restorePassText(ctx context.Context, chatID int64, text string) bool {
	a.bkPendMu.Lock()
	p := a.bkPend[chatID]
	if p == nil || !p.wantPass || time.Since(p.at) > bkPendTTL {
		a.bkPendMu.Unlock()
		return false
	}
	busy := p.checking
	p.checking = true
	a.bkPendMu.Unlock()
	lang := a.lang(chatID)
	if busy {
		a.sendSysKB(ctx, chatID, i18n.T(lang, "bk.checking"), nil)
		return true
	}
	a.sendSysKB(ctx, chatID, i18n.T(lang, "bk.checking"), nil)
	bg := a.bgContext()
	pass := strings.TrimSpace(text)
	a.spawn(func() { a.openBackupFile(bg, chatID, p, pass, nil) })
	return true
}

// limitWriter обрывает запись, когда файл перерос лимит Telegram: дальше
// собирать его в памяти незачем.
type limitWriter struct {
	w    io.Writer
	left int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if len(p) > l.left {
		return 0, errBackupTooBig
	}
	l.left -= len(p)
	return l.w.Write(p)
}

// makeBackup снимает бэкап и запирает его паролем. Вызывающий держит bkMu.
func (a *App) makeBackup(ctx context.Context, password string) ([]byte, *storage.BackupInfo, error) {
	lang := a.lang(a.cfg.AdminID)
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return nil, nil, errors.New(i18n.T(lang, "bk.err_no_db"))
	}
	app := a.cfg.Commit
	if app == "" {
		app = "—"
	}
	var gz bytes.Buffer
	limit := tgUploadLimit
	if a.uploadLimitD > 0 {
		limit = a.uploadLimitD
	}
	// У SQLite одно соединение на всех, и выгрузка держит его целиком —
	// поэтому сжатие самое быстрое. Копить несжатую выгрузку в памяти нельзя:
	// на большой базе это сотни мегабайт, а сжатый поток обрывается на 50 МБ.
	level := gzip.DefaultCompression
	if st.Kind() == model.DBSQLite {
		level = gzip.BestSpeed
	}
	zw, err := gzip.NewWriterLevel(&limitWriter{w: &gz, left: limit}, level)
	if err != nil {
		return nil, nil, err
	}
	info, err := st.WriteBackup(ctx, zw, app)
	if err == nil {
		err = zw.Close()
	}
	if errors.Is(err, errBackupTooBig) {
		return nil, nil, &textErr{msg: i18n.T(lang, "bk.err_too_big"), err: errBackupTooBig}
	}
	if err != nil {
		return nil, nil, err
	}
	file, err := backupfile.Seal(gz.Bytes(), password)
	if err != nil {
		return nil, nil, err
	}
	return file, info, nil
}

func backupFileName(info *storage.BackupInfo) string {
	t, err := time.Parse(time.RFC3339, info.CreatedAt)
	if err != nil {
		t = time.Now()
	}
	return "chill-remna-bot-backup-" + t.In(displayTZ).Format("2006-01-02-1504") + ".bak"
}

// backupDest — куда и чем слать файл.
type backupDest struct {
	chat  int64
	token string // токен отдельного бота; пусто — шлёт этот бот
}

// configuredDest — получатель бэкапов по настройкам.
func (a *App) configuredDest(bc model.BackupConfig) backupDest {
	to := bc.ChatID
	if to == 0 {
		to = a.cfg.AdminID
	}
	return backupDest{chat: to, token: bc.BotToken}
}

// sendBackupFile снимает бэкап и шлёт его. Вызывающий держит bkMu.
func (a *App) sendBackupFile(ctx context.Context, d backupDest, password, titleKey string) error {
	file, info, err := a.makeBackup(ctx, password)
	if err != nil {
		return err
	}
	lang := a.lang(a.cfg.AdminID)
	caption := i18n.T(lang, titleKey) + "\n" + i18n.T(lang, "bk.caption",
		fmtBackupTime(info.CreatedAt), info.Rows["users"], info.Rows["payments"])
	if len(file) > maxDumpBytes {
		caption += "\n" + i18n.T(lang, "bk.caption_big")
	}
	name := backupFileName(info)
	if d.token != "" {
		s, err := a.backupSenderFor(d.token)
		if err == nil {
			err = s.SendFile(ctx, d.chat, name, file, caption)
		}
		if err != nil {
			return errors.New(i18n.T(lang, "bk.err_not_sent", redactToken(redactToken(err, d.token), a.cfg.BotToken).Error()))
		}
		return nil
	}
	// В личном чате с админом под файлом — кнопка «Убрать»: сам бот файл
	// не удаляет никогда. В группе её не ставим — нажать мог бы любой.
	var rows [][]models.InlineKeyboardButton
	if d.chat == a.cfg.AdminID {
		rows = [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "bk.btn_remove"), cbClose+":close")}}
	}
	if _, err := a.msg.SendFile(ctx, d.chat, name, file, caption, rows); err != nil {
		return errors.New(i18n.T(lang, "bk.err_not_sent", redactToken(err, a.cfg.BotToken).Error()))
	}
	return nil
}

// markBackupSent запоминает время бэкапа: от него считается расписание.
func (a *App) markBackupSent(ctx context.Context, at time.Time) {
	if err := a.editBackupState(ctx, func(bc *model.BackupConfig) { bc.LastAt = at.UTC().Format(time.RFC3339) }); err != nil {
		a.log.Warn("время бэкапа не сохранено", "err", err)
	}
}

func (a *App) backupNow(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	bc := a.backupCfg()
	if bc.Password == "" {
		a.showBackupNote(ctx, chatID, i18n.T(lang, "bk.need_pass"))
		return
	}
	if !a.bkMu.TryLock() {
		a.showBackupNote(ctx, chatID, i18n.T(lang, "bk.busy"))
		return
	}
	a.sendSysKB(ctx, chatID, i18n.T(lang, "bk.making"), nil)
	bg := a.bgContext()
	a.spawn(func() {
		defer a.bkMu.Unlock()
		if err := a.sendBackupFile(bg, a.configuredDest(bc), bc.Password, "bk.caption_title"); err != nil {
			a.showBackupNote(bg, chatID, i18n.T(lang, "bk.fail", a.errText(err, bc.BotToken)))
			return
		}
		a.markBackupSent(bg, time.Now())
		a.showBackupNote(bg, chatID, i18n.T(lang, "bk.sent"))
	})
}

// RunBackups шлёт бэкапы по расписанию и после восстановления базы
// сверяет сроки подписок с панелью.
func (a *App) RunBackups(ctx context.Context) {
	t := time.NewTicker(backupTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			a.guardTick(ctx, now)
			a.backupOnce(ctx, now)
		}
	}
}

// backupDue — пора ли слать бэкап по расписанию.
func backupDue(bc model.BackupConfig, now time.Time) bool {
	if bc.Hours <= 0 || bc.Password == "" {
		return false
	}
	last, err := time.Parse(time.RFC3339, bc.LastAt)
	if err != nil {
		return true
	}
	// Часы на сервере ушли назад: метка «из будущего» не должна выключать
	// бэкапы до тех пор, пока время её не догонит.
	if last.After(now) {
		return true
	}
	return now.Sub(last) >= time.Duration(bc.Hours)*time.Hour
}

func (a *App) backupOnce(ctx context.Context, now time.Time) {
	if !a.enterWork() {
		return
	}
	defer a.leaveWork()
	a.mu.Lock()
	installed := a.installed()
	a.mu.Unlock()
	bc := a.backupCfg()
	if !installed || !backupDue(bc, now) {
		return
	}
	if !a.bkMu.TryLock() {
		return
	}
	defer a.bkMu.Unlock()
	if now.Before(a.bkNextTry) {
		return
	}
	if err := a.sendBackupFile(ctx, a.configuredDest(bc), bc.Password, "bk.caption_title"); err != nil {
		retry := backupRetryAfter(err, bc.Hours)
		a.bkNextTry = now.Add(retry)
		a.log.Warn("бэкап по расписанию не отправлен", "err", err)
		lang := a.lang(a.cfg.AdminID)
		a.notify(ctx, a.cfg.AdminID, i18n.T(lang, "bk.alert_fail", a.errText(err, bc.BotToken), int(retry/time.Hour)))
		return
	}
	a.bkNextTry = time.Time{}
	a.markBackupSent(ctx, now)
}

// backupRetryAfter — пауза после неудачного бэкапа по расписанию. Слишком
// большой файл не уменьшится через час — повтор по расписанию, а не каждый
// час с тревогой.
func backupRetryAfter(err error, hours int) time.Duration {
	if errors.Is(err, errBackupTooBig) {
		return max(backupRetry, time.Duration(hours)*time.Hour)
	}
	return backupRetry
}

func (a *App) bkPendSet(chatID int64, p *bkPending) {
	a.bkPendMu.Lock()
	defer a.bkPendMu.Unlock()
	if a.bkPend == nil {
		a.bkPend = map[int64]*bkPending{}
	}
	a.bkPend[chatID] = p
}

func (a *App) bkPendPeek(chatID int64) *bkPending {
	a.bkPendMu.Lock()
	defer a.bkPendMu.Unlock()
	return a.bkPend[chatID]
}

// bkPendReplace меняет файл, только если он всё ещё тот же: админ мог нажать
// «Отмена», пока файл проверялся, и тогда экран подтверждения не нужен.
func (a *App) bkPendReplace(chatID int64, old, p *bkPending) bool {
	a.bkPendMu.Lock()
	defer a.bkPendMu.Unlock()
	if a.bkPend[chatID] != old {
		return false
	}
	if p == nil {
		delete(a.bkPend, chatID)
	} else {
		a.bkPend[chatID] = p
	}
	return true
}

// bkPendTake забирает файл: подтвердить восстановление дважды нельзя.
func (a *App) bkPendTake(chatID int64) *bkPending {
	a.bkPendMu.Lock()
	defer a.bkPendMu.Unlock()
	p := a.bkPend[chatID]
	delete(a.bkPend, chatID)
	return p
}

// handleBackupDoc принимает файл бэкапа для восстановления. Ожидание
// пароля живёт в самом файле (bkPend), а не в состоянии ввода: фоновая
// горутина состояние ввода не трогает, а «Отмена» снимает файл целиком.
func (a *App) handleBackupDoc(ctx context.Context, m *models.Message) {
	chatID := m.Chat.ID
	lang := a.lang(chatID)
	back := [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.back"), "bk:cancel")}}
	if m.Document.FileSize > maxDumpBytes {
		a.sendSysKB(ctx, chatID, i18n.T(lang, "bk.too_big"), back)
		return
	}
	// Метка файла ставится сразу: «Отмена» во время скачивания её снимет,
	// и скачанное уже никуда не ляжет.
	slot := &bkPending{at: time.Now()}
	a.bkPendSet(chatID, slot)
	fileID := m.Document.FileID
	pass := a.backupCfg().Password
	a.sendSysKB(ctx, chatID, i18n.T(lang, "bk.checking"), nil)
	bg := a.bgContext()
	a.spawn(func() {
		dctx, cancel := context.WithTimeout(bg, dumpDownloadTimeout)
		data, err := a.msg.Download(dctx, fileID)
		cancel()
		if err != nil || !backupfile.IsBackup(data) {
			if !a.bkPendReplace(chatID, slot, nil) {
				return
			}
			text := i18n.T(lang, "bk.not_backup")
			if err != nil {
				text = i18n.T(lang, "bk.download_fail", a.errText(err))
			}
			a.sendSysKB(bg, chatID, text, back)
			return
		}
		p := &bkPending{at: slot.at, file: data}
		if !a.bkPendReplace(chatID, slot, p) {
			return
		}
		// Сначала пробуем пароль из настроек: обычно файл заперт им же.
		if pass != "" {
			if gz, err := backupfile.Open(data, pass); err == nil {
				a.openBackupFile(bg, chatID, p, pass, gz)
				return
			}
		}
		a.bkPendMu.Lock()
		p.wantPass = true
		a.bkPendMu.Unlock()
		a.askRestorePass(bg, chatID, "bk.ask_rpass")
	})
}

func (a *App) askRestorePass(ctx context.Context, chatID int64, key string) {
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, i18n.T(lang, key), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.cancel"), "bk:cancel")},
	})
}

func gunzipOpener(gz []byte) func() (io.Reader, error) {
	return func() (io.Reader, error) {
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			return nil, storage.ErrBackupFormat
		}
		return io.LimitReader(zr, backupMaxPlain), nil
	}
}

// openBackupFile расшифровывает присланный файл (gz — если уже
// расшифрован), проверяет его и показывает сводку перед восстановлением.
func (a *App) openBackupFile(ctx context.Context, chatID int64, p *bkPending, password string, gz []byte) {
	lang := a.lang(chatID)
	back := [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.back"), "bk:cancel")}}
	if gz == nil {
		var err error
		if gz, err = backupfile.Open(p.file, password); err != nil {
			a.bkPendMu.Lock()
			p.checking = false
			live := a.bkPend[chatID] == p
			a.bkPendMu.Unlock()
			if live {
				a.askRestorePass(ctx, chatID, "bk.rpass_bad")
			}
			return
		}
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		if a.bkPendReplace(chatID, p, nil) {
			a.sendSysKB(ctx, chatID, i18n.T(lang, "bk.fail", i18n.T(lang, "bk.err_no_db")), back)
		}
		return
	}
	info, err := st.CheckBackup(ctx, gunzipOpener(gz))
	if err != nil {
		if a.bkPendReplace(chatID, p, nil) {
			a.sendSysKB(ctx, chatID, a.backupErrText(lang, err), back)
		}
		return
	}
	// Сам файл больше не нужен: для замены хватает расшифрованного.
	np := &bkPending{at: p.at, gz: gz, password: password, info: info}
	if !a.bkPendReplace(chatID, p, np) {
		return
	}
	a.showRestorePreview(ctx, chatID, np)
}

func (a *App) showRestorePreview(ctx context.Context, chatID int64, p *bkPending) {
	lang := a.lang(chatID)
	info := p.info
	db := html.EscapeString(info.DB)
	switch info.DB {
	case model.DBPostgres:
		db = "PostgreSQL"
	case model.DBSQLite:
		db = "SQLite"
	}
	text := i18n.T(lang, "bk.preview",
		html.EscapeString(fmtBackupTime(info.CreatedAt)), html.EscapeString(info.App), db,
		info.Rows["users"], info.Rows["payments"], info.Rows["plans"], info.Rows["promo_codes"])
	a.sendSysKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "bk.btn_apply"), "bk:apply")},
		{btn(i18n.T(lang, "btn.cancel"), "bk:cancel")},
	})
}

func (a *App) backupErrText(lang string, err error) string {
	switch {
	case errors.Is(err, storage.ErrBackupNewer):
		return i18n.T(lang, "bk.err_newer")
	case errors.Is(err, storage.ErrBackupFormat):
		return i18n.T(lang, "bk.err_format")
	}
	return i18n.T(lang, "bk.fail", a.errText(err))
}

func (a *App) applyRestore(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	p := a.bkPendTake(chatID)
	if p == nil || p.info == nil || time.Since(p.at) > bkPendTTL {
		a.showBackup(ctx, chatID)
		return
	}
	if !a.bkMu.TryLock() {
		a.bkPendSet(chatID, p)
		a.showBackupNote(ctx, chatID, i18n.T(lang, "bk.busy"))
		return
	}
	// Фаза ставится до флага: оплата звёздами, пришедшая сразу после
	// флага, должна увидеть «ждём», а не «заменяем».
	if a.paused() {
		a.bkPendSet(chatID, p)
		a.bkMu.Unlock()
		a.showBackupNote(ctx, chatID, i18n.T(lang, "bk.busy"))
		return
	}
	a.restorePhase.Store(phaseWait)
	a.restoring.Store(true)
	a.sendSysKB(ctx, chatID, i18n.T(lang, "bk.restore_wait"), nil)
	bg := a.bgContext()
	a.spawn(func() {
		defer a.bkMu.Unlock()
		a.runRestore(bg, chatID, p)
	})
}

// waitQuiet ждёт, пока доиграют начатые оплаты, вебхуки и фоновые проходы,
// и переводит восстановление в фазу замены. false — не дождались.
func (a *App) waitQuiet(ctx context.Context) bool {
	wait := quietWait
	if a.quietWaitD > 0 {
		wait = a.quietWaitD
	}
	deadline := time.Now().Add(wait)
	for {
		if a.bgActive.Load() == 0 && a.moneyActive.Load() == 0 {
			a.restorePhase.Store(phaseReplace)
			// Оплата звёздами могла начаться ровно между проверкой и сменой
			// фазы — тогда ждём и её.
			if a.moneyActive.Load() == 0 {
				return true
			}
			a.restorePhase.Store(phaseWait)
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (a *App) endRestore() {
	a.restorePhase.Store(0)
	a.restoring.Store(false)
}

// runRestore — само восстановление. Флаг restoring уже поднят.
func (a *App) runRestore(ctx context.Context, chatID int64, p *bkPending) {
	lang := a.lang(chatID)
	back := [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.back"), "menu:backup")}}
	if !a.waitQuiet(ctx) {
		a.endRestore()
		a.sendSysKB(ctx, chatID, i18n.T(lang, "bk.not_quiet"), back)
		return
	}
	// Граница «до восстановления» — момент, когда закончились начатые
	// оплаты: всё, что сверки найдут раньше, бот сам не выдаёт.
	guard := &model.RestoreGuard{
		At:          time.Now().UTC().Format(time.RFC3339),
		BackupAt:    p.info.CreatedAt,
		Chat:        chatID,
		AutoPayHeld: true,
	}

	// Страховка: бэкап текущей базы уходит админу ДО замены и тем же
	// паролем, что у восстанавливаемого файла. Не ушёл — не заменяем.
	if err := a.sendBackupFile(ctx, backupDest{chat: chatID}, p.password, "bk.caption_before"); err != nil {
		a.endRestore()
		a.sendSysKB(ctx, chatID, i18n.T(lang, "bk.before_fail", a.errText(err)), back)
		return
	}

	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	// cfgSaveMu держим всю замену: сохранение конфига, начатое раньше, должно
	// доехать ДО неё, а не лечь поверх восстановленного.
	a.cfgSaveMu.Lock()
	err := st.RestoreBackup(ctx, gunzipOpener(p.gz), guard)
	a.cfgSaveMu.Unlock()
	if err != nil {
		a.endRestore()
		text := i18n.T(lang, "bk.restore_fail", a.errText(err))
		if errors.Is(err, storage.ErrBackupNewer) || errors.Is(err, storage.ErrBackupFormat) {
			text = a.backupErrText(lang, err) + "\n" + i18n.T(lang, "bk.unchanged")
		}
		a.sendSysKB(ctx, chatID, text, back)
		return
	}
	a.log.Info("база восстановлена из бэкапа, перезапуск", "backup_at", p.info.CreatedAt)
	a.sendSysKB(ctx, chatID, i18n.T(lang, "bk.restored", html.EscapeString(fmtBackupTime(p.info.CreatedAt))), nil)
	a.requestRestart()
}

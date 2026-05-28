// Package app связывает воедино конфиг, хранилище, клиент панели и
// Telegram-бота, и реализует мастер первичной установки (FSM).
package app

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/config"
	"remnabot/internal/crypto"
	"remnabot/internal/hostctl"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
)

// messenger абстрагирует отправку сообщений в Telegram — это «шов» для тестов
// (в проде botMessenger поверх *bot.Bot, в тестах — фейк-перехватчик).
type messenger interface {
	// Send* возвращают id отправленного сообщения (0 при ошибке) для трекинга «экрана».
	Send(ctx context.Context, chatID int64, text string) int
	SendKB(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) int
	SendPhoto(ctx context.Context, chatID int64, fileID, caption string, rows [][]models.InlineKeyboardButton) int
	// SendPhotoCacheable отправляет картинку с цепочкой источников:
	//   1) cachedFileID (если есть) — мгновенно, без скачивания;
	//   2) embedBytes (если есть) — встроенный в бинарь JPG (locally-shipped);
	//   3) urlFallback — последний резерв (например, Unsplash).
	// Возвращает (msgID, newFileID); newFileID непустой когда фото пришло
	// НЕ из cachedFileID — его надо перекэшировать в media_cache.
	SendPhotoCacheable(ctx context.Context, chatID int64, cachedFileID string, embedBytes []byte, urlFallback, caption string, rows [][]models.InlineKeyboardButton) (msgID int, newFileID string)
	SendBanner(ctx context.Context, chatID int64, photo models.InputFile, caption string, entities []models.MessageEntity, rm models.ReplyMarkup) int
	Delete(ctx context.Context, chatID int64, msgID int)
	RemoveKeyboard(ctx context.Context, chatID int64)
	// SetCommandKeyboard ставит постоянную reply-клавиатуру (кнопка под полем ввода).
	SetCommandKeyboard(ctx context.Context, chatID int64, label string)
	AnswerCallback(ctx context.Context, id string)
	// SendInvoice выставляет счёт (для Telegram Stars currency=XTR, providerToken="").
	SendInvoice(ctx context.Context, chatID int64, title, description, payload, currency string, amount int)
	AnswerPreCheckout(ctx context.Context, id string, ok bool, errMsg string)
}

type App struct {
	cfg     *config.Config
	crypter *crypto.Crypter
	log     *slog.Logger
	b       *bot.Bot

	ctl *hostctl.Controller
	msg messenger
	// newStore — «шов» открытия хранилища (в тестах подменяется на фейк);
	// nil → используется storage.Open(... a.crypter).
	newStore func(kind, dsn string) (storage.Storage, error)

	mu     sync.Mutex
	store  storage.Storage
	botCfg *model.BotConfig
	panel  *remnawave.Client
	wiz    map[int64]*wizard
	ui     map[int64]*uiState

	// single-message UI: на бота держим РОВНО одно «экранное» сообщение на chatID;
	// каждое новое экранное сообщение удаляет предыдущее (чистота чата).
	scrMu  sync.Mutex
	screen map[int64][]int
	kbSet  map[int64]bool // у кого уже выставлена постоянная reply-клавиатура

	// subCache — мини-кэш ответа panel.Subscription для navRow (см. userHasSub).
	// userHasSub вызывается на каждый рендер главной/меню — без кэша это
	// делало бы GET /api/users/by-telegram-id/<id> на каждый клик.
	subMu    sync.Mutex
	subCache map[int64]subCacheEntry
}

// subCacheEntry — ответ панели + срок жизни. TTL короткий (30s), чтобы
// сразу после реальной покупки/удаления юзер увидел корректную кнопку.
type subCacheEntry struct {
	has      bool
	expireAt time.Time
}

func New(cfg *config.Config, crypter *crypto.Crypter, log *slog.Logger) *App {
	return &App{cfg: cfg, crypter: crypter, log: log, ctl: hostctl.New(), wiz: map[int64]*wizard{}, ui: map[int64]*uiState{},
		screen: map[int64][]int{}, kbSet: map[int64]bool{}}
}

// Bootstrap при старте подхватывает ранее выбранную БД и конфиг.
// До первого выбора в мастере БД не открывается (store остаётся nil).
func (a *App) Bootstrap(ctx context.Context) error {
	bs, err := storage.LoadBootstrap(a.cfg.DataDir)
	if err != nil {
		return err
	}
	if bs == nil {
		if a.cfg.DBKind != "" {
			if err := a.openStore(a.cfg.DBKind, a.dsnForEnv(a.cfg.DBKind)); err != nil {
				return err
			}
		}
		return a.loadConfigIfStore(ctx)
	}
	if err := a.openStore(bs.DBKind, bs.DSN); err != nil {
		return err
	}
	return a.loadConfigIfStore(ctx)
}

func (a *App) dsnForEnv(kind string) string {
	if kind == model.DBPostgres {
		return a.cfg.DatabaseURL
	}
	return filepath.Join(a.cfg.DataDir, "bot.db")
}

func (a *App) loadConfigIfStore(ctx context.Context) error {
	if a.store == nil {
		return nil
	}
	cfg, ok, err := a.store.LoadConfig(ctx)
	if err != nil {
		return err
	}
	if ok && cfg.Installed {
		cfg.NormalizePricing()
		cfg.NormalizeReminders()
		a.botCfg = cfg
		a.panel = remnawave.New(cfg.Panel)
		if cfg.Panel.Mode == model.ModeLocal && a.ctl != nil && a.ctl.Available() {
			if err := a.ctl.ConnectPanelNetwork(ctx); err != nil {
				a.log.Warn("подключение к сети панели", "err", err)
			}
		}
		a.log.Info("конфигурация загружена, бот установлен", "db", a.store.Kind())
	}
	return nil
}

func (a *App) openOne(kind, dsn string) (storage.Storage, error) {
	if a.newStore != nil {
		return a.newStore(kind, dsn)
	}
	return storage.Open(kind, dsn, a.crypter)
}

// openStore открывает БД, прогоняет миграции, запоминает выбор в bootstrap.json.
func (a *App) openStore(kind, dsn string) error {
	st, err := a.openOne(kind, dsn)
	if err != nil {
		return err
	}
	if err := st.Migrate(context.Background()); err != nil {
		_ = st.Close()
		return err
	}
	if a.store != nil {
		_ = a.store.Close()
	}
	a.store = st
	return storage.SaveBootstrap(a.cfg.DataDir, &storage.Bootstrap{DBKind: kind, DSN: dsn})
}

// switchStore открывает новое хранилище и при наличии старого переносит данные,
// затем переключает активное хранилище (старое закрывается после переноса).
func (a *App) switchStore(ctx context.Context, kind, dsn string) error {
	newSt, err := a.openOne(kind, dsn)
	if err != nil {
		return err
	}
	if err := newSt.Migrate(ctx); err != nil {
		_ = newSt.Close()
		return err
	}
	a.mu.Lock()
	old := a.store
	a.mu.Unlock()
	if old != nil {
		if err := storage.Transfer(ctx, old, newSt); err != nil {
			_ = newSt.Close()
			return err
		}
	}
	a.mu.Lock()
	a.store = newSt
	a.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return storage.SaveBootstrap(a.cfg.DataDir, &storage.Bootstrap{DBKind: kind, DSN: dsn})
}

// Run создаёт бота и запускает long polling до отмены контекста.
func (a *App) Run(ctx context.Context) error {
	b, err := bot.New(a.cfg.BotToken, bot.WithDefaultHandler(a.handle))
	if err != nil {
		return err
	}
	a.b = b
	a.msg = botMessenger{b: b, log: a.log}
	a.notifyUpdated(ctx)
	a.log.Info("бот запущен")
	b.Start(ctx)
	return nil
}

func (a *App) installed() bool {
	return a.botCfg != nil && a.botCfg.Installed
}

func (a *App) handle(ctx context.Context, b *bot.Bot, update *models.Update) {
	switch {
	case update.CallbackQuery != nil:
		a.handleCallback(ctx, update.CallbackQuery)
	case update.PreCheckoutQuery != nil:
		a.handlePreCheckout(ctx, update.PreCheckoutQuery)
	case update.Message != nil && update.Message.SuccessfulPayment != nil:
		a.handleSuccessfulPayment(ctx, update.Message)
	case update.Message != nil && update.Message.Text != "":
		a.handleMessage(ctx, update.Message)
	case update.Message != nil && len(update.Message.Photo) > 0:
		a.handlePhoto(ctx, update.Message)
	}
}

func (a *App) handleMessage(ctx context.Context, m *models.Message) {
	chatID := m.Chat.ID
	userID := int64(0)
	firstName, username := "", ""
	if m.From != nil {
		userID = m.From.ID
		firstName = m.From.FirstName
		username = m.From.Username
	}
	text := strings.TrimSpace(m.Text)
	isAdmin := userID == a.cfg.AdminID
	a.rememberUser(ctx, chatID, username, firstName)
	if !isAdmin && a.userBlocked(ctx, chatID) {
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "user.you_blocked"))
		return
	}

	// Чистота чата: любая ручная команда пользователя (/start, /buy, …) тут же
	// удаляется — бот сам покажет нужный экран. Команды-«хвосты» в истории
	// чата мешают single-message UI.
	if strings.HasPrefix(text, "/") {
		a.msg.Delete(ctx, chatID, m.ID)
	}

	// Нажатие постоянной reply-кнопки «Главная».
	if a.installed() && isHomeText(text) {
		a.msg.Delete(ctx, chatID, m.ID)
		a.enterHome(ctx, chatID, isAdmin, firstName, username)
		return
	}

	switch {
	case strings.HasPrefix(text, "/setup"):
		if !isAdmin {
			a.send(ctx, chatID, i18n.T(i18n.Fallback, "setup.not_admin"))
			return
		}
		a.startWizard(ctx, chatID)
		return
	case strings.HasPrefix(text, "/start"):
		if !a.installed() {
			if !isAdmin {
				a.send(ctx, chatID, i18n.T(i18n.Fallback, "setup.not_admin"))
				return
			}
			a.startWizard(ctx, chatID)
			return
		}
		a.enterHome(ctx, chatID, isAdmin, firstName, username)
		return
	case strings.HasPrefix(text, "/status"):
		a.handleStatus(ctx, chatID)
		return
	case strings.HasPrefix(text, "/update"):
		if isAdmin {
			a.handleUpdate(ctx, chatID)
		}
		return
	case strings.HasPrefix(text, "/buy"):
		if a.installed() {
			a.showPlans(ctx, chatID)
		}
		return
	case strings.HasPrefix(text, "/p2p"):
		if isAdmin {
			a.showP2PAdmin(ctx, chatID)
		}
		return
	case strings.HasPrefix(text, "/emoji"):
		if isAdmin {
			a.showEmojiGrid(ctx, chatID)
		}
		return
	case strings.HasPrefix(text, "/welcome"):
		if isAdmin {
			a.showWelcomeAdmin(ctx, chatID)
		}
		return
	}

	if !isAdmin {
		return
	}
	// Введённый админом текст (цена, реквизиты, токен и т.п.) — это часть «экрана»:
	// удаляем сообщение пользователя, чтобы в чате оставалось одно сообщение бота.
	a.msg.Delete(ctx, chatID, m.ID)
	ui := a.getUI(chatID)
	if ui.welcomeAwait == "txt" {
		a.setWelcomeText(ctx, chatID, m)
		return
	}
	if ui.welcomeAwait == "img" {
		a.setWelcomeImageURL(ctx, chatID, text)
		return
	}
	if ui.awaitEmojiFor != "" {
		a.setEmojiFor(ctx, chatID, m)
		return
	}
	a.mu.Lock()
	wizActive := a.wiz[chatID] != nil
	a.mu.Unlock()
	if wizActive {
		a.handleWizardText(ctx, chatID, text)
		return
	}
	a.handleAdminText(ctx, chatID, text)
}

func (a *App) handleStatus(ctx context.Context, chatID int64) {
	a.mu.Lock()
	installed := a.installed()
	panel := a.panel
	var dbKind, mode, methods string
	if installed {
		dbKind = a.botCfg.DBKind
		mode = a.botCfg.Panel.Mode
		methods = enabledMethods(a.botCfg)
	}
	lang := a.lang(chatID)
	a.mu.Unlock()

	isAdmin := chatID == a.cfg.AdminID
	rows := a.statusNavRows(lang, isAdmin)

	if !installed || panel == nil {
		a.sendKB(ctx, chatID, i18n.T(lang, "installed.hint"), rows)
		return
	}
	count, err := panel.SystemStats(ctx)
	if err != nil {
		a.sendKB(ctx, chatID, i18n.T(lang, "status.fail", err.Error()), rows)
		return
	}
	text := i18n.T(lang, "status.line", count, dbKind, mode, methods)
	if isAdmin {
		text += "\n\n" + i18n.T(lang, "status.res_title") + "\n" + resourceStats()
	}
	a.sendKB(ctx, chatID, text, rows)
}

// statusNavRows — кнопки под сервисным сообщением: «Назад» (админу, в Управление) + «Главная».
func (a *App) statusNavRows(lang string, isAdmin bool) [][]models.InlineKeyboardButton {
	if isAdmin {
		return [][]models.InlineKeyboardButton{{
			btn(i18n.T(lang, "btn.back"), "menu:manage"),
			btn(i18n.T(lang, "btn.home"), "menu:home"),
		}}
	}
	return [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.home"), "menu:home")}}
}

// enabledMethods — список включённых способов оплаты для сервисного статуса.
func enabledMethods(cfg *model.BotConfig) string {
	var m []string
	if cfg.P2P.Enabled {
		m = append(m, "P2P")
	}
	if cfg.Stars.Enabled {
		m = append(m, "Stars")
	}
	if cfg.YooKassa.Enabled {
		m = append(m, "ЮKassa")
	}
	if len(m) == 0 {
		return "—"
	}
	return strings.Join(m, ", ")
}

// handleUpdate запускает самообновление образа через одноразовый контейнер.
// «Запускаю обновление…» шлём напрямую через messenger (а не через emit/send),
// чтобы получить msgID и сохранить его в marker'е — после рестарта бот его
// удалит и пришлёт «update.done» одним свежим сообщением.
func (a *App) handleUpdate(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if a.ctl == nil || !a.ctl.Available() {
		a.send(ctx, chatID, i18n.T(lang, "update.not_available"))
		return
	}
	startMsgID := a.msg.SendKB(ctx, chatID,
		a.applyPremium(i18n.T(lang, "update.starting")),
		[][]models.InlineKeyboardButton{backHomeRow(lang)})
	marker := filepath.Join(a.cfg.DataDir, "update.pending")
	_ = os.WriteFile(marker, []byte(strconv.FormatInt(chatID, 10)+":"+strconv.Itoa(startMsgID)), 0o600)
	if err := a.ctl.SelfUpdate(ctx); err != nil {
		_ = os.Remove(marker)
		a.send(ctx, chatID, i18n.T(lang, "update.fail", err.Error()))
	}
}

// notifyUpdated при старте сообщает админу, что бот обновлён (если был /update).
// Если в marker'е записан id «Запускаю обновление…» — удаляем его, чтобы это
// сообщение не висело в чате (его никто не закроет, пока бот был офлайн).
func (a *App) notifyUpdated(ctx context.Context) {
	marker := filepath.Join(a.cfg.DataDir, "update.pending")
	data, err := os.ReadFile(marker)
	if err != nil {
		return
	}
	_ = os.Remove(marker)
	// Формат marker'а: "<chatID>:<msgID>" (msgID может быть 0).
	parts := strings.SplitN(strings.TrimSpace(string(data)), ":", 2)
	var chatID int64
	var msgID int
	if len(parts) == 2 {
		chatID, _ = strconv.ParseInt(parts[0], 10, 64)
		msgID, _ = strconv.Atoi(parts[1])
	}
	if chatID == 0 {
		chatID = a.cfg.AdminID
	}
	if msgID != 0 && a.msg != nil {
		a.msg.Delete(ctx, chatID, msgID)
	}
	a.notify(ctx, chatID, i18n.T(a.botLang(), "update.done"))
}

// --- single-message UI: «экран» = последние сообщения бота для chatID ---

// emit отправляет «экранное» сообщение и УДАЛЯЕТ предыдущее экранное сообщение
// этого чата — на боте всегда остаётся ровно одно сообщение (строгая чистота).
// Постоянные уведомления (notify*) сюда не попадают и не трекаются.
func (a *App) emit(ctx context.Context, chatID int64, send func() int) {
	a.scrMu.Lock()
	if a.screen == nil {
		a.screen = map[int64][]int{}
	}
	toDelete := a.screen[chatID]
	a.screen[chatID] = nil
	a.scrMu.Unlock()

	for _, id := range toDelete {
		a.msg.Delete(ctx, chatID, id)
	}
	id := send()
	if id != 0 {
		a.scrMu.Lock()
		a.screen[chatID] = []int{id}
		a.scrMu.Unlock()
	}
}

// --- отправка сообщений ---
//
// send/sendKB/sendBanner — «экранные»: участвуют в single-message UI.
// notify*/SendPhoto к чужому чату — постоянные (модерация, уведомления, ссылки
// пользователю): не трекаются и не удаляют чужой экран.

func (a *App) send(ctx context.Context, chatID int64, text string) {
	t := a.applyPremium(text)
	a.emit(ctx, chatID, func() int { return a.msg.Send(ctx, chatID, t) })
}

func (a *App) sendKB(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) {
	t := a.applyPremium(text)
	a.emit(ctx, chatID, func() int { return a.msg.SendKB(ctx, chatID, t, rows) })
}

func (a *App) sendBanner(ctx context.Context, chatID int64, photo models.InputFile, caption string, ents []models.MessageEntity, rm models.ReplyMarkup) {
	a.emit(ctx, chatID, func() int { return a.msg.SendBanner(ctx, chatID, photo, caption, ents, rm) })
}

// sendKBSection — экранное сообщение С КАРТИНКОЙ раздела (вместо обычного sendKB).
//  1. URL раздела берётся из assets.URL(section). Если раздела нет в каталоге —
//     мягкий фолбэк на sendKB (текст без картинки), чтобы UX не ломался.
//  2. Сначала пытаемся отдать через закэшированный file_id (если есть в media_cache).
//  3. Если file_id ещё нет — Telegram скачает по URL, мы получим новый file_id
//     и сохраним его в media_cache для последующих отправок.
//
// Подпись и клавиатура — как у sendKB; премиум-эмодзи применяются.
func (a *App) sendKBSection(ctx context.Context, chatID int64, section, caption string, rows [][]models.InlineKeyboardButton) {
	url := assets.URL(section)
	if url == "" {
		a.sendKB(ctx, chatID, caption, rows)
		return
	}
	t := a.applyPremium(caption)
	var cached string
	if a.store != nil {
		if id, ok, _ := a.store.LoadMediaFileID(ctx, section); ok {
			cached = id
		}
	}
	var newFileID string
	embed := assets.Bytes(section)
	a.emit(ctx, chatID, func() int {
		id, nf := a.msg.SendPhotoCacheable(ctx, chatID, cached, embed, url, t, rows)
		newFileID = nf
		return id
	})
	if a.store != nil && newFileID != "" && newFileID != cached {
		if err := a.store.SaveMediaFileID(ctx, section, newFileID); err != nil {
			a.log.Warn("media_cache save", "section", section, "err", err)
		}
	}
}

// notify — постоянное сообщение (вне single-message UI). Чтобы не «висело»
// в чате, бот всегда добавляет в конец кнопку «✕ Закрыть» (callback x:close),
// которая удаляет это сообщение.
func (a *App) notify(ctx context.Context, chatID int64, text string) {
	a.msg.SendKB(ctx, chatID, a.applyPremium(text), [][]models.InlineKeyboardButton{backHomeRow(a.lang(chatID))})
}

// notifyKB как notify, но с пользовательскими кнопками выше «✕ Закрыть».
// Кнопка закрытия добавляется автоматически последним рядом — нажатие удаляет
// всё уведомление (и саму себя), чтобы ничего не «осиротело» в чате.
func (a *App) notifyKB(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) {
	withClose := append(append([][]models.InlineKeyboardButton{}, rows...), backHomeRow(a.lang(chatID)))
	a.msg.SendKB(ctx, chatID, a.applyPremium(text), withClose)
}

func (a *App) notifyPhoto(ctx context.Context, chatID int64, fileID, caption string, rows [][]models.InlineKeyboardButton) {
	withClose := append(append([][]models.InlineKeyboardButton{}, rows...), backHomeRow(a.lang(chatID)))
	a.msg.SendPhoto(ctx, chatID, fileID, a.applyPremium(caption), withClose)
}

// backHomeRow — ряд с одной кнопкой «◀️ На главную» (callback x:home).
// Используется notify*-методами: клик удаляет это уведомление И возвращает
// пользователя в главное меню (привычная навигация, а не просто «×»).
// Если хочется только закрыть без перехода — есть x:close (наследие).
func backHomeRow(lang string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back_home"), "x:home")}
}

func btn(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data}
}

// askInput — отправляет вопрос админу и добавляет кнопку «◀️ Отмена»
// (callback inp:cancel). back — куда вернуться при отмене (например,
// "menu:pay" / "prc:base"). Пусто = вернуться на главную.
func (a *App) askInput(ctx context.Context, chatID int64, text, back string) {
	a.getUI(chatID).inputBack = back
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.cancel"), "inp:cancel")},
	})
}

// cancelInput — обработчик «◀️ Отмена» на ask-форме. Сбрасывает все ожидания
// ввода и эмулирует переход на запомненный родительский callback.
func (a *App) cancelInput(ctx context.Context, chatID int64, isAdmin bool, fname, uname string) {
	ui := a.getUI(chatID)
	back := ui.inputBack
	ui.adminInput = ""
	ui.priceMonths = 0
	ui.inputBack = ""
	if back == "" {
		a.enterHome(ctx, chatID, isAdmin, fname, uname)
		return
	}
	key, val, _ := strings.Cut(back, ":")
	switch key {
	case "menu":
		a.onMenu(ctx, chatID, val, isAdmin, fname, uname)
	case "prc":
		a.onPricing(ctx, chatID, val)
	case "yk":
		a.onYKAdmin(ctx, chatID, val)
	case "star":
		a.onStars(ctx, chatID, val)
	case "adm":
		a.onAdmin(ctx, chatID, val, 0)
	case "ctc":
		a.onContacts(ctx, chatID, val)
	case "subd":
		a.onSubdomain(ctx, chatID, val)
	case "trial":
		a.onTrialAdmin(ctx, chatID, val)
	case "wh":
		a.onWebhooksAdmin(ctx, chatID, val)
	case "cb":
		a.onCBAdmin(ctx, chatID, val)
	case "cbc":
		a.onCBCheck(ctx, chatID, val)
	default:
		a.enterHome(ctx, chatID, isAdmin, fname, uname)
	}
}

// enterHome показывает домашний экран: админу — меню, новому юзеру — регистрацию.
func (a *App) enterHome(ctx context.Context, chatID int64, isAdmin bool, firstName, username string) {
	name := displayName(firstName, username)
	if isAdmin {
		a.showMenu(ctx, chatID, true, name)
		return
	}
	if a.store != nil {
		if u, _ := a.store.GetUser(ctx, chatID); u == nil {
			a.showRegister(ctx, chatID, name)
			return
		}
	}
	a.showMenu(ctx, chatID, false, name)
}

// isHomeText распознаёт нажатие постоянной reply-кнопки «Главная» (RU/EN).
func isHomeText(text string) bool {
	t := strings.TrimSpace(text)
	return t == i18n.T(model.LangRU, "btn.home") || t == i18n.T(model.LangEN, "btn.home")
}

// ensureHomeKey один раз за сессию ставит постоянную reply-кнопку «Главная».
func (a *App) ensureHomeKey(ctx context.Context, chatID int64) {
	a.scrMu.Lock()
	if a.kbSet == nil {
		a.kbSet = map[int64]bool{}
	}
	already := a.kbSet[chatID]
	a.kbSet[chatID] = true
	a.scrMu.Unlock()
	if already {
		return
	}
	a.msg.SetCommandKeyboard(ctx, chatID, i18n.T(a.lang(chatID), "btn.home"))
}

// pricing возвращает единый прайс (карты инициализированы).
func (a *App) pricing() model.Pricing {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.Pricing{}
	}
	a.botCfg.NormalizePricing() // идемпотентно: инициализирует карты и переносит legacy-цены
	return a.botCfg.Pricing
}

func (a *App) lang(chatID int64) string {
	if w, ok := a.wiz[chatID]; ok && w.cfg.Language != "" {
		return w.cfg.Language
	}
	if a.botCfg != nil && a.botCfg.Language != "" {
		return a.botCfg.Language
	}
	return i18n.Fallback
}

// premiumMap — карта "эмодзи -> custom_emoji_id": env PREMIUM_EMOJI + заданная
// через /emoji (вторая перекрывает первую). nil, если пусто.
func (a *App) premiumMap() map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]string{}
	for k, v := range a.cfg.PremiumEmoji {
		out[k] = v
	}
	if a.botCfg != nil {
		for k, v := range a.botCfg.PremiumEmoji {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *App) applyPremium(text string) string {
	return applyPremiumEmoji(text, a.premiumMap())
}

// botMessenger — реальная отправка через Telegram (ParseMode=HTML).
type botMessenger struct {
	b   *bot.Bot
	log *slog.Logger
}

func (m botMessenger) Send(ctx context.Context, chatID int64, text string) int {
	msg, err := m.b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		m.log.Error("send message", "err", err)
		return 0
	}
	return msg.ID
}

func (m botMessenger) SendKB(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) int {
	msg, err := m.b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML,
		ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
	if err != nil {
		m.log.Error("send keyboard", "err", err)
		return 0
	}
	return msg.ID
}

func (m botMessenger) SendPhoto(ctx context.Context, chatID int64, fileID, caption string, rows [][]models.InlineKeyboardButton) int {
	p := &bot.SendPhotoParams{
		ChatID:    chatID,
		Photo:     &models.InputFileString{Data: fileID},
		Caption:   caption,
		ParseMode: models.ParseModeHTML,
	}
	if len(rows) > 0 {
		p.ReplyMarkup = models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	msg, err := m.b.SendPhoto(ctx, p)
	if err != nil {
		m.log.Error("send photo", "err", err)
		return 0
	}
	return msg.ID
}

func (m botMessenger) SendBanner(ctx context.Context, chatID int64, photo models.InputFile, caption string, entities []models.MessageEntity, rm models.ReplyMarkup) int {
	p := &bot.SendPhotoParams{ChatID: chatID, Photo: photo, Caption: caption, ReplyMarkup: rm}
	if len(entities) > 0 {
		p.CaptionEntities = entities
	} else {
		p.ParseMode = models.ParseModeHTML
	}
	msg, err := m.b.SendPhoto(ctx, p)
	if err != nil {
		m.log.Error("send banner", "err", err)
		return 0
	}
	return msg.ID
}

func (m botMessenger) Delete(ctx context.Context, chatID int64, msgID int) {
	if msgID == 0 {
		return
	}
	_, _ = m.b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msgID})
}

func (m botMessenger) SendInvoice(ctx context.Context, chatID int64, title, description, payload, currency string, amount int) {
	if _, err := m.b.SendInvoice(ctx, &bot.SendInvoiceParams{
		ChatID:      chatID,
		Title:       title,
		Description: description,
		Payload:     payload,
		Currency:    currency,
		Prices:      []models.LabeledPrice{{Label: title, Amount: amount}},
	}); err != nil {
		m.log.Error("send invoice", "err", err)
	}
}

// SendPhotoCacheable — три источника по приоритету:
//  1. cachedFileID (если есть)
//  2. встроенный в бинарь JPG (embedBytes)
//  3. URL (urlFallback)
//
// Возвращает (msgID, newFileID). newFileID непустой, если Telegram скачал
// фото из embed/URL и вернул нам свежий file_id для кэша.
func (m botMessenger) SendPhotoCacheable(ctx context.Context, chatID int64, cachedFileID string, embedBytes []byte, urlFallback, caption string, rows [][]models.InlineKeyboardButton) (int, string) {
	// build пробует отправить через конкретный источник.
	// source: "id" / "embed" / "url" — для решения, кэшировать ли file_id из ответа.
	build := func(source string) (*models.Message, string, error) {
		var photo models.InputFile
		switch source {
		case "id":
			photo = &models.InputFileString{Data: cachedFileID}
		case "embed":
			photo = &models.InputFileUpload{Filename: "banner.jpg", Data: bytes.NewReader(embedBytes)}
		case "url":
			photo = &models.InputFileString{Data: urlFallback}
		}
		p := &bot.SendPhotoParams{
			ChatID:    chatID,
			Photo:     photo,
			Caption:   caption,
			ParseMode: models.ParseModeHTML,
		}
		if len(rows) > 0 {
			p.ReplyMarkup = models.InlineKeyboardMarkup{InlineKeyboard: rows}
		}
		msg, err := m.b.SendPhoto(ctx, p)
		return msg, source, err
	}

	// Порядок источников.
	var tries []string
	if cachedFileID != "" {
		tries = append(tries, "id")
	}
	if len(embedBytes) > 0 {
		tries = append(tries, "embed")
	}
	if urlFallback != "" {
		tries = append(tries, "url")
	}
	if len(tries) == 0 {
		return 0, ""
	}
	var (
		msg    *models.Message
		source string
		err    error
	)
	for _, src := range tries {
		msg, source, err = build(src)
		if err == nil {
			break
		}
	}
	if err != nil {
		m.log.Error("send photo cacheable", "err", err)
		return 0, ""
	}
	// Если отправили НЕ по cached file_id, Telegram вернул новый file_id — кэшируем.
	var newFileID string
	if source != "id" && len(msg.Photo) > 0 {
		newFileID = msg.Photo[len(msg.Photo)-1].FileID
	}
	return msg.ID, newFileID
}

func (m botMessenger) AnswerPreCheckout(ctx context.Context, id string, ok bool, errMsg string) {
	if _, err := m.b.AnswerPreCheckoutQuery(ctx, &bot.AnswerPreCheckoutQueryParams{
		PreCheckoutQueryID: id, OK: ok, ErrorMessage: errMsg,
	}); err != nil {
		m.log.Error("answer precheckout", "err", err)
	}
}

func (m botMessenger) RemoveKeyboard(ctx context.Context, chatID int64) {
	msg, err := m.b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "🔄",
		ReplyMarkup: models.ReplyKeyboardRemove{RemoveKeyboard: true},
	})
	if err == nil && msg != nil {
		_, _ = m.b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msg.ID})
	}
}

func (m botMessenger) SetCommandKeyboard(ctx context.Context, chatID int64, label string) {
	// Ставим постоянную reply-клавиатуру и удаляем сообщение-носитель —
	// клавиатура остаётся внизу, лишнего сообщения в чате нет.
	msg, err := m.b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "🏠",
		ReplyMarkup: models.ReplyKeyboardMarkup{
			Keyboard:       [][]models.KeyboardButton{{{Text: label}}},
			ResizeKeyboard: true,
			IsPersistent:   true,
		},
	})
	if err == nil && msg != nil {
		_, _ = m.b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msg.ID})
	}
}

func (m botMessenger) AnswerCallback(ctx context.Context, id string) {
	_, _ = m.b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: id})
}

// applyPremiumEmoji оборачивает обычные эмодзи в HTML-тег <tg-emoji> с
// custom_emoji_id, чтобы показать анимированные (premium) версии. Запасной
// (обычный) эмодзи остаётся внутри тега. При пустой карте текст не меняется.
func applyPremiumEmoji(text string, m map[string]string) string {
	if len(m) == 0 {
		return text
	}
	for emoji, id := range m {
		if id == "" {
			continue
		}
		text = strings.ReplaceAll(text, emoji, "<tg-emoji emoji-id=\""+id+"\">"+emoji+"</tg-emoji>")
	}
	return text
}

// Package app связывает воедино конфиг, хранилище, клиент панели и
// Telegram-бота, и реализует мастер первичной установки (FSM).
package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

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
	SendBanner(ctx context.Context, chatID int64, photo models.InputFile, caption string, entities []models.MessageEntity, rm models.ReplyMarkup) int
	Delete(ctx context.Context, chatID int64, msgID int)
	RemoveKeyboard(ctx context.Context, chatID int64)
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
}

func New(cfg *config.Config, crypter *crypto.Crypter, log *slog.Logger) *App {
	return &App{cfg: cfg, crypter: crypter, log: log, ctl: hostctl.New(), wiz: map[int64]*wizard{}, ui: map[int64]*uiState{},
		screen: map[int64][]int{}}
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
	var dbKind, mode string
	if installed {
		dbKind = a.botCfg.DBKind
		mode = a.botCfg.Panel.Mode
	}
	lang := a.lang(chatID)
	a.mu.Unlock()

	if !installed || panel == nil {
		a.send(ctx, chatID, i18n.T(lang, "installed.hint"))
		return
	}
	count, err := panel.SystemStats(ctx)
	if err != nil {
		a.send(ctx, chatID, i18n.T(lang, "status.fail", err.Error()))
		return
	}
	a.send(ctx, chatID, i18n.T(lang, "status.line", count, dbKind, mode))
}

// handleUpdate запускает самообновление образа через одноразовый контейнер.
func (a *App) handleUpdate(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if a.ctl == nil || !a.ctl.Available() {
		a.send(ctx, chatID, i18n.T(lang, "update.not_available"))
		return
	}
	a.send(ctx, chatID, i18n.T(lang, "update.starting"))
	marker := filepath.Join(a.cfg.DataDir, "update.pending")
	_ = os.WriteFile(marker, []byte("1"), 0o600)
	if err := a.ctl.SelfUpdate(ctx); err != nil {
		_ = os.Remove(marker)
		a.send(ctx, chatID, i18n.T(lang, "update.fail", err.Error()))
	}
}

// notifyUpdated при старте сообщает админу, что бот обновлён (если был /update).
func (a *App) notifyUpdated(ctx context.Context) {
	marker := filepath.Join(a.cfg.DataDir, "update.pending")
	if _, err := os.Stat(marker); err == nil {
		_ = os.Remove(marker)
		a.notify(ctx, a.cfg.AdminID, i18n.T(a.botLang(), "update.done"))
	}
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

// notify — постоянное сообщение (не трекается, не удаляет экран).
func (a *App) notify(ctx context.Context, chatID int64, text string) {
	a.msg.Send(ctx, chatID, a.applyPremium(text))
}

func (a *App) notifyKB(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) {
	a.msg.SendKB(ctx, chatID, a.applyPremium(text), rows)
}

func (a *App) notifyPhoto(ctx context.Context, chatID int64, fileID, caption string, rows [][]models.InlineKeyboardButton) {
	a.msg.SendPhoto(ctx, chatID, fileID, a.applyPremium(caption), rows)
}

func btn(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data}
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

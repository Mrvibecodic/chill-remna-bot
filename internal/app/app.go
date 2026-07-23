package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
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
	"remnabot/internal/moynalog"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
)

type messenger interface {
	Send(ctx context.Context, chatID int64, text string) int
	SendKB(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) int
	SendPhoto(ctx context.Context, chatID int64, fileID, caption string, rows [][]models.InlineKeyboardButton) int

	SendPhotoCacheable(ctx context.Context, chatID int64, cachedFileID string, embedBytes []byte, urlFallback, caption string, rows [][]models.InlineKeyboardButton) (msgID int, newFileID string)
	SendBanner(ctx context.Context, chatID int64, photo models.InputFile, caption string, entities []models.MessageEntity, rm models.ReplyMarkup) int
	Delete(ctx context.Context, chatID int64, msgID int)
	RemoveKeyboard(ctx context.Context, chatID int64)

	SetCommandKeyboard(ctx context.Context, chatID int64, label string)
	AnswerCallback(ctx context.Context, id string)
	EditText(ctx context.Context, chatID int64, msgID int, text string, rows [][]models.InlineKeyboardButton) bool
	EditCaption(ctx context.Context, chatID int64, msgID int, caption string, rows [][]models.InlineKeyboardButton) bool

	SendInvoice(ctx context.Context, chatID int64, title, description, payload, currency string, amount int)
	CreateInvoiceLink(ctx context.Context, title, description, payload, currency string, amount int) (string, error)
	AnswerPreCheckout(ctx context.Context, id string, ok bool, errMsg string)

	SendDocument(ctx context.Context, chatID int64, filename string, data []byte, caption string)
}

type App struct {
	cfg     *config.Config
	crypter *crypto.Crypter
	log     *slog.Logger
	b       *bot.Bot

	ctl *hostctl.Controller
	msg messenger

	newStore func(kind, dsn string) (storage.Storage, error)

	mu           sync.Mutex
	store        storage.Storage
	botCfg       *model.BotConfig
	panel        *remnawave.Client
	wiz          map[int64]*wizard
	ui           map[int64]*uiState
	updNoticeMsg map[int64]int

	scrMu         sync.Mutex
	screen        map[int64][]int
	kbSet         map[int64]bool
	editTarget    map[int64]int
	screenSection map[int64]string

	subMu    sync.Mutex
	subCache map[int64]subCacheEntry

	// finalizeLk serializes finalizePurchase per ext_id (striped) so a payment
	// delivered twice concurrently (webhook redelivery vs reconciler vs manual
	// check) can't extend the panel subscription more than once.
	finalizeLk [finalizeLockShards]sync.Mutex

	infraMu    sync.Mutex
	infraCache *infraCacheEntry

	connectMu    sync.Mutex
	connectCache *connectCacheEntry

	flagMu       sync.RWMutex
	flags        map[string][]byte
	flagsStarted bool

	botUserMu sync.Mutex
	botUser   string

	mnMu     sync.Mutex
	mnClient *moynalog.Client
	mnKey    string

	payLogPurgedAt time.Time

	bgCtx context.Context
}

type subCacheEntry struct {
	has      bool
	expireAt time.Time
}

func New(cfg *config.Config, crypter *crypto.Crypter, log *slog.Logger) *App {
	return &App{cfg: cfg, crypter: crypter, log: log, ctl: hostctl.New(), wiz: map[int64]*wizard{}, ui: map[int64]*uiState{},
		screen: map[int64][]int{}, kbSet: map[int64]bool{}, editTarget: map[int64]int{}, screenSection: map[int64]string{}}
}

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
		cfg.NormalizeReferral()
		cfg.NormalizeUpdateCheck()
		cfg.NormalizeAddSub()
		cfg.NormalizeMiniApp()
		cfg.NormalizeCabinet()
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

func (a *App) Run(ctx context.Context) error {
	a.bgCtx = ctx
	b, err := bot.New(a.cfg.BotToken, bot.WithDefaultHandler(a.handle))
	if err != nil {
		return err
	}
	a.b = b
	a.msg = botMessenger{b: b, log: a.log}
	a.notifyUpdated(ctx)
	a.cleanupWebhookApplyMsg(ctx)
	a.cleanupBotPortMsg(ctx)
	if a.MiniEnabled() || a.CabinetEnabled() {
		a.ensureFlagsAsync(ctx)
	}
	a.log.Info("бот запущен")
	b.Start(ctx)
	return nil
}

// bgContext returns the long-lived root context so background goroutines
// (broadcasts, fiscalization) are cancelled on shutdown instead of leaking.
func (a *App) bgContext() context.Context {
	if a.bgCtx != nil {
		return a.bgCtx
	}
	return context.Background()
}

func (a *App) installed() bool {
	return a.botCfg != nil && a.botCfg.Installed
}

func (a *App) handle(ctx context.Context, b *bot.Bot, update *models.Update) {
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("паника в обработчике апдейта", "panic", r, "stack", string(debug.Stack()))
		}
	}()
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
	// A text message/command is not an inline-button action — drop any stale
	// edit target from a previous callback so we never edit the wrong message.
	a.setEditTarget(chatID, 0)
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
	if a.denyAccess(ctx, chatID, isAdmin) {
		return
	}

	if strings.HasPrefix(text, "/") {
		a.msg.Delete(ctx, chatID, m.ID)
	}

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
		if _, payload, ok := strings.Cut(text, " "); ok {
			payload = strings.TrimSpace(payload)
			a.bindReferrer(ctx, chatID, payload)
			if code, isPromo := strings.CutPrefix(payload, "promo_"); isPromo && code != "" {
				if a.store != nil {
					_ = a.store.UpsertUser(ctx, chatID)
				}
				if msg, _ := a.redeemPromo(ctx, chatID, code); msg != "" {
					a.send(ctx, chatID, msg)
				}
			}
		}
		a.enterHome(ctx, chatID, isAdmin, firstName, username)
		return
	case strings.HasPrefix(text, "/status"):
		if isAdmin {
			a.handleStatus(ctx, chatID)
		}
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
	case strings.HasPrefix(text, "/paysupport") || strings.HasPrefix(text, "/support"):
		a.handleSupportCmd(ctx, chatID)
		return
	case strings.HasPrefix(text, "/terms"):
		a.handleTermsCmd(ctx, chatID)
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

	if a.getUI(chatID).awaitPromo {
		a.getUI(chatID).awaitPromo = false
		a.msg.Delete(ctx, chatID, m.ID)
		a.applyPromo(ctx, chatID, text)
		return
	}
	if a.getUI(chatID).awaitTopUp {
		a.msg.Delete(ctx, chatID, m.ID)
		a.setTopUpCustom(ctx, chatID, text)
		return
	}
	if !isAdmin {
		return
	}

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

func (a *App) handleSupportCmd(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if sup := a.supportURL(); sup != "" {
		a.notifyKB(ctx, chatID, i18n.T(lang, "cmd.support"), [][]models.InlineKeyboardButton{
			{{Text: i18n.T(lang, "btn.support"), URL: sup}},
		})
		return
	}
	a.notify(ctx, chatID, i18n.T(lang, "cmd.support_none"))
}

func (a *App) handleTermsCmd(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	text := ""
	if a.botCfg != nil {
		text = a.botCfg.Contact.TermsText
	}
	a.mu.Unlock()
	if text == "" {
		a.notify(ctx, chatID, i18n.T(lang, "cmd.terms_none"))
		return
	}
	a.notify(ctx, chatID, i18n.T(lang, "terms.intro")+"\n\n"+text)
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
		a.sendSysKB(ctx, chatID, i18n.T(lang, "installed.hint"), rows)
		return
	}
	count, err := panel.SystemStats(ctx)
	if err != nil {
		a.sendSysKB(ctx, chatID, i18n.T(lang, "status.fail", err.Error()), rows)
		return
	}
	text := i18n.T(lang, "status.line", count, dbKind, mode, methods)
	if isAdmin {
		text += "\n\n" + i18n.T(lang, "status.res_title") + "\n" + resourceStats()
	}
	a.sendSysKB(ctx, chatID, text, rows)
}

func (a *App) statusNavRows(lang string, isAdmin bool) [][]models.InlineKeyboardButton {
	if isAdmin {
		return [][]models.InlineKeyboardButton{{
			btn(i18n.T(lang, "btn.back"), "menu:system"),
			btn(i18n.T(lang, "btn.home"), "menu:home"),
		}}
	}
	return [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.home"), "menu:home")}}
}

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

func (a *App) setUpdNotice(chatID int64, msgID int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.updNoticeMsg == nil {
		a.updNoticeMsg = map[int64]int{}
	}
	a.updNoticeMsg[chatID] = msgID
}

func (a *App) takeUpdNotice(chatID int64) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := a.updNoticeMsg[chatID]
	delete(a.updNoticeMsg, chatID)
	return id
}

func (a *App) clearUpdNotice(chatID int64, msgID int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.updNoticeMsg[chatID] == msgID {
		delete(a.updNoticeMsg, chatID)
	}
}

func (a *App) handleUpdate(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	// Reuse the message the admin acted on so the whole update flow lives in ONE
	// message: changelog -> starting -> result (edit in place, not delete+resend).
	src := a.takeEditTarget(chatID)
	if notice := a.takeUpdNotice(chatID); notice != 0 {
		if src == 0 {
			src = notice
		} else if notice != src {
			a.msg.Delete(ctx, chatID, notice)
		}
	}
	if a.ctl == nil || !a.ctl.Available() {
		if src != 0 && a.msg.EditText(ctx, chatID, src, a.applyPremium(i18n.T(lang, "update.not_available")), [][]models.InlineKeyboardButton{homeRow(lang)}) {
			return
		}
		a.sendHome(ctx, chatID, i18n.T(lang, "update.not_available"))
		return
	}
	startText := a.applyPremium(i18n.T(lang, "update.starting"))
	startRows := [][]models.InlineKeyboardButton{backHomeRow(lang)}
	startMsgID := src
	if startMsgID == 0 || !a.msg.EditText(ctx, chatID, startMsgID, startText, startRows) {
		// The source message can't be edited in place (e.g. it's a banner/photo
		// "update available" notice — editMessageText fails on media messages).
		// Delete it so it doesn't linger, then post the flow as a fresh message
		// that can later morph into the "updated" result.
		if startMsgID != 0 {
			a.msg.Delete(ctx, chatID, startMsgID)
		}
		startMsgID = a.msg.SendKB(ctx, chatID, startText, startRows)
	}
	marker := filepath.Join(a.cfg.DataDir, "update.pending")
	_ = os.WriteFile(marker, []byte(strconv.FormatInt(chatID, 10)+":"+strconv.Itoa(startMsgID)), 0o600)
	if err := a.ctl.SetImageChannel(channelTag(a.updChannel())); err != nil {
		_ = os.Remove(marker)
		a.updateFailMsg(ctx, chatID, startMsgID, err)
		return
	}
	if err := a.ctl.SelfUpdate(ctx); err != nil {
		_ = os.Remove(marker)
		a.updateFailMsg(ctx, chatID, startMsgID, err)
		return
	}
	time.AfterFunc(90*time.Second, func() {
		bg := context.Background()
		if _, err := os.Stat(marker); err != nil {
			return
		}
		_ = os.Remove(marker)
		txt := a.applyPremium(i18n.T(a.botLang(), "update.no_restart"))
		rows := [][]models.InlineKeyboardButton{homeRow(a.botLang())}
		if startMsgID != 0 && a.msg != nil && a.msg.EditText(bg, chatID, startMsgID, txt, rows) {
			return
		}
		if a.msg != nil && startMsgID != 0 {
			a.msg.Delete(bg, chatID, startMsgID)
		}
		a.sendHome(bg, chatID, i18n.T(a.botLang(), "update.no_restart"))
	})
}

func (a *App) updateFailMsg(ctx context.Context, chatID int64, msgID int, err error) {
	lang := a.lang(chatID)
	text := a.applyPremium(i18n.T(lang, "update.fail", err.Error()))
	rows := [][]models.InlineKeyboardButton{homeRow(lang)}
	if msgID != 0 && a.msg.EditText(ctx, chatID, msgID, text, rows) {
		return
	}
	if msgID != 0 {
		a.msg.Delete(ctx, chatID, msgID)
	}
	a.sendHome(ctx, chatID, i18n.T(lang, "update.fail", err.Error()))
}

func (a *App) notifyUpdated(ctx context.Context) {
	marker := filepath.Join(a.cfg.DataDir, "update.pending")
	data, err := os.ReadFile(marker)
	if err != nil {
		return
	}
	_ = os.Remove(marker)

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
	doneText := a.applyPremium(i18n.T(a.botLang(), "update.done"))
	doneRows := [][]models.InlineKeyboardButton{homeRow(a.botLang())}
	doneID := msgID
	if msgID == 0 || a.msg == nil || !a.msg.EditText(ctx, chatID, msgID, doneText, doneRows) {
		if msgID != 0 && a.msg != nil {
			a.msg.Delete(ctx, chatID, msgID)
		}
		if a.msg != nil {
			doneID = a.msg.SendKB(ctx, chatID, doneText, doneRows)
		}
	}
	if doneID != 0 && a.msg != nil {
		// Register as the current screen so the next navigation (e.g. "Главная")
		// deletes it instead of leaving it stuck.
		a.scrMu.Lock()
		if a.screen == nil {
			a.screen = map[int64][]int{}
		}
		a.screen[chatID] = []int{doneID}
		a.scrMu.Unlock()
		if a.store != nil {
			_ = a.store.SetScreenMsg(ctx, chatID, doneID)
		}
		id := doneID
		time.AfterFunc(60*time.Second, func() { a.msg.Delete(context.Background(), chatID, id) })
	}
}

func (a *App) setEditTarget(chatID int64, msgID int) {
	a.scrMu.Lock()
	defer a.scrMu.Unlock()
	if a.editTarget == nil {
		a.editTarget = map[int64]int{}
	}
	if msgID == 0 {
		delete(a.editTarget, chatID)
		return
	}
	a.editTarget[chatID] = msgID
}

func (a *App) takeEditTarget(chatID int64) int {
	a.scrMu.Lock()
	defer a.scrMu.Unlock()
	id := a.editTarget[chatID]
	delete(a.editTarget, chatID)
	return id
}

func (a *App) setScreenSection(chatID int64, section string) {
	a.scrMu.Lock()
	defer a.scrMu.Unlock()
	if a.screenSection == nil {
		a.screenSection = map[int64]string{}
	}
	a.screenSection[chatID] = section
}

func (a *App) getScreenSection(chatID int64) string {
	a.scrMu.Lock()
	defer a.scrMu.Unlock()
	return a.screenSection[chatID]
}

// tryEditScreen edits the message the user acted on (callback source) in place
// instead of delete+resend. Text screens only; on a photo message or any error
// it returns false and the caller falls back to the normal delete+send flow.
func (a *App) tryEditScreen(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) bool {
	target := a.takeEditTarget(chatID)
	if target == 0 {
		return false
	}
	if !a.msg.EditText(ctx, chatID, target, text, rows) {
		return false
	}
	a.scrMu.Lock()
	old := a.screen[chatID]
	a.screen[chatID] = []int{target}
	a.scrMu.Unlock()
	for _, id := range old {
		if id != target {
			a.msg.Delete(ctx, chatID, id)
		}
	}
	if a.store != nil {
		_ = a.store.SetScreenMsg(ctx, chatID, target)
	}
	return true
}

func (a *App) emit(ctx context.Context, chatID int64, send func() int) {
	a.takeEditTarget(chatID)
	a.setScreenSection(chatID, "")
	a.scrMu.Lock()
	if a.screen == nil {
		a.screen = map[int64][]int{}
	}
	toDelete := a.screen[chatID]
	a.screen[chatID] = nil
	a.scrMu.Unlock()

	// After a restart the in-memory screen map is empty; fall back to the
	// persisted last-screen id so the previous screen can still be removed.
	if len(toDelete) == 0 && a.store != nil {
		if pid, _ := a.store.GetScreenMsg(ctx, chatID); pid != 0 {
			toDelete = []int{pid}
		}
	}
	for _, id := range toDelete {
		a.msg.Delete(ctx, chatID, id)
	}
	id := send()
	if id != 0 {
		a.scrMu.Lock()
		a.screen[chatID] = []int{id}
		a.scrMu.Unlock()
		if a.store != nil {
			_ = a.store.SetScreenMsg(ctx, chatID, id)
		}
	}
}

func (a *App) screenMsgID(chatID int64) int {
	a.scrMu.Lock()
	defer a.scrMu.Unlock()
	if ids := a.screen[chatID]; len(ids) > 0 {
		return ids[len(ids)-1]
	}
	return 0
}

func (a *App) send(ctx context.Context, chatID int64, text string) {
	t := a.applyPremium(text)
	a.emit(ctx, chatID, func() int { return a.msg.Send(ctx, chatID, t) })
}

func (a *App) sendHome(ctx context.Context, chatID int64, text string) {
	a.sendKB(ctx, chatID, text, [][]models.InlineKeyboardButton{homeRow(a.lang(chatID))})
}

func (a *App) sendKB(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) {
	t := a.applyPremium(text)
	a.setScreenSection(chatID, "")
	if a.tryEditScreen(ctx, chatID, t, rows) {
		return
	}
	a.emit(ctx, chatID, func() int { return a.msg.SendKB(ctx, chatID, t, rows) })
}

func (a *App) sendBanner(ctx context.Context, chatID int64, photo models.InputFile, caption string, ents []models.MessageEntity, rm models.ReplyMarkup) {
	a.emit(ctx, chatID, func() int { return a.msg.SendBanner(ctx, chatID, photo, caption, ents, rm) })
}

func (a *App) sendKBSection(ctx context.Context, chatID int64, section, caption string, rows [][]models.InlineKeyboardButton) {
	url := assets.URL(section)
	if url == "" || len([]rune(caption)) > 1000 {
		a.sendKB(ctx, chatID, caption, rows)
		return
	}
	t := a.applyPremium(caption)
	// Same-section re-render (toggle/pagination/refresh): edit the caption in
	// place instead of delete+resend. Different section (navigation) or any
	// failure falls through to the normal resend below.
	if target := a.takeEditTarget(chatID); target != 0 && a.getScreenSection(chatID) == section {
		if a.msg.EditCaption(ctx, chatID, target, t, rows) {
			a.scrMu.Lock()
			old := a.screen[chatID]
			a.screen[chatID] = []int{target}
			a.scrMu.Unlock()
			for _, id := range old {
				if id != target {
					a.msg.Delete(ctx, chatID, id)
				}
			}
			if a.store != nil {
				_ = a.store.SetScreenMsg(ctx, chatID, target)
			}
			a.setScreenSection(chatID, section)
			return
		}
	}
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
	a.setScreenSection(chatID, section)
}

func (a *App) notify(ctx context.Context, chatID int64, text string) {
	a.msg.SendKB(ctx, chatID, a.applyPremium(text), [][]models.InlineKeyboardButton{backHomeRow(a.lang(chatID))})
}

func (a *App) notifyKB(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) {
	withClose := append(append([][]models.InlineKeyboardButton{}, rows...), backHomeRow(a.lang(chatID)))
	a.msg.SendKB(ctx, chatID, a.applyPremium(text), withClose)
}

func (a *App) notifyPhoto(ctx context.Context, chatID int64, fileID, caption string, rows [][]models.InlineKeyboardButton) {
	withClose := append(append([][]models.InlineKeyboardButton{}, rows...), backHomeRow(a.lang(chatID)))
	a.msg.SendPhoto(ctx, chatID, fileID, a.applyPremium(caption), withClose)
}

func backHomeRow(lang string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.home"), "menu:home")}
}

func btn(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data}
}

func (a *App) askInput(ctx context.Context, chatID int64, text, back string) {
	a.getUI(chatID).inputBack = back
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.cancel"), "inp:cancel")},
	})
}

func (a *App) cancelInput(ctx context.Context, chatID int64, isAdmin bool, fname, uname string) {
	ui := a.getUI(chatID)
	back := ui.inputBack
	ui.adminInput = ""
	ui.priceMonths = 0
	ui.linkUID = 0
	ui.inputBack = ""
	ui.awaitPromo = false
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
	case "usr":
		a.onUsers(ctx, chatID, val, 0)
	default:
		a.enterHome(ctx, chatID, isAdmin, fname, uname)
	}
}

func (a *App) enterHome(ctx context.Context, chatID int64, isAdmin bool, firstName, username string) {
	name := displayName(firstName, username)
	if isAdmin {
		a.showMenu(ctx, chatID, true, name)
		return
	}
	if a.store != nil {
		if u, _ := a.store.GetUser(ctx, chatID); u == nil {
			a.ensureHomeKey(ctx, chatID)
			a.registerUser(ctx, chatID, firstName, username)
			return
		}
	}
	a.showMenu(ctx, chatID, false, name)
}

func isHomeText(text string) bool {
	t := strings.TrimSpace(text)
	return t == i18n.T(model.LangRU, "btn.home") || t == i18n.T(model.LangEN, "btn.home")
}

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

func (a *App) pricing() model.Pricing {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.Pricing{}
	}
	a.botCfg.NormalizePricing()
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

type botMessenger struct {
	b   *bot.Bot
	log *slog.Logger
}

func (m botMessenger) Send(ctx context.Context, chatID int64, text string) int {
	return m.SendKB(ctx, chatID, text, nil)
}

// sendWithRetry retries a Telegram call when the API replies 429 Too Many
// Requests, honouring the retry_after hint. Non-429 errors return immediately.
func (m botMessenger) sendWithRetry(ctx context.Context, do func() error) error {
	const maxAttempts = 4
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err = do(); err == nil {
			return nil
		}
		var tmr *bot.TooManyRequestsError
		if !errors.As(err, &tmr) {
			return err
		}
		wait := time.Duration(tmr.RetryAfter) * time.Second
		if wait <= 0 {
			wait = time.Second
		}
		if wait > 60*time.Second {
			wait = 60 * time.Second
		}
		m.log.Warn("telegram 429, backing off", "retry_after_s", tmr.RetryAfter, "attempt", attempt+1)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	return err
}

func (m botMessenger) SendKB(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) int {
	params := &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML}
	if len(rows) > 0 {
		params.ReplyMarkup = models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	var msg *models.Message
	err := m.sendWithRetry(ctx, func() (e error) {
		msg, e = m.b.SendMessage(ctx, params)
		return e
	})
	if err != nil {
		params.ParseMode = ""
		params.Text = stripHTMLTags(text)
		err = m.sendWithRetry(ctx, func() (e error) {
			msg, e = m.b.SendMessage(ctx, params)
			return e
		})
		if err != nil {
			m.log.Error("send message", "err", err)
			return 0
		}
		m.log.Warn("send message: HTML rejected, sent as plain text", "chat_id", chatID)
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

func (m botMessenger) SendDocument(ctx context.Context, chatID int64, filename string, data []byte, caption string) {
	_, err := m.b.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(data)},
		Caption:  caption,
	})
	if err != nil {
		m.log.Error("send document", "err", err)
	}
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

func (m botMessenger) CreateInvoiceLink(ctx context.Context, title, description, payload, currency string, amount int) (string, error) {
	return m.b.CreateInvoiceLink(ctx, &bot.CreateInvoiceLinkParams{
		Title:       title,
		Description: description,
		Payload:     payload,
		Currency:    currency,
		Prices:      []models.LabeledPrice{{Label: title, Amount: amount}},
	})
}

func (m botMessenger) SendPhotoCacheable(ctx context.Context, chatID int64, cachedFileID string, embedBytes []byte, urlFallback, caption string, rows [][]models.InlineKeyboardButton) (int, string) {

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

func (m botMessenger) EditText(ctx context.Context, chatID int64, msgID int, text string, rows [][]models.InlineKeyboardButton) bool {
	params := &bot.EditMessageTextParams{ChatID: chatID, MessageID: msgID, Text: text, ParseMode: models.ParseModeHTML}
	if len(rows) > 0 {
		params.ReplyMarkup = models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	if _, err := m.b.EditMessageText(ctx, params); err == nil {
		return true
	} else if strings.Contains(err.Error(), "not modified") {
		return true
	}
	params.ParseMode = ""
	params.Text = stripHTMLTags(text)
	_, err := m.b.EditMessageText(ctx, params)
	return err == nil
}

func (m botMessenger) EditCaption(ctx context.Context, chatID int64, msgID int, caption string, rows [][]models.InlineKeyboardButton) bool {
	params := &bot.EditMessageCaptionParams{ChatID: chatID, MessageID: msgID, Caption: caption, ParseMode: models.ParseModeHTML}
	if len(rows) > 0 {
		params.ReplyMarkup = models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	if _, err := m.b.EditMessageCaption(ctx, params); err == nil {
		return true
	} else if strings.Contains(err.Error(), "not modified") {
		return true
	}
	return false
}

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

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

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
	"remnabot/internal/rsimport"
	"remnabot/internal/storage"
)

type messenger interface {
	Send(ctx context.Context, chatID int64, text string) int
	// SendErr — как Send, но отдаёт ошибку Telegram. Нужен рассылке: без неё
	// нельзя отличить «человек заблокировал бота» от временной неудачи, и
	// такие адресаты тратят по два обращения на каждую рассылку вечно.
	SendErr(ctx context.Context, chatID int64, text string) (int, error)
	SendKB(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) int
	// SendEnt отправляет текст с телеграмными entities (форматирование 1-в-1,
	// без ParseMode) — для сообщений, набранных админом в клиенте Telegram.
	SendEnt(ctx context.Context, chatID int64, text string, entities []models.MessageEntity, rows [][]models.InlineKeyboardButton) int
	SendPhoto(ctx context.Context, chatID int64, fileID, caption string, rows [][]models.InlineKeyboardButton) int

	SendPhotoCacheable(ctx context.Context, chatID int64, cachedFileID string, embedBytes []byte, caption string, rows [][]models.InlineKeyboardButton) (msgID int, newFileID string)
	// SendBanner возвращает id сообщения и ошибку: по ней видно, отверг ли
	// Telegram саму картинку (тогда настройку баннера снимают) или отправка не
	// доехала по другой причине.
	SendBanner(ctx context.Context, chatID int64, photo models.InputFile, caption string, entities []models.MessageEntity, rm models.ReplyMarkup) (int, error)
	Delete(ctx context.Context, chatID int64, msgID int)
	// DeleteErr — то же, но с ответом Telegram: сообщения старше 48 часов
	// бот удалить не может, и об этом надо сказать.
	DeleteErr(ctx context.Context, chatID int64, msgID int) error
	RemoveKeyboard(ctx context.Context, chatID int64)

	SetCommandKeyboard(ctx context.Context, chatID int64, label string)
	AnswerCallback(ctx context.Context, id string)
	EditText(ctx context.Context, chatID int64, msgID int, text string, rows [][]models.InlineKeyboardButton) bool
	EditCaption(ctx context.Context, chatID int64, msgID int, caption string, rows [][]models.InlineKeyboardButton) bool

	SendInvoice(ctx context.Context, chatID int64, title, description, payload, currency string, amount int)
	CreateInvoiceLink(ctx context.Context, title, description, payload, currency string, amount int) (string, error)
	AnswerPreCheckout(ctx context.Context, id string, ok bool, errMsg string)
	// RefundStars возвращает звёзды плательщику. Бот сам создаёт состояния
	// «деньги приняты, выдачи нет» (неизвестный срок, сумма не совпала,
	// панель не ответила), а вернуть их было нечем.
	RefundStars(ctx context.Context, userID int64, chargeID string) error
	// StarTransactions — история звёздных операций бота. Единственный способ
	// догнать оплату, апдейт о которой не дожил до выдачи: Telegram
	// подтверждает получение апдейта сразу, а не после обработки.
	StarTransactions(ctx context.Context, offset, limit int) ([]models.StarTransaction, error)

	SendDocument(ctx context.Context, chatID int64, filename string, data []byte, caption string)
	// SendFile загружает файл и отдаёт ошибку Telegram: по ней видно, что не
	// так (нет прав, файл велик, таймаут). Загрузка идёт с долгим таймаутом.
	SendFile(ctx context.Context, chatID int64, name string, data []byte, caption string, rows [][]models.InlineKeyboardButton) (int, error)
	// AnswerCallbackText отвечает на нажатие кнопки всплывающей надписью.
	AnswerCallbackText(ctx context.Context, id, text string)
	// SendDocumentKB отправляет файл (по file_id или загрузкой) с подписью и
	// кнопками — так уходят чеки об оплате, которые пришли не картинкой.
	SendDocumentKB(ctx context.Context, chatID int64, doc models.InputFile, caption string, rm models.ReplyMarkup) int
	Download(ctx context.Context, fileID string) ([]byte, error)
	// ProfilePhotoID — file_id последнего фото профиля в размере для
	// аватарки; "" — фото нет.
	ProfilePhotoID(ctx context.Context, userID int64) (string, error)
}

type App struct {
	cfg     *config.Config
	crypter *crypto.Crypter
	log     *slog.Logger
	b       *bot.Bot

	ctl *hostctl.Controller
	msg messenger

	newStore func(kind, dsn string) (storage.Storage, error)

	// cfgSaveMu сериализует сохранение конфига: снимок и запись в базу под одним
	// замком, иначе два сохранения доезжают до базы в обратном порядке и в базе
	// остаётся состояние, снятое раньше. Порядок захвата: cfgSaveMu → mu и
	// никогда наоборот; с plansMu не вкладывается вовсе.
	cfgSaveMu sync.Mutex
	// plansMu сериализует «прочитал тариф → изменил → записал»: строку тарифа
	// пишут и админка, и синхронизация «Базового» от сетки цен, причём пишут
	// целиком. Без замка правка из карточки возвращала бы цену, только что
	// приехавшую из конфига. Порядок захвата: plansMu → mu.
	plansMu sync.Mutex

	mu     sync.Mutex
	store  storage.Storage
	botCfg *model.BotConfig
	// healNotice — на старте зеркало нашло расхождение сетки с тарифом и
	// восстановило её (след отката). Уведомить админа надо, но в момент
	// загрузки конфига мессенджера ещё нет — флаг ждёт запуска бота.
	healNotice bool
	// txtRejected — свои тексты, которые i18n не принял (язык → ключ →
	// причина). Под отдельным замком: заполняется и под a.mu, и без него.
	txtMu       sync.Mutex
	txtRejected map[string]map[string]error
	// basePlanRef — тариф «Базовый», прочитанный при последней синхронизации.
	// Нужен там, где тариф требуется под замком и лезть в базу нельзя (снимок
	// условий сделки). nil до первой синхронизации.
	basePlanRef  *model.Plan
	panel        *remnawave.Client
	wiz          map[int64]*wizard
	ui           map[int64]*uiState
	updNoticeMsg map[int64]int

	// reconSeen — последнее записанное в журнал состояние каждого висящего
	// счёта. Реконсилятор опрашивает шлюзы раз в две минуты по каждому
	// неоплаченному счёту, и запись результата КАЖДОГО прохода на нагруженном
	// боте — главный генератор объёма журнала (сотни тысяч строк в сутки при
	// пустом смысле: статус не менялся). Пишем только изменения.
	reconMu   sync.Mutex
	reconSeen map[string]string

	// thrMu защищает троттлинг журналирования неаутентифицированных вебхуков
	// (thrLast), разовые уведомления админу по счёту Heleket (hlNotified) и
	// ЮMoney (ymNotified), взведённое ожидание OAuth ЮMoney (ymAuthState),
	// паузу между торрент-предупреждениями пользователю (torSeen) и счётчик
	// неудачных попыток открыть тариф по ссылке (planLinkFails).
	thrMu      sync.Mutex
	thrLast    map[string]time.Time
	hlNotified map[string]time.Time
	ymNotified map[string]time.Time
	// ymAuthState/ymAuthUntil — взведённое ожидание OAuth-редиректа ЮMoney
	// (ссылка «Авторизовать» живёт ymAuthTTL).
	ymAuthState   string
	ymAuthUntil   time.Time
	torSeen       map[int64]time.Time
	torUnbSeen    map[int64]time.Time
	torStrikeBusy map[int64]bool
	torStrikeSeen map[int64]time.Time
	torStrikeFail map[int64]time.Time
	planLinkFails map[int64][]time.Time
	// rwSeen — хэши уже обработанных тел вебхуков панели: она переотправляет
	// событие, не дождавшись ответа, и без этого повтор давал второе
	// сообщение человеку.
	// bootAt — когда поднялся этот процесс. Нужен там, где бюджет ожидания
	// отсчитывается от события: после долгого простоя все накопленные записи
	// оказались бы «просроченными» в первую же секунду.
	bootAt time.Time

	rwSeen    map[string]time.Time
	rwSweptAt time.Time
	// rwNoSecretTold — администратору уже сказано, что события панели
	// отклоняются из-за незаданного секрета; rwNoSecretTried — когда была
	// последняя попытка это сказать.
	rwNoSecretTold  bool
	rwNoSecretTried time.Time
	// remindFails — сколько раз подряд не удалось доставить напоминание по
	// ключу «человек:окно». Без потолка заблокировавший бота крутился бы в
	// очереди до самого истечения подписки.
	remindFails map[string]remindFail
	// planLinkGlobalFails — тот же счётчик, но на весь бот. Лимит на человека
	// обходится новым аккаунтом: регистрация бесплатна, а перебор кодов
	// параллелится линейно по числу аккаунтов.
	planLinkGlobalFails []time.Time
	// p2pReqNotified — когда последний раз звали админа по заявке на доступ к
	// переводу. Без этого каждое нажатие давало отдельное сообщение.
	p2pReqNotified map[int64]time.Time

	// bannerFail — сколько отказов подряд пришло на конкретную картинку
	// баннера (ключ — file_id или ссылка). Живёт под a.mu.
	bannerFail map[string]int

	avatars avatarCache

	scrMu  sync.Mutex
	screen map[int64][]int
	// screenDropDelay — через сколько после нового экрана удаляется старый
	// (0 — сразу). Новый экран уходит первым: если сначала удалить старый,
	// чат на миг пустеет и Telegram показывает кнопку «Запустить».
	screenDropDelay time.Duration
	kbSet           map[int64]bool
	editTarget      map[int64]int
	editProt        map[int64]bool
	screenSection   map[int64]string
	// notices — уведомления с одной кнопкой «Главная» (notify): они не экран,
	// и без учёта оставались висеть после ухода с них. noticeHit — уведомление,
	// на кнопку которого нажали: уходит вместе со старым экраном.
	notices   map[int64][]int
	noticeHit map[int64]int

	subMu    sync.Mutex
	subCache map[int64]subCacheEntry

	// hwidRetrying dedupes background HWID delete-all retries by panel uuid, so a
	// user tapping "reset devices" repeatedly can't pile up goroutines.
	hwidMu       sync.Mutex
	hwidRetrying map[string]bool

	// addSubSyncing guards the add-on backfill, so two admins can't walk the
	// whole panel user list at the same time.
	addSubSyncing atomic.Bool

	// bcastRunning/bcastStop — состояние рассылки. Одно на весь бот: рассылка
	// одна, а админов может быть несколько, и остановить её обязан любой.
	// Раньше остановить её было нельзя вообще — только погасив контейнер.
	bcastRunning atomic.Bool
	bcastStop    atomic.Bool

	// finalizeUserLk serializes finalizePurchase per USER (striped): два
	// РАЗНЫХ платежа одного человека (P2P-заявка + вебхук) иначе считали бы
	// зачёт остатка при смене тарифа от одного и того же снимка — и остаток
	// конвертировался бы дважды. Берётся ПОСЛЕ finalizeLk, порядок строгий.
	finalizeUserLk [finalizeLockShards]sync.Mutex

	// checkoutLk сериализует оплату с баланса из мини-аппа по «человек+тариф+
	// срок». Запросы веб-сервера идут параллельно (в чате апдейты по
	// очереди), а ключ сделки там строится из конца срока, прочитанного ДО
	// списания: второй запрос, прочитавший его уже после первой выдачи,
	// получал другой ключ и списывал деньги ещё раз.
	//
	// Отдельный набор замков, а НЕ finalizeUserLk: тот берёт finalizePurchase
	// внутри, и повторный захват здесь означал бы взаимную блокировку.
	checkoutLk [finalizeLockShards]sync.Mutex
	// recentBuy — когда по этому ключу только что прошла покупка. Замок
	// защищает от одновременности, а это — от двойного тапа с паузой:
	// после выдачи конец срока уже другой, и ключ сделки не совпадёт.
	recentBuy map[string]time.Time

	// webMu защищает состояние веб-интерфейса ниже: кэш логотипов, открытые
	// по ссылке тарифы и отметки ручной проверки оплаты.
	webMu sync.Mutex
	// brandCache — скачанные у Telegram логотипы по идентификатору файла.
	brandCache map[string]brandLogoEntry
	// brandFetchMu — одно скачивание логотипа за раз.
	brandFetchMu sync.Mutex
	// linkPlans — тарифы «по ссылке», открытые человеком в мини-аппе или
	// кабинете: покупка такого тарифа разрешена только после открытия.
	linkPlans map[int64]map[string]time.Time
	// payChecks — когда человек последний раз просил перепроверить оплату.
	payChecks map[int64]time.Time

	// p2pRotate — очередь реквизитов перевода. В памяти, а не в конфиге:
	// см. nextP2PCardIdx.
	p2pRotate atomic.Uint64
	// finalizeLk serializes finalizePurchase per ext_id (striped) so a payment
	// delivered twice concurrently (webhook redelivery vs reconciler vs manual
	// check) can't extend the panel subscription more than once.
	finalizeLk [finalizeLockShards]sync.Mutex

	// popular* — кэш «чаще всего выбирают»: подсчёт идёт по всей таблице
	// платежей и на больших базах стоит сотни миллисекунд на каждый показ.
	popularMu     sync.Mutex
	popularAt     time.Time
	popularMonths int
	popularTotal  int

	infraMu    sync.Mutex
	infraCache *infraCacheEntry

	// hwidDef — лимит устройств по умолчанию из панели (hwid_defaults.go).
	hwidDef hwidDefCache

	connectMu    sync.Mutex
	connectCache *connectCacheEntry
	// connectFail — когда в последний раз не вышло достать конфиг приложений,
	// по ключу «хост|ссылка подписки». Без этого каждое нажатие «Подключить»
	// на лежащей странице заново ждало обхода всех путей.
	//
	// Ключ включает ССЫЛКУ, а не только хост: конфиг отдаётся по сессионной
	// куке конкретной подписки, и один человек с отозванной ссылкой не должен
	// на минуту выключать «Подключить» всем остальным.
	connectFail map[string]time.Time
	// panelCfgs — разобранные конфиги страницы подписки из панели.
	panelCfgs *panelCfgCache
	// subpageOffUntil молчит про конфиг приложений в панели до этого момента:
	// на панелях без такого API спрашивать его на каждый заход в «Подключить»
	// незачем. subpageOffOwner — чей это отказ: после смены адреса или токена
	// клиент панели другой, и пауза прежнего на него не действует.
	subpageOffUntil time.Time
	subpageOffOwner *remnawave.Client

	flagMu       sync.RWMutex
	flags        map[string][]byte
	flagsStarted bool

	// epochMu/epochs — поколение пропусков по аккаунтам. Проверяется на каждом
	// запросе к API кабинета и мини-аппа, поэтому держится в памяти: иначе
	// каждая загрузка страницы добавляла бы полдюжины лишних чтений базы.
	// Значение меняется только отсюда же (смена пароля, привязка Telegram,
	// удаление). epochGen растёт при каждой такой смене: чтение из базы,
	// начатое до неё, своё уже устаревшее значение в карту не кладёт.
	epochMu  sync.RWMutex
	epochs   map[int64]int
	epochGen uint64

	// mailWG считает письма, отправляемые в фоне. Нужен остановке (успеть
	// договорить с почтовым сервером) и тестам, которым иначе не за что
	// зацепиться, чтобы дождаться фоновой отправки.
	mailWG sync.WaitGroup

	botUserMu sync.Mutex
	botUser   string

	mnMu     sync.Mutex
	mnClient *moynalog.Client
	mnKey    string

	payLogPurgedAt time.Time

	// rsMu защищает разобранные дампы remnashop: их кладёт фоновая горутина
	// разбора, а читает обработчик кнопки «Импортировать».
	rsMu   sync.Mutex
	rsDump map[int64]*rsimport.Data

	// bkState — настройки бэкапа и метка восстановления (под mu). Хранятся
	// в своей таблице, а не в конфиге: старый образ выбросил бы их при
	// сохранении конфига.
	bkState model.BackupConfig

	// Бэкап базы. bkMu — одна выгрузка или восстановление за раз; bkPend —
	// присланный файл до подтверждения восстановления; bkNextTry — когда
	// повторить бэкап по расписанию после неудачи (в памяти: перезапуск и так
	// даёт новую попытку).
	bkMu     sync.Mutex
	bkPendMu sync.Mutex
	// bkDestMu — проверки нового получателя бэкапов идут по одной.
	bkDestMu  sync.Mutex
	bkPend    map[int64]*bkPending
	bkNextTry time.Time
	// restoring — идёт восстановление базы: бот, вебхуки и фоновые задачи
	// стоят, сохранение конфига запрещено. После удачного восстановления
	// флаг не снимается до перезапуска процесса.
	restoring atomic.Bool
	// moneyActive — сколько денежных задач (trackMoney) сейчас идёт;
	// bgActive — сколько фоновых проходов, вебхуков и запросов API пишут в
	// базу. Восстановление ждёт, пока оба счётчика не обнулятся.
	moneyActive  atomic.Int64
	bgActive     atomic.Int64
	restorePhase atomic.Int32
	restartFn    func()
	restartReq   atomic.Bool
	// newBackupSender подменяет отдельного бота для бэкапов в тестах,
	// quietWaitD — срок ожидания перед восстановлением (0 — quietWait).
	newBackupSender func(token string) (backupSender, error)
	// quietHook — тесты: вызывается между сменой фазы восстановления и
	// повторной проверкой начатых оплат.
	quietHook  func()
	quietWaitD time.Duration
	// uploadLimitD — потолок файла бэкапа в тестах (0 — tgUploadLimit).
	uploadLimitD int
	// Сверка сроков с панелью после восстановления базы.
	guardMu        sync.Mutex
	guardNextTry   time.Time
	guardFailNoted bool
	// reviewLines — оплаты на ручную проверку после восстановления базы.
	reviewMu    sync.Mutex
	reviewItems []reviewItem

	bgCtx context.Context

	// moneyCtx — контекст «денежных» фоновых задач: выдача по звёздам,
	// возврат на баланс. Отдельно от bgCtx НАМЕРЕННО: тот отменяется тем же
	// сигналом, который начинает остановку, и всё недоделанное обрывалось на
	// полпути — деньги списаны, подписки нет. Этот отменяется только в Drain,
	// когда работа доиграла или вышел бюджет.
	moneyCtx    context.Context
	moneyCancel context.CancelFunc
	moneyWG     sync.WaitGroup
	// draining — Drain уже ждёт: новые задачи в очередь ожидания не ставятся.
	draining bool

	// runInline выполняет фоновые задачи синхронно — нужно тестам, чтобы
	// проверять результат сразу после вызова обработчика.
	runInline bool
}

// spawn выполняет длинную работу в фоне: апдейты Telegram обрабатываются одним
// воркером, и синхронный импорт на тысячу пользователей заморозил бы бота.
func (a *App) spawn(f func()) {
	if a.runInline {
		f()
		return
	}
	go f()
}

type subCacheEntry struct {
	has      bool
	expireAt time.Time
}

func New(cfg *config.Config, crypter *crypto.Crypter, log *slog.Logger) *App {
	return &App{cfg: cfg, crypter: crypter, log: log, ctl: hostctl.New(), wiz: map[int64]*wizard{}, ui: map[int64]*uiState{},
		bootAt: time.Now(), screenDropDelay: 300 * time.Millisecond,
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
		cfg.NormalizeTorrent()
		cfg.NormalizeReferral()
		cfg.NormalizeUpdateCheck()
		cfg.NormalizeAddSub()
		cfg.NormalizeMiniApp()
		cfg.NormalizeDevices()
		cfg.NormalizeWallet()
		cfg.NormalizeCabinet()
		cfg.NormalizeMail()
		cfg.NormalizeAccess()
		cfg.NormalizeYooKassa()
		cfg.NormalizeLegal()
		healWebhookConfig(cfg)
		a.botCfg = cfg
		a.applyTexts(cfg)
		a.panel = a.newPanel(cfg.Panel)
		if cfg.Panel.Mode == model.ModeLocal && a.ctl != nil && a.ctl.Available() {
			if err := a.ctl.ConnectPanelNetwork(ctx); err != nil {
				a.log.Warn("подключение к сети панели", "err", err)
			}
		}
		a.log.Info("конфигурация загружена, бот установлен", "db", a.store.Kind())
		// Одиночный сквад P2P — легаси до глобального набора: переносим в
		// набор и чистим поле, дальше живёт только набор (и тариф).
		a.foldLegacyP2PSquad(ctx)
		// Тариф «Базовый»: при первом запуске новой версии текущая сетка цен
		// переезжает в таблицу тарифов; после первой правки цен тариф ведущий и
		// сетка зеркалится из него (см. internal/app/plans_sync.go). Ошибка
		// боту не мешает: продажи идут по конфигу.
		if mirrored, err := a.syncPlansConfig(ctx); err != nil {
			a.log.Warn("тариф «Базовый» не синхронизирован", "err", err)
		} else if mirrored {
			// Сетка отличалась от тарифа. Так бывает после отката: старый образ
			// правит цены прямо в конфиге, а тариф — истина. Восстановили из
			// тарифа — и говорим об этом, молча менять прайс нельзя.
			//
			// Само сообщение уходит из Run: здесь загрузка конфига, мессенджера
			// ещё нет, и прямой notify падал бы на первом же старте после
			// отката — вместе с уведомлением пропадал бы и бот.
			a.log.Warn("сетка цен отличалась от тарифа и восстановлена из него")
			a.mu.Lock()
			a.healNotice = true
			a.mu.Unlock()
		}
		a.loadBackupState(ctx, cfg)
	}
	return nil
}

// foldLegacyP2PSquad переносит одиночный сквад P2P (легаси-поле совсем старых
// версий) в глобальный набор сквадов — ровно туда, откуда его и так читала
// первая ступень всех цепочек фолбэков, — и чистит само поле. Перенос
// откатоустойчив: старый образ тоже продаёт по глобальному набору, когда тот
// непуст. Сами фолбэки по коду остаются — вдруг где-то лежит конфиг, записанный
// старым образом уже после переноса.
//
// Экраны правки этого поля убраны: сквады правятся в тарифе (и в глобальном
// наборе «Сквады»), два места истины для одного и того же — источник расхождений.
func (a *App) foldLegacyP2PSquad(ctx context.Context) {
	a.mu.Lock()
	cfg := a.botCfg
	if cfg == nil || cfg.P2P.SquadUUID == "" {
		a.mu.Unlock()
		return
	}
	if len(cfg.Plan.ActiveInternalSquads) == 0 {
		cfg.Plan.ActiveInternalSquads = []string{cfg.P2P.SquadUUID}
	}
	// При непустом наборе легаси-поле всеми читателями игнорировалось — чистка
	// ничего не меняет в поведении.
	cfg.P2P.SquadUUID = ""
	a.mu.Unlock()
	// Без хвоста синхронизации: syncPlansConfig вызывается следом на старте.
	if err := a.saveConfigOnly(ctx); err != nil {
		a.log.Warn("перенос легаси-сквада P2P не сохранён", "err", err)
	}
}

// newPanel собирает клиента панели из сохранённой конфигурации, наложив на неё
// окружение: X-Api-Key аддона «Caddy with security» задаётся переменной
// CADDY_AUTH_API_TOKEN (docs.rw → install/panel-security). Ключ живёт рядом с
// прокси, а не в панели, поэтому env главнее того, что ввели в мастере, и
// работает при любом типе установки — в том числе там, где мастер про ключ не
// спрашивает (eGames, локальная панель за общим Caddy).
func (a *App) newPanel(pc model.PanelConfig) *remnawave.Client {
	c := remnawave.New(a.panelWithEnv(pc))
	c.SetEmailResolver(a.panelEmailFor)
	return c
}

// panelEmailFor — почта, по которой аккаунт кабинета ищет свою учётку в панели.
// Только подтверждённая: иначе зарегистрировавшийся на чужой адрес получил бы
// чужую подписку. a.mu не берётся: поиск в панели бывает и под ним.
func (a *App) panelEmailFor(ctx context.Context, tgID int64) string {
	st := a.store
	if tgID >= 0 || st == nil {
		return ""
	}
	wu, err := st.GetWebUserByTgID(ctx, tgID)
	if err != nil || wu == nil || wu.VerifiedAt == "" {
		return ""
	}
	return model.NormalizeEmail(wu.Email)
}

// panelWithEnv накладывает CADDY_AUTH_API_TOKEN на конфиг панели.
func (a *App) panelWithEnv(pc model.PanelConfig) model.PanelConfig {
	if a.cfg != nil && a.cfg.CaddyAuthToken != "" {
		pc.APIKey = a.cfg.CaddyAuthToken
	}
	return pc
}

// caddyKeyFromEnv сообщает, что X-Api-Key уже пришёл из окружения — тогда
// мастеру не о чем спрашивать.
func (a *App) caddyKeyFromEnv() bool {
	return a.cfg != nil && a.cfg.CaddyAuthToken != ""
}

// caddyKeyFor возвращает X-Api-Key для запроса не через клиента панели —
// например, за app-config страницы подписки. Аддон «Caddy with security»
// закрывает домен панели целиком, включая /api/sub, поэтому ключ нужен и там.
// Но только для самой панели: адрес должен совпасть с ней и хостом, и схемой —
// чужому домену секрет не отдаём, в открытый http не отправляем.
//
// panelBase вызывающий берёт через panelBaseURL(), а не читает a.botCfg сам:
// конфиг подменяется на лету (мастер/переустановка), и без блокировки это гонка.
func (a *App) caddyKeyFor(rawURL, panelBase string) string {
	if !a.caddyKeyFromEnv() {
		return ""
	}
	host, scheme := urlHostScheme(rawURL)
	pHost, pScheme := urlHostScheme(panelBase)
	if host == "" || host != pHost || scheme != pScheme {
		return ""
	}
	return a.cfg.CaddyAuthToken
}

// panelBaseURL — базовый URL панели из текущего конфига, снятый под замком.
// Вызывать только там, где a.mu не удерживается (мьютекс не рекурсивный).
func (a *App) panelBaseURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return ""
	}
	return a.botCfg.Panel.BaseURL
}

func urlHostScheme(raw string) (host, scheme string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Scheme == "" {
		return "", ""
	}
	return strings.ToLower(u.Host), strings.ToLower(u.Scheme)
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
	a.mu.Lock()
	old := a.store
	a.store = st
	a.mu.Unlock()
	// Старое хранилище закрываем НЕ сразу: его прямо сейчас держат другие
	// горутины (напоминания, сверка, вебхуки), и мгновенный Close отдал бы им
	// «sql: database is closed» посреди работы.
	if old != nil {
		a.closeStoreLater(old)
	}
	return storage.SaveBootstrap(a.cfg.DataDir, &storage.Bootstrap{DBKind: kind, DSN: dsn})
}

// storeCloseDelay — сколько ждать, прежде чем закрыть подменённое хранилище.
// Столько живёт самый долгий обработчик (вебхук платёжки — 15 с) плюс запас.
const storeCloseDelay = 30 * time.Second

func (a *App) closeStoreLater(old storage.Storage) {
	time.AfterFunc(storeCloseDelay, func() { _ = old.Close() })
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
	old := a.storeRef()
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
		a.closeStoreLater(old)
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
	up, err := bot.New(a.cfg.BotToken, bot.WithSkipGetMe(),
		bot.WithHTTPClient(time.Minute, &http.Client{Timeout: fileUploadTimeout}))
	if err != nil {
		up = nil
	}
	// Под замком: веб-сервер уже принимает запросы, и AlertAdmin с вебхуком
	// панели читают msg из своих горутин.
	a.mu.Lock()
	a.msg = botMessenger{b: b, log: a.log, up: up}
	a.mu.Unlock()
	a.sendHealNotice(ctx)
	a.sendTextsNotice(ctx)
	a.prunePlanAccess(ctx)
	a.pruneWebUsers(ctx)
	a.notifyUpdated(ctx)
	a.noticeRestored(ctx)
	a.resumeBroadcast(ctx)
	a.cleanupWebhookApplyMsg(ctx)
	a.cleanupBotPortMsg(ctx)
	if a.MiniEnabled() || a.CabinetEnabled() {
		a.ensureFlagsAsync(ctx)
	}
	a.log.Info("бот запущен")
	b.Start(ctx)
	return nil
}

// moneyContext — контекст для работы, которую нельзя бросить на полпути.
func (a *App) moneyContext() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.moneyCtx == nil {
		a.moneyCtx, a.moneyCancel = context.WithCancel(context.Background())
	}
	return a.moneyCtx
}

// trackMoney запускает «денежную» задачу так, чтобы остановка бота дала ей
// доиграть (см. Drain).
func (a *App) trackMoney(name string, f func(context.Context)) {
	ctx := a.moneyContext()
	// Счётчик нельзя увеличивать одновременно с ожиданием: обработчик может
	// дожить до остановки и запустить задачу ровно тогда, когда Drain уже
	// ждёт, а это ломает WaitGroup вплоть до паники. Начали останавливаться —
	// задачу запускаем, но в очередь ожидания не ставим: ждать её всё равно
	// уже некому.
	a.mu.Lock()
	counted := !a.draining
	if counted {
		a.moneyWG.Add(1)
	}
	a.mu.Unlock()
	a.moneyActive.Add(1)
	go func() {
		// Отложенные вызовы идут в обратном порядке: счётчик гаснет раньше,
		// чем отпускает ожидание, — кто дождался Drain, видит ноль.
		if counted {
			defer a.moneyWG.Done()
		}
		defer a.moneyActive.Add(-1)
		defer func() {
			if r := recover(); r != nil {
				a.log.Error("паника в фоновой задаче", "task", name, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		f(ctx)
	}()
}

// Drain ждёт недоделанную «денежную» работу не дольше d и возвращает, успела
// ли она. По истечении бюджета контекст отменяется: держать процесс дольше
// бессмысленно — docker всё равно убьёт контейнер.
func (a *App) Drain(d time.Duration) bool {
	a.mu.Lock()
	a.draining = true
	a.mu.Unlock()
	done := make(chan struct{})
	go func() {
		a.moneyWG.Wait()
		// Письма — тоже незаконченный разговор с внешним сервером: оборвать
		// его на середине значит оставить человека без ссылки и без ошибки.
		a.mailWG.Wait()
		close(done)
	}()
	ok := false
	select {
	case <-done:
		ok = true
	case <-time.After(d):
	}
	a.mu.Lock()
	cancel := a.moneyCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return ok
}

// WebRequired — нужна ли веб-часть этой установке. Если всё выключено, отказ
// веб-сервера не повод гасить бота: чат продолжает работать.
func (a *App) WebRequired() bool {
	a.mu.Lock()
	webhook := a.botCfg != nil && a.botCfg.Webhook.Enabled
	a.mu.Unlock()
	return webhook || a.MiniEnabled() || a.CabinetEnabled()
}

// AlertAdmin — короткое сообщение админу о поломке уровня процесса. Своим
// контекстом: тот, что отменён сигналом, до Telegram уже не доедет.
func (a *App) AlertAdmin(key string, cause error) {
	a.mu.Lock()
	msg := a.msg
	a.mu.Unlock()
	if msg == nil || a.cfg == nil || a.cfg.AdminID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	msg.Send(ctx, a.cfg.AdminID, i18n.T(a.botLang(), key, detail))
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
	// Библиотека запускает каждый апдейт в своей горутине, поэтому начатый
	// обработчик учитывается так же, как фоновая работа: восстановление базы
	// ждёт, пока он доиграет. Кроме нажатия, которое само запускает
	// восстановление, — иначе оно ждало бы себя.
	if !a.isRestoreApply(update) {
		if !a.enterWork() {
			a.answerMaintenance(ctx, update)
			return
		}
		defer a.leaveWork()
	} else if a.paused() {
		a.answerMaintenance(ctx, update)
		return
	}
	switch {
	case update.CallbackQuery != nil:
		a.handleCallback(ctx, update.CallbackQuery)
	case update.PreCheckoutQuery != nil:
		a.handlePreCheckout(ctx, update.PreCheckoutQuery)
	case update.Message != nil && update.Message.SuccessfulPayment != nil:
		// Финализация ходит в панель (до трёх HTTP-вызовов по 15 секунд), и
		// её нельзя оборвать посреди: уводим в trackMoney — он переживает
		// остановку процесса. Внутри finalizePurchase свои замки и
		// идемпотентность.
		msg := update.Message
		// Через trackMoney: перезапуск ровно в этот момент раньше обрывал
		// выдачу — деньги списаны, подписки нет, и сверка её не подбирала.
		a.trackMoney("финализация оплаты Stars", func(c context.Context) {
			a.handleSuccessfulPayment(c, msg)
		})
	case update.Message != nil && update.Message.RefundedPayment != nil:
		a.handleRefundedPayment(ctx, update.Message)
	case update.Message != nil && update.Message.Text != "":
		a.handleMessage(ctx, update.Message)
	case update.Message != nil && len(update.Message.Photo) > 0:
		a.handlePhoto(ctx, update.Message)
	case update.Message != nil && update.Message.Document != nil:
		a.handleDocument(ctx, update.Message)
	}
}

// isRestoreApply — нажатие «Заменить базу» админом. Кнопку с той же
// строкой может прислать кто угодно, а мимо учёта работы пускаем только её.
func (a *App) isRestoreApply(u *models.Update) bool {
	return u.CallbackQuery != nil && u.CallbackQuery.Data == "bk:apply" &&
		u.CallbackQuery.From.ID == a.cfg.AdminID
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
	// Ссылка-приглашение обрабатывается ДО проверки режима публичности: иначе
	// приглашённый упрётся в «нужно приглашение» и ссылка никогда не сработает.
	if !isAdmin && strings.HasPrefix(text, "/start ") {
		if code, isInv := strings.CutPrefix(strings.TrimSpace(strings.TrimPrefix(text, "/start ")), "inv_"); isInv && code != "" {
			if msg, ok := a.redeemInvite(ctx, chatID, code); msg != "" {
				a.msg.Delete(ctx, chatID, m.ID)
				a.send(ctx, chatID, msg)
				if !ok {
					return
				}
			}
		}
	}
	if a.denyAccess(ctx, chatID, isAdmin) {
		return
	}

	// Новый текст бота может начинаться с «/» (например, подсказка про
	// команду). Пока редактор ждёт текст, это не команда — кроме /start и
	// /setup, которыми из любого состояния выходят в меню.
	if isAdmin && strings.HasPrefix(text, "/") && a.getUI(chatID).txtKey != "" &&
		!strings.HasPrefix(text, "/start") && !strings.HasPrefix(text, "/setup") {
		a.msg.Delete(ctx, chatID, m.ID)
		a.onTextInput(ctx, chatID, m)
		return
	}

	if strings.HasPrefix(text, "/") {
		a.msg.Delete(ctx, chatID, m.ID)
		// Команда уводит с экрана ввода — ожидание секрета доступа к панели
		// снимаем здесь же: экран ввода команда затрёт, а состояние осталось бы
		// взведённым, и следующий обычный текст молча стал бы ключом панели.
		a.clearPanelInput(chatID)
		if isAdmin {
			a.clearTextInput(ctx, chatID)
		}
	}

	if a.installed() && isHomeText(text) {
		a.msg.Delete(ctx, chatID, m.ID)
		if isAdmin {
			a.clearTextInput(ctx, chatID)
		}
		a.enterHome(ctx, chatID, isAdmin, firstName, username)
		return
	}

	switch {
	case strings.HasPrefix(text, "/setup"):
		if !isAdmin {
			a.send(ctx, chatID, i18n.T(i18n.Fallback, "setup.not_admin"))
			return
		}
		// На настроенном боте /setup ведёт в ПЕРЕустановку, а не в первичную
		// установку. Первичная стартует с пустого конфига и в конце пишет его
		// поверх боевого полной заменой: обнулялись бы ключи всех платёжек,
		// документы, контакты, триал и рефералка, а закрытый бот становился бы
		// публичным (AccessMode пустой = публичный). Переустановка делает то
		// же самое, но от копии живого конфига, — она и есть правильный вход.
		if a.installed() {
			a.send(ctx, chatID, i18n.T(a.lang(chatID), "setup.already_installed"))
			a.startReconfigure(ctx, chatID)
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
			a.bindReferrer(ctx, chatID, payload, false)
			if code, isPromo := strings.CutPrefix(payload, "promo_"); isPromo && code != "" {
				if a.store != nil {
					_ = a.store.UpsertUser(ctx, chatID)
				}
				if msg, _ := a.redeemPromo(ctx, chatID, code); msg != "" {
					a.send(ctx, chatID, msg)
				}
			}
			// Прямая ссылка на тариф: вместо главного меню открывается экран
			// тарифа. Режим публичности и вайтлист уже проверены выше — ссылка
			// на тариф не даёт входа в закрытый бот.
			if code, isPlan := strings.CutPrefix(payload, "plan_"); isPlan && code != "" {
				if a.store != nil {
					_ = a.store.UpsertUser(ctx, chatID)
				}
				a.openPlanLink(ctx, chatID, code)
				return
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
	case strings.HasPrefix(text, "/terms"), strings.HasPrefix(text, "/privacy"), strings.HasPrefix(text, "/docs"):
		a.showLegalDocs(ctx, chatID)
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
		// Обычный человек прислал текст, которого никто не ждёт: раньше он
		// исчезал молча. Чаще всего это ответ на просьбу ввести что-то, чьё
		// ожидание не пережило перезапуск бота.
		//
		// Но не в группе: там бот отвечал бы на каждое сообщение чата.
		if m.Chat.Type != models.ChatTypePrivate {
			return
		}
		// Чек об оплате бот ждёт КАРТИНКОЙ или файлом (см. handlePhoto), а
		// человек часто пишет текстом «оплатил, операция 12345». Отвечать ему
		// «я ничего не жду» посреди оплаты — худшее, что можно сделать.
		if a.getUI(chatID).awaitShotReq != 0 {
			a.sendHome(ctx, chatID, i18n.T(a.lang(chatID), "p2p.send_screenshot"))
			return
		}
		a.sendHome(ctx, chatID, i18n.T(a.lang(chatID), "input.not_expected"))
		return
	}

	a.msg.Delete(ctx, chatID, m.ID)
	ui := a.getUI(chatID)
	if ui.welcomeAwait == "txt" {
		a.setWelcomeText(ctx, chatID, m)
		return
	}
	if ui.torAwait {
		a.setTorrentUnblockText(ctx, chatID, m)
		return
	}
	if ui.txtKey != "" {
		a.onTextInput(ctx, chatID, m)
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
	w := a.wiz[chatID]
	// Брошенный мастер переустановки съедал ВЕСЬ ввод в админке до перезапуска
	// процесса: проверка стоит выше обработки, а гасился он только своей
	// кнопкой отмены. Если админ уже нажал «введите значение» в другом
	// разделе — мастер брошен, и текст принадлежит тому разделу.
	//
	// Только для переустановки: при первичной установке админки ещё нет, а
	// сброс мастера оставил бы бота без языка (a.lang читает w.cfg).
	dropped := false
	if w != nil && w.reconfig && ui.adminInput != "" {
		delete(a.wiz, chatID)
		w, dropped = nil, true
	}
	a.mu.Unlock()
	if dropped {
		a.log.Info("мастер переустановки брошен: админ вводит значение в другом разделе", "chat_id", chatID)
	}
	if w != nil {
		a.handleWizardText(ctx, chatID, text)
		return
	}
	if ui.awaitLogo != "" && ui.adminInput == "" {
		// Логотип принимается только картинкой — как и баннер раздела.
		lang := a.lang(chatID)
		a.sendKB(ctx, chatID, i18n.T(lang, "webui.ask_logo"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.cancel"), cbWebUI+":cancel")},
		})
		return
	}
	if ui.awaitSectionBanner != "" && ui.adminInput == "" {
		// Баннер раздела принимается только картинкой: сохранить сюда текст
		// нельзя, а молча съесть сообщение — оставить человека в непонимании.
		// Проверка стоит последней: ожидание живёт до отмены, и вперёд него
		// должны идти и мастер, и ввод любого поля.
		lang := a.lang(chatID)
		a.sendKB(ctx, chatID, i18n.T(lang, "banners.need_photo"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.cancel"), "sec:cancel:"+ui.awaitSectionBanner)},
		})
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

func (a *App) handleStatus(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	installed := a.installed()
	panel := a.panel
	var dbKind, mode, methods string
	if installed {
		dbKind = a.botCfg.DBKind
		mode = a.botCfg.Panel.Mode
		methods = enabledMethods(a.botCfg)
	}
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

// clearUpdNotice снимает уведомление, если по нему ещё не нажали «Обновить»:
// нажатое уже стало сообщением о ходе обновления и удалять его нельзя.
func (a *App) clearUpdNotice(chatID int64, msgID int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.updNoticeMsg[chatID] != msgID {
		return false
	}
	delete(a.updNoticeMsg, chatID)
	return true
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
		a.showUpdateMsg(ctx, chatID, src, i18n.T(lang, "update.not_available"), [][]models.InlineKeyboardButton{homeRow(lang)})
		return
	}
	startMsgID := a.showUpdateMsg(ctx, chatID, src, i18n.T(lang, "update.starting"), [][]models.InlineKeyboardButton{backHomeRow(lang)})
	marker := filepath.Join(a.cfg.DataDir, "update.pending")
	// Третьим полем — версия, с которой уходим. После рестарта по нему видно,
	// обновились мы на самом деле или контейнер просто перезапустился на том
	// же образе. Формат «chat:msg» из прежних версий тоже читается — иначе
	// ровно то обновление, которое привозит эту правку, потеряло бы финальное
	// сообщение.
	_ = os.WriteFile(marker, []byte(strconv.FormatInt(chatID, 10)+":"+strconv.Itoa(startMsgID)+":"+a.cfg.Commit), 0o600)
	// pull теперь синхронный (чтобы причина сбоя дошла до админа), поэтому весь
	// процесс — в горутине: скачивание образа не должно блокировать обработку
	// апдейтов единственным воркером.
	go a.runSelfUpdate(chatID, startMsgID, marker)
}

// showUpdateMsg выводит шаг обновления текущим экраном: правит msgID, а если
// его не отредактировать (баннер, уже удалено), шлёт новое. Прежний экран и
// само msgID из чата уходят — нажать «Обновить» могли и на уведомлении под
// главным меню. Цель правки от последнего нажатия не трогается: часть шагов
// приходит из фоновой горутины, пока админ нажимает что-то ещё.
func (a *App) showUpdateMsg(ctx context.Context, chatID int64, msgID int, text string, rows [][]models.InlineKeyboardButton) int {
	t := a.applyPremium(text)
	if msgID != 0 && a.msg.EditText(ctx, chatID, msgID, t, rows) {
		a.adoptScreen(ctx, chatID, msgID)
		return msgID
	}
	pctx := withProtect(ctx, a.screenProtect(ctx, chatID, copyableText(t)))
	id := a.msg.SendKB(pctx, chatID, t, rows)
	if id == 0 {
		return 0
	}
	a.adoptScreen(ctx, chatID, id, msgID)
	return id
}

func (a *App) runSelfUpdate(chatID int64, startMsgID int, marker string) {
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("паника в самообновлении", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	ctx, cancel := context.WithTimeout(a.bgContext(), 10*time.Minute)
	defer cancel()
	// Сообщение об ошибке уходит со СВЕЖИМ контекстом: если сам pull упёрся в
	// 10-минутный потолок, ctx уже мёртв — и EditText, и запасной sendHome
	// падали бы молча, а админ так и смотрел бы на вечное «запускается».
	fail := func(err error) {
		_ = os.Remove(marker)
		nctx, ncancel := context.WithTimeout(a.bgContext(), time.Minute)
		defer ncancel()
		a.updateFailMsg(nctx, chatID, startMsgID, err)
	}
	// Пин версии не трогаем. В стабильном канале образ часто пришпилен как
	// «:v1» — и сам compose объясняет, что это защита от прыжка на 2.0.0.
	// Прежний код молча превращал его в «:latest», отменяя это обещание с
	// первого же нажатия «Обновить».
	var prev []byte
	if cur, err := a.ctl.BotImageTag(); err != nil || !tagInChannel(cur, a.updChannel()) {
		p, serr := a.ctl.SetImageChannel(channelTag(a.updChannel()))
		if serr != nil {
			fail(serr)
			return
		}
		prev = p
	}
	if err := a.ctl.SelfUpdate(ctx); err != nil {
		// Образ не скачался, а тег уже переписан: следующий любой «up -d»
		// поднял бы контейнер на несуществующем образе. Возвращаем файл.
		if rerr := a.ctl.RestoreCompose(prev); rerr != nil {
			a.log.Error("откат compose после неудачного обновления", "err", rerr)
		}
		fail(err)
		return
	}
	time.AfterFunc(90*time.Second, func() { a.reportNoRestart(chatID, startMsgID, marker) })
}

// reportNoRestart сообщает, что контейнер так и не перезапустился. Если бот
// уже останавливается, маркер остаётся: это и есть перезапуск, и новый
// процесс по маркеру скажет «обновлено».
func (a *App) reportNoRestart(chatID int64, startMsgID int, marker string) {
	if a.bgContext().Err() != nil {
		return
	}
	if _, err := os.Stat(marker); err != nil {
		return
	}
	_ = os.Remove(marker)
	ctx, cancel := context.WithTimeout(a.bgContext(), time.Minute)
	defer cancel()
	lang := a.botLang()
	a.showUpdateMsg(ctx, chatID, startMsgID, i18n.T(lang, "update.no_restart"), [][]models.InlineKeyboardButton{homeRow(lang)})
}

func (a *App) updateFailMsg(ctx context.Context, chatID int64, msgID int, err error) {
	lang := a.lang(chatID)
	a.showUpdateMsg(ctx, chatID, msgID, i18n.T(lang, "update.fail", err.Error()), [][]models.InlineKeyboardButton{homeRow(lang)})
}

func (a *App) notifyUpdated(ctx context.Context) {
	marker := filepath.Join(a.cfg.DataDir, "update.pending")
	data, err := os.ReadFile(marker)
	if err != nil {
		return
	}
	_ = os.Remove(marker)

	parts := strings.SplitN(strings.TrimSpace(string(data)), ":", 3)
	var chatID int64
	var msgID int
	prevCommit := ""
	if len(parts) >= 2 {
		chatID, _ = strconv.ParseInt(parts[0], 10, 64)
		msgID, _ = strconv.Atoi(parts[1])
	}
	if len(parts) == 3 {
		prevCommit = parts[2]
	}
	// Версия не изменилась — значит контейнер пересоздался на том же образе.
	// Говорить «обновление установлено» тут было бы враньём.
	sameVersion := prevCommit != "" && a.cfg.Commit != "" && prevCommit == a.cfg.Commit
	if chatID == 0 {
		chatID = a.cfg.AdminID
	}
	doneKey := "update.done"
	if sameVersion {
		doneKey = "update.same_version"
	}
	if a.msg == nil {
		return
	}
	// «Обновлено» остаётся экраном до следующего перехода: прежний экран уже
	// убран, и удаление по таймеру оставляло бы чат пустым.
	lang := a.botLang()
	a.showUpdateMsg(ctx, chatID, msgID, i18n.T(lang, doneKey), [][]models.InlineKeyboardButton{homeRow(lang)})
}

func (a *App) setEditTarget(chatID int64, msgID int) {
	a.scrMu.Lock()
	defer a.scrMu.Unlock()
	if a.editTarget == nil {
		a.editTarget = map[int64]int{}
	}
	if a.noticeHit == nil {
		a.noticeHit = map[int64]int{}
	}
	delete(a.noticeHit, chatID)
	if i := slices.Index(a.notices[chatID], msgID); msgID != 0 && i >= 0 {
		if n := slices.Delete(a.notices[chatID], i, i+1); len(n) > 0 {
			a.notices[chatID] = n
		} else {
			delete(a.notices, chatID)
		}
		a.noticeHit[chatID] = msgID
	}
	delete(a.editProt, chatID)
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
	a.adoptScreen(ctx, chatID, target)
	return true
}

// adoptScreen делает уже показанное сообщение текущим экраном и убирает
// прежний вместе с also. После перезапуска прежний экран известен только из
// базы. Раздел экрана сбрасывается: вызывающий с баннером ставит свой после.
func (a *App) adoptScreen(ctx context.Context, chatID int64, id int, also ...int) {
	a.setScreenSection(chatID, "")
	a.scrMu.Lock()
	if a.screen == nil {
		a.screen = map[int64][]int{}
	}
	old := a.screen[chatID]
	a.screen[chatID] = []int{id}
	a.scrMu.Unlock()
	if a.store != nil {
		if len(old) == 0 {
			if pid, _ := a.store.GetScreenMsg(ctx, chatID); pid != 0 {
				old = []int{pid}
			}
		}
		_ = a.store.SetScreenMsg(ctx, chatID, id)
	}
	for _, x := range also {
		if x != 0 && !slices.Contains(old, x) {
			old = append(old, x)
		}
	}
	a.dropScreen(chatID, old)
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
	id := send()
	if id == 0 {
		// Новый экран не ушёл — старый остаётся на месте и текущим экраном,
		// чтобы человек не остался с пустым чатом.
		a.scrMu.Lock()
		a.screen[chatID] = append(toDelete, a.screen[chatID]...)
		a.scrMu.Unlock()
		return
	}
	a.scrMu.Lock()
	a.screen[chatID] = []int{id}
	if hit := a.noticeHit[chatID]; hit != 0 {
		delete(a.noticeHit, chatID)
		toDelete = append(toDelete, hit)
	}
	a.scrMu.Unlock()
	if a.store != nil {
		_ = a.store.SetScreenMsg(ctx, chatID, id)
	}
	a.dropScreen(chatID, toDelete)
}

// dropScreen удаляет прошлый экран уже после того, как пришёл новый, с
// небольшой задержкой: старое сообщение к этому времени выше нового и
// исчезает без рывка. Сообщение, которое за это время стало текущим экраном
// (его отредактировали в новый), не трогается.
func (a *App) dropScreen(chatID int64, ids []int) {
	if len(ids) == 0 || a.msg == nil {
		return
	}
	del := func() {
		ctx, cancel := context.WithTimeout(a.bgContext(), 15*time.Second)
		defer cancel()
		a.scrMu.Lock()
		cur := append([]int(nil), a.screen[chatID]...)
		a.scrMu.Unlock()
		for _, id := range ids {
			if id != 0 && !slices.Contains(cur, id) {
				a.msg.Delete(ctx, chatID, id)
			}
		}
	}
	if a.screenDropDelay <= 0 {
		del()
		return
	}
	time.AfterFunc(a.screenDropDelay, del)
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
	pctx := withProtect(ctx, a.screenProtect(ctx, chatID, copyableText(t)))
	a.emit(ctx, chatID, func() int { return a.msg.Send(pctx, chatID, t) })
}

func (a *App) sendHome(ctx context.Context, chatID int64, text string) {
	a.sendKB(ctx, chatID, text, [][]models.InlineKeyboardButton{homeRow(a.lang(chatID))})
}

func (a *App) sendKB(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) {
	t := a.applyPremium(text)
	prot := a.screenProtect(ctx, chatID, copyableText(t))
	a.setScreenSection(chatID, "")
	if a.editMatches(chatID, prot) && a.tryEditScreen(ctx, chatID, t, rows) {
		return
	}
	pctx := withProtect(ctx, prot)
	a.emit(ctx, chatID, func() int { return a.msg.SendKB(pctx, chatID, t, rows) })
}

// sendKBParts отправляет экран, не влезающий в одно сообщение Telegram.
// Части уходят подряд, клавиатура — на последней, и ВСЕ они запоминаются как
// экран: иначе следующий переход стёр бы только последнее сообщение, а хвост
// остался бы висеть в чате навсегда.
func (a *App) sendKBParts(ctx context.Context, chatID int64, parts []string, rows [][]models.InlineKeyboardButton) {
	switch len(parts) {
	case 0:
		return
	case 1:
		a.sendKB(ctx, chatID, parts[0], rows)
		return
	}
	last := len(parts) - 1
	// Первая часть идёт через emit: он и снимает предыдущий экран.
	a.emit(ctx, chatID, func() int { return a.msg.Send(ctx, chatID, a.applyPremium(parts[0])) })
	for i := 1; i <= last; i++ {
		t := a.applyPremium(parts[i])
		var id int
		if i == last {
			id = a.msg.SendKB(ctx, chatID, t, rows)
		} else {
			id = a.msg.Send(ctx, chatID, t)
		}
		if id == 0 {
			continue
		}
		a.scrMu.Lock()
		if a.screen == nil {
			a.screen = map[int64][]int{}
		}
		a.screen[chatID] = append(a.screen[chatID], id)
		a.scrMu.Unlock()
		if i == last && a.store != nil {
			_ = a.store.SetScreenMsg(ctx, chatID, id)
		}
	}
}

// sendBanner отправляет экран с картинкой и НИКОГДА не оставляет человека без
// экрана: Telegram отвергает баннер целиком, если картинка ему не понравилась
// (битый file_id, недоступная ссылка), и главное меню тогда просто не
// приходит. На отказе пробуем встроенный баннер, а негодную настройку снимаем
// — иначе бот остаётся «мёртвым» до вмешательства администратора.
func (a *App) sendBanner(ctx context.Context, chatID int64, photo models.InputFile, caption string, ents []models.MessageEntity, rm models.ReplyMarkup) {
	pctx := withProtect(ctx, a.screenProtect(ctx, chatID, copyableEnts(caption, ents)))
	a.emit(ctx, chatID, func() int {
		id, err := a.msg.SendBanner(pctx, chatID, photo, caption, ents, rm)
		if id != 0 {
			a.noteBannerOK(photo)
			return id
		}
		// Настройку снимаем ТОЛЬКО когда Telegram отверг саму картинку:
		// сетевой сбой или заблокировавший бота пользователь не повод стирать
		// баннер у всех.
		a.dropBrokenWelcomeImage(ctx, photo, photoErrKind(err))
		fallback := &models.InputFileUpload{Filename: "welcome.jpg", Data: bytes.NewReader(defaultBanner)}
		if id, _ := a.msg.SendBanner(pctx, chatID, fallback, caption, ents, rm); id != 0 {
			return id
		}
		// Не принялась и картинка по умолчанию (например, подпись длиннее
		// лимита) — экран уходит текстом: кнопки важнее оформления.
		rows := [][]models.InlineKeyboardButton(nil)
		if kb, ok := rm.(models.InlineKeyboardMarkup); ok {
			rows = kb.InlineKeyboard
		}
		if len(ents) > 0 {
			return a.msg.SendEnt(pctx, chatID, caption, ents, rows)
		}
		return a.msg.SendKB(pctx, chatID, caption, rows)
	})
}

// photoErrKind разбирает отказ Telegram на картинке:
//
//	"id"    — ссылка на файл негодна навсегда (мусор вместо ссылки, битый
//	          file_id): чинить нечего, настройку снимаем сразу;
//	"fetch" — Telegram не смог забрать картинку по ссылке или не осилил её
//	          (сайт оператора мог лежать минуту): снимаем только после
//	          нескольких отказов подряд;
//	""      — отказ не про картинку (сеть, лимиты, бот заблокирован).
func photoErrKind(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"wrong remote file identifier",
		"wrong file identifier",
		"invalid file id",
		"wrong padding in the string",
	} {
		if strings.Contains(msg, marker) {
			return "id"
		}
	}
	for _, marker := range []string{
		"wrong type of the web page content",
		"failed to get http url content",
		"webpage_curl_failed",
		"webpage_media_empty",
		"photo_invalid_dimensions",
		"image_process_failed",
	} {
		if strings.Contains(msg, marker) {
			return "fetch"
		}
	}
	return ""
}

// bannerFailLimit — сколько отказов подряд по одной и той же картинке считаем
// доказательством, что дело не в разовом сбое сайта, откуда её берут.
const bannerFailLimit = 3

// dropBrokenWelcomeImage снимает картинку баннера главной, если отказ пришёл
// именно на неё. Настройка, из-за которой не рисуется меню, не должна пережить
// первую же неудачную отправку — но и разовая недоступность сайта, откуда
// картинка берётся по ссылке, стирать её не должна: такие отказы считаются до
// bannerFailLimit подряд.
func (a *App) dropBrokenWelcomeImage(ctx context.Context, photo models.InputFile, kind string) {
	ref, ok := photo.(*models.InputFileString)
	if !ok || ref.Data == "" || kind == "" {
		return
	}
	a.mu.Lock()
	hit := a.botCfg != nil && (a.botCfg.Welcome.ImageFileID == ref.Data || a.botCfg.Welcome.ImageURL == ref.Data)
	if hit {
		if a.bannerFail == nil {
			a.bannerFail = map[string]int{}
		}
		a.bannerFail[ref.Data]++
		if kind == "fetch" && a.bannerFail[ref.Data] < bannerFailLimit {
			hit = false
		}
	}
	if hit {
		a.botCfg.Welcome.ImageFileID = ""
		a.botCfg.Welcome.ImageURL = ""
		delete(a.bannerFail, ref.Data)
	}
	a.mu.Unlock()
	if !hit {
		return
	}
	a.log.Warn("картинка баннера главной не принята Telegram, настройка снята", "ref", ref.Data, "причина", kind)
	_ = a.saveBotConfig(ctx)
	if a.cfg != nil && a.cfg.AdminID != 0 {
		a.msg.SendKB(ctx, a.cfg.AdminID, i18n.T(a.botLang(), "welcome.image_dropped"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(a.botLang(), "welcome.btn_image"), "wel:img")},
		})
	}
}

// noteBannerOK сбрасывает счётчик отказов: картинка ушла, прошлые сбои были
// разовыми.
func (a *App) noteBannerOK(photo models.InputFile) {
	ref, ok := photo.(*models.InputFileString)
	if !ok || ref.Data == "" {
		return
	}
	a.mu.Lock()
	delete(a.bannerFail, ref.Data)
	a.mu.Unlock()
}

func (a *App) sendKBSection(ctx context.Context, chatID int64, section, caption string, rows [][]models.InlineKeyboardButton) {
	// Предел подписи Telegram (1024) считается в единицах UTF-16 видимого
	// текста: эмодзи — по две. Длинная подпись уходит обычным сообщением.
	if !assets.Has(section) || len([]rune(caption)) > 1000 || len(utf16.Encode([]rune(stripHTMLTags(caption)))) > 1000 {
		a.sendKB(ctx, chatID, caption, rows)
		return
	}
	t := a.applyPremium(caption)
	prot := a.screenProtect(ctx, chatID, copyableText(t))
	match := a.editMatches(chatID, prot)
	// Same-section re-render (toggle/pagination/refresh): edit the caption in
	// place instead of delete+resend. Different section (navigation) or any
	// failure falls through to the normal resend below.
	if target := a.takeEditTarget(chatID); target != 0 && match && a.getScreenSection(chatID) == section {
		if a.msg.EditCaption(ctx, chatID, target, t, rows) {
			a.adoptScreen(ctx, chatID, target)
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
	pctx := withProtect(ctx, prot)
	a.emit(ctx, chatID, func() int {
		id, nf := a.msg.SendPhotoCacheable(pctx, chatID, cached, embed, t, rows)
		newFileID = nf
		return id
	})
	a.keepSectionPhoto(ctx, section, cached, newFileID)
	a.setScreenSection(chatID, section)
}

// keepSectionPhoto запоминает file_id картинки раздела, только что ушедшей из
// сборки. Если в ячейке до этого лежала другая, Telegram её отверг — а это
// могла быть картинка, загруженная администратором: ему об этом говорим.
func (a *App) keepSectionPhoto(ctx context.Context, section, cached, newFileID string) {
	if a.store == nil || newFileID == "" || newFileID == cached {
		return
	}
	if err := a.store.SaveMediaFileID(ctx, section, newFileID); err != nil {
		a.log.Warn("media_cache save", "section", section, "err", err)
	}
	if cached == "" || !assets.IsUserSection(section) || a.cfg == nil || a.cfg.AdminID == 0 {
		return
	}
	a.log.Warn("картинка раздела не принята Telegram, показана встроенная", "section", section)
	lang := a.botLang()
	a.msg.SendKB(ctx, a.cfg.AdminID, i18n.T(lang, "banners.dropped", assets.LabelByKey(section, lang)), [][]models.InlineKeyboardButton{
		{btn(assets.LabelByKey(section, lang), "sec:open:"+section)},
	})
}

// hasChat — есть ли у аккаунта чат в Telegram. У аккаунта кабинета,
// заведённого по почте, id синтетический отрицательный: писать туда некуда,
// Telegram отвечает «chat not found» на каждую попытку.
func hasChat(id int64) bool { return id > 0 }

// Уведомления адресату без чата в Telegram (аккаунт кабинета по почте —
// отрицательный id) не отправляются вовсе: это только лишний запрос и
// ошибка «chat not found» в логе.
func (a *App) notify(ctx context.Context, chatID int64, text string) {
	if !hasChat(chatID) {
		return
	}
	id := a.msg.SendKB(ctx, chatID, a.applyPremium(text), [][]models.InlineKeyboardButton{backHomeRow(a.lang(chatID))})
	if id == 0 {
		return
	}
	a.scrMu.Lock()
	defer a.scrMu.Unlock()
	if a.notices == nil {
		a.notices = map[int64][]int{}
	}
	// Хватает последних: старые уведомления после перезапуска и так забыты.
	n := append(a.notices[chatID], id)
	if len(n) > 20 {
		n = n[len(n)-20:]
	}
	a.notices[chatID] = n
}

// noticeThen показывает короткое пояснение и сразу под ним экран. Пояснение
// уходит из чата вместе с этим экраном при следующем переходе: отдельным
// уведомлением с кнопкой «Главная» оно оставалось висеть навсегда.
func (a *App) noticeThen(ctx context.Context, chatID int64, text string, show func()) {
	if !hasChat(chatID) {
		show()
		return
	}
	// Экран встаёт под пояснением новым сообщением, а не правкой того, что выше.
	a.setEditTarget(chatID, 0)
	id := a.msg.Send(ctx, chatID, a.applyPremium(text))
	show()
	if id == 0 {
		return
	}
	a.scrMu.Lock()
	if a.screen == nil {
		a.screen = map[int64][]int{}
	}
	// В начало: последним в списке должен оставаться сам экран.
	a.screen[chatID] = append([]int{id}, a.screen[chatID]...)
	a.scrMu.Unlock()
}

// notifyKB возвращает id отправленного сообщения; 0 — доставить не удалось.
// Вызывающему это важно там, где факт доставки что-то закрывает (окно
// напоминания).
func (a *App) notifyKB(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton) int {
	if !hasChat(chatID) {
		return 0
	}
	withClose := withHome(rows, a.lang(chatID))
	return a.msg.SendKB(ctx, chatID, a.applyPremium(text), withClose)
}

func (a *App) notifyPhoto(ctx context.Context, chatID int64, fileID, caption string, rows [][]models.InlineKeyboardButton) {
	if !hasChat(chatID) {
		return
	}
	withClose := withHome(rows, a.lang(chatID))
	a.msg.SendPhoto(ctx, chatID, fileID, a.applyPremium(caption), withClose)
}

// notifyDoc — то же, что notifyPhoto, но для чека, пришедшего файлом (PDF или
// картинка «без сжатия»): такие Telegram отдаёт только как документ.
func (a *App) notifyDoc(ctx context.Context, chatID int64, doc models.InputFile, caption string, rows [][]models.InlineKeyboardButton) {
	if !hasChat(chatID) {
		return
	}
	withClose := withHome(rows, a.lang(chatID))
	a.msg.SendDocumentKB(ctx, chatID, doc, a.applyPremium(caption), &models.InlineKeyboardMarkup{InlineKeyboard: withClose})
}

// withHome добавляет к уведомлению кнопку «Главная», если её там ещё нет:
// иначе под сообщением с «Назад | Главная» вставала вторая «Главная».
func withHome(rows [][]models.InlineKeyboardButton, lang string) [][]models.InlineKeyboardButton {
	out := append([][]models.InlineKeyboardButton{}, rows...)
	for _, r := range rows {
		for _, b := range r {
			if b.CallbackData == "menu:home" {
				return out
			}
		}
	}
	return append(out, backHomeRow(lang))
}

func backHomeRow(lang string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.home"), "menu:home")}
}

func btn(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data}
}

func (a *App) askInput(ctx context.Context, chatID int64, text, back string) {
	ui := a.getUI(chatID)
	// torAwait перехватывает текст РАНЬШЕ adminInput (см. handleMessage), и
	// незакрытое ожидание текста съедало бы ответ на этот вопрос.
	ui.torAwait = false
	ui.inputBack = back
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.cancel"), "inp:cancel")},
	})
}

func (a *App) cancelInput(ctx context.Context, chatID int64, isAdmin bool, fname, uname string) {
	ui := a.getUI(chatID)
	back := ui.inputBack
	ui.adminInput = ""
	ui.torAwait = false
	ui.priceMonths = 0
	ui.planCode = ""
	ui.linkUID = 0
	ui.inputBack = ""
	ui.awaitPromo = false
	ui.awaitLogo = ""
	ui.userQuery = ""
	ui.userPage = 0
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
	case cbPlans:
		a.onPlansAdmin(ctx, chatID, val)
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
	case "acc":
		a.onAccess(ctx, chatID, val)
	case cbTorrent:
		a.onTorrentAdmin(ctx, chatID, val)
	case cbTribute:
		if isAdmin {
			a.onTributeAdmin(ctx, chatID, val)
		}
	case cbTexts:
		if isAdmin {
			a.onTexts(ctx, chatID, val)
		}
	case cbBackup:
		if isAdmin {
			a.onBackup(ctx, chatID, val)
		}
	default:
		a.enterHome(ctx, chatID, isAdmin, fname, uname)
	}
}

func (a *App) enterHome(ctx context.Context, chatID int64, isAdmin bool, firstName, username string) {
	name := displayName(a.lang(chatID), firstName, username)
	a.clearPanelInput(chatID)
	if isAdmin {
		// Брошенный файл бэкапа не должен ждать пароль после ухода в меню.
		a.bkPendTake(chatID)
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
	// Согласие на входе: оператор включил показ документов при первом входе, а
	// человек их ещё не принял — меню он увидит после «Принимаю».
	if a.legalStartRequired(ctx, chatID) {
		a.ensureHomeKey(ctx, chatID)
		a.getUI(chatID).pendingLegalHome = true
		a.askLegal(ctx, chatID)
		return
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

// pricing отдаёт КОПИЮ сетки цен: возвращалась она по значению, но карты внутри
// оставались общими с конфигом, и каждый из трёх десятков вызывающих читал их
// уже без замка, пока админка в них писала. Одновременное чтение и запись карты
// роняет процесс мимо перехвата паники (см. model.Pricing.Clone).
func (a *App) pricing() model.Pricing {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.Pricing{}
	}
	a.botCfg.NormalizePricing()
	return a.botCfg.Pricing.Clone()
}

func (a *App) lang(chatID int64) string {
	// Под мьютексом: карта a.wiz пишется из обработчика апдейтов, а lang()
	// зовут и фоновые горутины (вебхуки, автоплатёж, напоминания) — без лока
	// это concurrent map read/write, который роняет процесс мимо recover.
	a.mu.Lock()
	defer a.mu.Unlock()
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
	// up — тот же бот с долгим таймаутом: загрузка файла бэкапа в 50 МБ
	// по медленному каналу не укладывается в минуту основного клиента.
	up *bot.Bot
}

func (m botMessenger) Send(ctx context.Context, chatID int64, text string) int {
	return m.SendKB(ctx, chatID, text, nil)
}

func (m botMessenger) SendErr(ctx context.Context, chatID int64, text string) (int, error) {
	return m.sendKBErr(ctx, chatID, text, nil, true)
}

// htmlRejected — Telegram отказал из-за разметки: сообщение точно не ушло.
func htmlRejected(err error) bool {
	return errors.Is(err, bot.ErrorBadRequest) && strings.Contains(strings.ToLower(err.Error()), "can't parse entities")
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
	id, _ := m.sendKBErr(ctx, chatID, text, rows, false)
	return id
}

// sendKBErr: strict — повторять без разметки только после отказа в разметке.
// Сбой сети или 5xx не значат, что сообщение не дошло, и повтор дал бы дубль.
// Рассылке дубль хуже недоставки; уведомлениям по-прежнему важнее дойти.
func (m botMessenger) sendKBErr(ctx context.Context, chatID int64, text string, rows [][]models.InlineKeyboardButton, strict bool) (int, error) {
	rows = fitKeyboard(rows)
	params := &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML, ProtectContent: protectedCtx(ctx)}
	if len(rows) > 0 {
		params.ReplyMarkup = models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	var msg *models.Message
	err := m.sendWithRetry(ctx, func() (e error) {
		msg, e = m.b.SendMessage(ctx, params)
		return e
	})
	if err != nil {
		// Повтор без разметки нужен ради 400 «can't parse entities». Но если
		// человек заблокировал бота, разметка ни при чём — второй запрос
		// уходил впустую по каждому такому адресату, на каждой рассылке и в
		// каждом уведомлении.
		if errors.Is(err, bot.ErrorForbidden) {
			m.log.Warn("send message: чат недоступен", "chat_id", chatID)
			return 0, err
		}
		if strict && !htmlRejected(err) {
			m.log.Warn("send message", "chat_id", chatID, "err", err)
			return 0, err
		}
		params.ParseMode = ""
		params.Text = stripHTMLTags(text)
		err = m.sendWithRetry(ctx, func() (e error) {
			msg, e = m.b.SendMessage(ctx, params)
			return e
		})
		if err != nil {
			m.log.Error("send message", "err", err)
			return 0, err
		}
		m.log.Warn("send message: HTML rejected, sent as plain text", "chat_id", chatID)
	}
	return msg.ID, nil
}

func (m botMessenger) SendEnt(ctx context.Context, chatID int64, text string, entities []models.MessageEntity, rows [][]models.InlineKeyboardButton) int {
	rows = fitKeyboard(rows)
	params := &bot.SendMessageParams{ChatID: chatID, Text: text, Entities: entities, ProtectContent: protectedCtx(ctx)}
	if len(rows) > 0 {
		params.ReplyMarkup = models.InlineKeyboardMarkup{InlineKeyboard: rows}
	}
	var msg *models.Message
	err := m.sendWithRetry(ctx, func() (e error) {
		msg, e = m.b.SendMessage(ctx, params)
		return e
	})
	if err != nil {
		// Кривые entities (например, после ручной правки конфига) не должны
		// глушить уведомление — повторяем без форматирования.
		params.Entities = nil
		err = m.sendWithRetry(ctx, func() (e error) {
			msg, e = m.b.SendMessage(ctx, params)
			return e
		})
		if err != nil {
			m.log.Error("send message with entities", "err", err)
			return 0
		}
		m.log.Warn("send message: entities rejected, sent as plain text", "chat_id", chatID)
	}
	return msg.ID
}

func (m botMessenger) SendPhoto(ctx context.Context, chatID int64, fileID, caption string, rows [][]models.InlineKeyboardButton) int {
	rows = fitKeyboard(rows)
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

func (m botMessenger) SendBanner(ctx context.Context, chatID int64, photo models.InputFile, caption string, entities []models.MessageEntity, rm models.ReplyMarkup) (int, error) {
	p := &bot.SendPhotoParams{ChatID: chatID, Photo: photo, Caption: caption, ReplyMarkup: fitMarkup(rm), ProtectContent: protectedCtx(ctx)}
	if len(entities) > 0 {
		p.CaptionEntities = entities
	} else {
		p.ParseMode = models.ParseModeHTML
	}
	var msg *models.Message
	err := m.sendWithRetry(ctx, func() error {
		var e error
		msg, e = m.b.SendPhoto(ctx, p)
		return e
	})
	if err != nil {
		m.log.Error("send banner", "err", err)
		return 0, err
	}
	return msg.ID, nil
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

func (m botMessenger) SendDocumentKB(ctx context.Context, chatID int64, doc models.InputFile, caption string, rm models.ReplyMarkup) int {
	msg, err := m.b.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:      chatID,
		Document:    doc,
		Caption:     caption,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: fitMarkup(rm),
	})
	if err != nil {
		m.log.Error("send document kb", "err", err)
		return 0
	}
	return msg.ID
}

// Download скачивает файл, присланный в чат. Telegram отдаёт боту файлы не
// больше 20 МБ, поэтому читаем с запасом и обрываем всё, что больше.
func (m botMessenger) Download(ctx context.Context, fileID string) ([]byte, error) {
	f, err := m.b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.b.FileDownloadLink(f), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram отдал %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDumpBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDumpBytes {
		return nil, errors.New("файл слишком большой")
	}
	return data, nil
}

// ProfilePhotoID берёт самый маленький размер не меньше 160 точек: для кружка
// в меню больше не нужно, а меньше — мыльно на экранах с высокой плотностью.
func (m botMessenger) ProfilePhotoID(ctx context.Context, userID int64) (string, error) {
	p, err := m.b.GetUserProfilePhotos(ctx, &bot.GetUserProfilePhotosParams{UserID: userID, Limit: 1})
	if err != nil {
		return "", err
	}
	if p == nil || len(p.Photos) == 0 || len(p.Photos[0]) == 0 {
		return "", nil
	}
	sizes := p.Photos[0]
	best := sizes[len(sizes)-1]
	for _, s := range sizes {
		if s.Width >= 160 && s.Width < best.Width {
			best = s
		}
	}
	return best.FileID, nil
}

func (m botMessenger) Delete(ctx context.Context, chatID int64, msgID int) {
	if msgID == 0 {
		return
	}
	_, _ = m.b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msgID})
}

func (m botMessenger) DeleteErr(ctx context.Context, chatID int64, msgID int) error {
	if msgID == 0 {
		return errors.New("нет сообщения")
	}
	_, err := m.b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msgID})
	return err
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

func (m botMessenger) SendPhotoCacheable(ctx context.Context, chatID int64, cachedFileID string, embedBytes []byte, caption string, rows [][]models.InlineKeyboardButton) (int, string) {
	rows = fitKeyboard(rows)

	build := func(source string) (*models.Message, string, error) {
		var photo models.InputFile
		switch source {
		case "id":
			photo = &models.InputFileString{Data: cachedFileID}
		case "embed":
			photo = &models.InputFileUpload{Filename: "banner.jpg", Data: bytes.NewReader(embedBytes)}
		}
		p := &bot.SendPhotoParams{
			ChatID:         chatID,
			Photo:          photo,
			Caption:        caption,
			ParseMode:      models.ParseModeHTML,
			ProtectContent: protectedCtx(ctx),
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
	if len(tries) == 0 {
		return 0, ""
	}
	var (
		msg    *models.Message
		source string
		err    error
	)
	var idErr error
	for _, src := range tries {
		msg, source, err = build(src)
		if err == nil {
			break
		}
		if src == "id" {
			idErr = err
		}
	}
	if err != nil {
		m.log.Error("send photo cacheable", "err", err)
		return 0, ""
	}
	if idErr != nil {
		m.log.Warn("сохранённая картинка не ушла, отправлена встроенная", "err", idErr)
	}

	// Сохранённую картинку заменяем, только когда Telegram отверг именно её:
	// на file_id пришёл 400, а тот же экран с картинкой из сборки ушёл. Сбой
	// сети или лимит запросов — не повод: в той же ячейке лежит картинка,
	// загруженная администратором, и она пропала бы без следа.
	var newFileID string
	if source != "id" && len(msg.Photo) > 0 && (idErr == nil || errors.Is(idErr, bot.ErrorBadRequest)) {
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

func (m botMessenger) RefundStars(ctx context.Context, userID int64, chargeID string) error {
	_, err := m.b.RefundStarPayment(ctx, &bot.RefundStarPaymentParams{
		UserID: userID, TelegramPaymentChargeID: chargeID,
	})
	return err
}

func (m botMessenger) StarTransactions(ctx context.Context, offset, limit int) ([]models.StarTransaction, error) {
	res, err := m.b.GetStarTransactions(ctx, &bot.GetStarTransactionsParams{Offset: offset, Limit: limit})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	return res.Transactions, nil
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

func (m botMessenger) AnswerCallbackText(ctx context.Context, id, text string) {
	_, _ = m.b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text})
}

func (m botMessenger) SendFile(ctx context.Context, chatID int64, name string, data []byte, caption string, rows [][]models.InlineKeyboardButton) (int, error) {
	b := m.up
	if b == nil {
		b = m.b
	}
	params := &bot.SendDocumentParams{
		ChatID:    chatID,
		Document:  &models.InputFileUpload{Filename: name, Data: bytes.NewReader(data)},
		Caption:   caption,
		ParseMode: models.ParseModeHTML,
	}
	if len(rows) > 0 {
		params.ReplyMarkup = models.InlineKeyboardMarkup{InlineKeyboard: fitKeyboard(rows)}
	}
	msg, err := b.SendDocument(ctx, params)
	if err != nil {
		m.log.Error("send file", "err", err)
		return 0, err
	}
	return msg.ID, nil
}

func (m botMessenger) EditText(ctx context.Context, chatID int64, msgID int, text string, rows [][]models.InlineKeyboardButton) bool {
	rows = fitKeyboard(rows)
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
	rows = fitKeyboard(rows)
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
	keys := make([]string, 0, len(m))
	for emoji, id := range m {
		if emoji != "" && id != "" {
			keys = append(keys, emoji)
		}
	}
	if len(keys) == 0 {
		return text
	}
	// Длинные первыми: «❤️» не должен разбиться заменой «❤».
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	// Замена идёт только в тексте: не внутри тегов (адрес ссылки) и не
	// внутри <code>, <pre> и уже стоящего <tg-emoji> — там вложенный тег
	// сломал бы разметку, и сообщение ушло бы без оформления.
	var b strings.Builder
	skip := 0
	for i := 0; i < len(text); {
		if text[i] == '<' {
			j := strings.IndexByte(text[i:], '>')
			if j < 0 {
				b.WriteString(text[i:])
				break
			}
			tag := text[i : i+j+1]
			name := strings.ToLower(strings.TrimLeft(tag[1:len(tag)-1], "/"))
			if k := strings.IndexAny(name, " \t\n"); k >= 0 {
				name = name[:k]
			}
			if name == "code" || name == "pre" || name == "tg-emoji" {
				if strings.HasPrefix(tag, "</") {
					if skip > 0 {
						skip--
					}
				} else {
					skip++
				}
			}
			b.WriteString(tag)
			i += j + 1
			continue
		}
		if skip == 0 {
			hit := ""
			for _, k := range keys {
				if strings.HasPrefix(text[i:], k) {
					hit = k
					break
				}
			}
			if hit != "" {
				b.WriteString("<tg-emoji emoji-id=\"" + m[hit] + "\">" + hit + "</tg-emoji>")
				i += len(hit)
				continue
			}
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

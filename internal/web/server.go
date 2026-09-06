package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// ErrUnauthorized — вебхук не прошёл проверку подписи. Обработчик отдаёт на
// него 401, а не 500: платёжки (в частности Tribute) сутки повторяют доставку
// при 5xx, а ретраить заведомо чужой запрос незачем.
var ErrUnauthorized = errors.New("webhook: unauthorized")

type Handlers interface {
	Healthy(ctx context.Context) error

	HandleYooKassaWebhook(ctx context.Context, body []byte) (handled bool, err error)

	HandleCryptoBotWebhook(ctx context.Context, signatureHex string, body []byte) (handled bool, err error)

	HandleRemnawaveWebhook(ctx context.Context, signatureHex string, body []byte) (handled bool, err error)

	HandlePlategaWebhook(ctx context.Context, merchantID, secret string, body []byte) (handled bool, err error)

	HandleHeleketWebhook(ctx context.Context, body []byte) (handled bool, err error)

	HandleTributeWebhook(ctx context.Context, signatureHex string, body []byte) (handled bool, err error)
}

type Server struct {
	log          *slog.Logger
	handlers     Handlers
	srv          *http.Server
	domain       string
	cacheDir     string
	mini         MiniProvider
	authLimiter  *rateLimiter
	moneyLimiter *rateLimiter
	readLimiter  *rateLimiter
	staticDir    string
	// allowPlainHTTP снимает требование HTTPS целиком. Нужен установкам, чей
	// прокси не выставляет X-Forwarded-Proto: до этой правки у них всё
	// работало по открытому HTTP молча, и апдейт не должен их ронять.
	allowPlainHTTP bool
}

// SetMiniApp wires the Mini App data provider. Routes read it live, so it may
// be set after construction (before Run). When nil or disabled, /api/miniapp/*
// and the static app return 404.
func (s *Server) SetMiniApp(p MiniProvider) { s.mini = p }

func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /flags/{code}", s.handleFlag)
	mux.HandleFunc("POST /webhook/yookassa", s.handleYooKassa)
	mux.HandleFunc("POST /webhook/cryptobot", s.handleCryptoBot)
	mux.HandleFunc("POST /webhook/remnawave", s.handleRemnawave)
	mux.HandleFunc("POST /webhook/platega", s.handlePlatega)
	mux.HandleFunc("POST /webhook/heleket", s.handleHeleket)
	mux.HandleFunc("POST /webhook/tribute", s.handleTribute)

	mux.HandleFunc("POST /api/miniapp/auth", s.handleMiniAuth)
	mux.HandleFunc("GET /api/miniapp/me", s.handleMiniMe)
	mux.HandleFunc("GET /api/miniapp/menu", s.handleMiniMenu)
	mux.HandleFunc("POST /api/miniapp/legal/accept", s.handleMiniAcceptLegal)
	mux.HandleFunc("GET /api/miniapp/subscription", s.handleMiniSubscription)
	mux.HandleFunc("GET /api/miniapp/plans", s.handleMiniPlans)
	mux.HandleFunc("POST /api/miniapp/trial", s.handleMiniTrial)
	mux.HandleFunc("POST /api/miniapp/checkout", s.handleMiniCheckout)
	mux.HandleFunc("GET /api/miniapp/referral", s.handleMiniReferral)
	mux.HandleFunc("POST /api/miniapp/promo", s.handleMiniPromo)
	mux.HandleFunc("GET /api/miniapp/topup/options", s.handleMiniTopUpOptions)
	mux.HandleFunc("POST /api/miniapp/topup", s.handleMiniTopUp)
	mux.HandleFunc("GET /api/miniapp/connect", s.handleMiniConnect)
	mux.HandleFunc("POST /api/miniapp/devices/reset", s.handleMiniResetDevices)
	mux.HandleFunc("GET /api/miniapp/autopay", s.handleMiniAutoPay)
	mux.HandleFunc("POST /api/miniapp/autopay", s.handleMiniSetAutoPay)
	mux.HandleFunc("GET /miniapp/", s.handleMiniStatic)
	mux.HandleFunc("GET /api/cabinet/config", s.handleCabinetConfig)
	mux.HandleFunc("POST /api/cabinet/auth/telegram", s.handleCabinetTelegramAuth)
	mux.HandleFunc("POST /api/cabinet/auth/register", s.handleCabinetRegister)
	mux.HandleFunc("POST /api/cabinet/auth/login", s.handleCabinetLogin)
	mux.HandleFunc("POST /api/cabinet/p2p/screenshot", s.handleCabinetP2PScreenshot)
	mux.HandleFunc("GET /", s.handleCabinetStatic)
	return mux
}

func applyTimeouts(srv *http.Server) {
	srv.ReadHeaderTimeout = 5 * time.Second
	// Чек об оплате принимается размером до 12 МБ, а на мобильном интернете
	// это заметно дольше десяти секунд: с прежним значением загрузка чека
	// обрывалась на чтении тела.
	srv.ReadTimeout = 60 * time.Second
	// Внешняя граница, а не внутренняя: денежные обработчики живут до 25
	// секунд, и прежние 15 срабатывали посреди создания счёта — счёт у
	// провайдера уже создан, а ответ покупателю не уходит, и он жмёт
	// «оплатить» второй раз.
	srv.WriteTimeout = 35 * time.Second
	srv.IdleTimeout = 60 * time.Second
}

// shutdownGrace — сколько ждём завершения текущих запросов при остановке.
// Обработка вебхука платёжки занимает до 15 секунд, прежние 5 рвали её на
// середине; больше брать нельзя — docker убивает контейнер через 10 секунд
// после SIGTERM, и остаток нужен фоновым задачам бота.
const shutdownGrace = 3 * time.Second

// newServer — общая часть обоих конструкторов: лимитеры и признак «разрешено
// без HTTPS» (переменная окружения для установок, чей прокси не выставляет
// X-Forwarded-Proto).
func newServer(log *slog.Logger, h Handlers) *Server {
	return &Server{
		log:            log,
		handlers:       h,
		authLimiter:    newRateLimiter(authLimitHits, authLimitWindow),
		moneyLimiter:   newRateLimiter(moneyLimitHits, moneyLimitWindow),
		readLimiter:    newRateLimiter(readLimitHits, readLimitWindow),
		allowPlainHTTP: os.Getenv("ALLOW_PLAIN_HTTP") == "1",
	}
}

func New(addr string, h Handlers, log *slog.Logger) *Server {
	if addr == "" {
		addr = ":8080"
	}
	s := newServer(log, h)
	s.srv = &http.Server{Addr: addr, Handler: s.wrap(s.mux())}
	applyTimeouts(s.srv)
	return s
}

func NewAutocert(domain, cacheDir string, h Handlers, log *slog.Logger) *Server {
	s := newServer(log, h)
	s.domain, s.cacheDir = domain, cacheDir
	s.srv = &http.Server{Addr: ":443", Handler: s.wrap(s.mux())}
	applyTimeouts(s.srv)
	return s
}

func (s *Server) Run(ctx context.Context) error {
	if s.domain != "" {
		return s.runTLS(ctx)
	}
	return s.runPlain(ctx)
}

func (s *Server) runPlain(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("HTTP webhook server starting", "addr", s.srv.Addr)
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		// Ошибку дренажа больше не выбрасываем не глядя: «не успели» значит
		// «бросили запрос на середине», и это ровно то событие, ради которого
		// пишут логи. Кодом выхода она не становится — это не отказ сервера.
		if err := s.srv.Shutdown(shutCtx); err != nil {
			s.log.Warn("веб-сервер: не все запросы успели завершиться", "err", err)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Server) runTLS(ctx context.Context) error {
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(s.domain),
		Cache:      autocert.DirCache(s.cacheDir),
	}
	s.srv.TLSConfig = m.TLSConfig()

	challenge := &http.Server{Addr: ":80", Handler: m.HTTPHandler(nil)}
	applyTimeouts(challenge)

	errCh := make(chan error, 1)
	go func() {
		// Отказ челлендж-сервера раньше не был виден вообще никогда: :80
		// занят — сертификат не выпускается, а рукопожатие падает уже у
		// клиента, без единой строки в логе.
		if err := challenge.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("ACME-челлендж на :80 не запущен — сертификат не выпустится", "err", err)
		}
	}()
	go func() {
		s.log.Info("HTTPS webhook server starting", "domain", s.domain)
		if err := s.srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := s.srv.Shutdown(shutCtx); err != nil {
			s.log.Warn("веб-сервер: не все запросы успели завершиться", "err", err)
		}
		_ = challenge.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

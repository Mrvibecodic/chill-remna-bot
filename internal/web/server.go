package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

type Handlers interface {
	Healthy(ctx context.Context) error

	HandleYooKassaWebhook(ctx context.Context, body []byte) (handled bool, err error)

	HandleCryptoBotWebhook(ctx context.Context, signatureHex string, body []byte) (handled bool, err error)

	HandleRemnawaveWebhook(ctx context.Context, signatureHex string, body []byte) (handled bool, err error)

	HandlePlategaWebhook(ctx context.Context, body []byte) (handled bool, err error)

	HandleTributeWebhook(ctx context.Context, signatureHex string, body []byte) (handled bool, err error)
}

type Server struct {
	log         *slog.Logger
	handlers    Handlers
	srv         *http.Server
	domain      string
	cacheDir    string
	mini        MiniProvider
	authLimiter *rateLimiter
	staticDir   string
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
	mux.HandleFunc("POST /webhook/tribute", s.handleTribute)

	mux.HandleFunc("POST /api/miniapp/auth", s.handleMiniAuth)
	mux.HandleFunc("GET /api/miniapp/me", s.handleMiniMe)
	mux.HandleFunc("GET /api/miniapp/menu", s.handleMiniMenu)
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
	srv.ReadTimeout = 10 * time.Second
	srv.WriteTimeout = 15 * time.Second
	srv.IdleTimeout = 60 * time.Second
}

func New(addr string, h Handlers, log *slog.Logger) *Server {
	if addr == "" {
		addr = ":8080"
	}
	s := &Server{log: log, handlers: h, authLimiter: newRateLimiter(15, 5*time.Minute)}
	s.srv = &http.Server{Addr: addr, Handler: s.mux()}
	applyTimeouts(s.srv)
	return s
}

func NewAutocert(domain, cacheDir string, h Handlers, log *slog.Logger) *Server {
	s := &Server{log: log, handlers: h, domain: domain, cacheDir: cacheDir, authLimiter: newRateLimiter(15, 5*time.Minute)}
	s.srv = &http.Server{Addr: ":443", Handler: s.mux()}
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
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutCtx)
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
	go func() { _ = challenge.ListenAndServe() }()
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
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutCtx)
		_ = challenge.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

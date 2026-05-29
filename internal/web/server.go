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
	log      *slog.Logger
	handlers Handlers
	srv      *http.Server
	domain   string
	cacheDir string
}

func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /webhook/yookassa", s.handleYooKassa)
	mux.HandleFunc("POST /webhook/cryptobot", s.handleCryptoBot)
	mux.HandleFunc("POST /webhook/remnawave", s.handleRemnawave)
	mux.HandleFunc("POST /webhook/platega", s.handlePlatega)
	mux.HandleFunc("POST /webhook/tribute", s.handleTribute)
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
	s := &Server{log: log, handlers: h}
	s.srv = &http.Server{Addr: addr, Handler: s.mux()}
	applyTimeouts(s.srv)
	return s
}

func NewAutocert(domain, cacheDir string, h Handlers, log *slog.Logger) *Server {
	s := &Server{log: log, handlers: h, domain: domain, cacheDir: cacheDir}
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

package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

func readAllLimited(r *http.Request, maxBytes int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBytes)
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.handlers.Healthy(ctx); err != nil {
		s.log.Warn("healthz: not ready", "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// webhook — общий приём вебхука: тело с ограничением размера, срок на
// обработку и разбор исхода. true — обработано, вызывающему осталось ответить
// отправителю тем, чего тот ждёт.
func (s *Server) webhook(w http.ResponseWriter, r *http.Request, name string, call func(ctx context.Context, body []byte) (bool, error)) bool {
	body, err := readAllLimited(r, 256*1024)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	handled, err := call(ctx, body)
	return s.webhookDone(w, name, handled, err)
}

// webhookDone переводит исход обработки в ответ отправителю: подпись не
// прошла — 401, просьба повторить или сбой — 500 (отправитель пришлёт снова).
// true — ошибки нет.
func (s *Server) webhookDone(w http.ResponseWriter, name string, handled bool, err error) bool {
	switch {
	case errors.Is(err, ErrUnauthorized):
		s.log.Warn(name+" webhook", "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	case errors.Is(err, ErrRetryLater):
		s.log.Warn(name+" webhook: retry later", "err", err)
		http.Error(w, "retry later", http.StatusInternalServerError)
		return false
	case err != nil:
		s.log.Error(name+" webhook", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return false
	}
	if !handled {
		s.log.Info(name + " webhook ignored")
	}
	return true
}

func (s *Server) handleYooKassa(w http.ResponseWriter, r *http.Request) {
	if s.webhook(w, r, "yookassa", s.handlers.HandleYooKassaWebhook) {
		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) handleCryptoBot(w http.ResponseWriter, r *http.Request) {
	sig := r.Header.Get("Crypto-Pay-API-Signature")
	if s.webhook(w, r, "cryptobot", func(ctx context.Context, body []byte) (bool, error) {
		return s.handlers.HandleCryptoBotWebhook(ctx, sig, body)
	}) {
		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) handleRemnawave(w http.ResponseWriter, r *http.Request) {
	sig := r.Header.Get("X-Remnawave-Signature")
	if s.webhook(w, r, "remnawave", func(ctx context.Context, body []byte) (bool, error) {
		return s.handlers.HandleRemnawaveWebhook(ctx, sig, body)
	}) {
		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) handlePlatega(w http.ResponseWriter, r *http.Request) {
	merchantID := r.Header.Get("X-MerchantId")
	secret := r.Header.Get("X-Secret")
	if s.webhook(w, r, "platega", func(ctx context.Context, body []byte) (bool, error) {
		return s.handlers.HandlePlategaWebhook(ctx, merchantID, secret, body)
	}) {
		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) handleHeleket(w http.ResponseWriter, r *http.Request) {
	if s.webhook(w, r, "heleket", s.handlers.HandleHeleketWebhook) {
		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) handleTribute(w http.ResponseWriter, r *http.Request) {
	sig := r.Header.Get("trbt-signature")
	if !s.webhook(w, r, "tribute", func(ctx context.Context, body []byte) (bool, error) {
		return s.handlers.HandleTributeWebhook(ctx, sig, body)
	}) {
		return
	}
	// Tribute ждёт в ответ {"status":"ok"}.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

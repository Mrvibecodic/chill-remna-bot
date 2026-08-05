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

func (s *Server) handleYooKassa(w http.ResponseWriter, r *http.Request) {
	body, err := readAllLimited(r, 256*1024)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	handled, err := s.handlers.HandleYooKassaWebhook(ctx, body)
	if err != nil {
		s.log.Error("yookassa webhook", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if !handled {

		s.log.Info("yookassa webhook ignored")
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleCryptoBot(w http.ResponseWriter, r *http.Request) {
	sig := r.Header.Get("Crypto-Pay-API-Signature")
	body, err := readAllLimited(r, 256*1024)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	handled, err := s.handlers.HandleCryptoBotWebhook(ctx, sig, body)
	if err != nil {
		s.log.Error("cryptobot webhook", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if !handled {
		s.log.Info("cryptobot webhook ignored")
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleRemnawave(w http.ResponseWriter, r *http.Request) {
	sig := r.Header.Get("X-Remnawave-Signature")
	body, err := readAllLimited(r, 256*1024)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	handled, err := s.handlers.HandleRemnawaveWebhook(ctx, sig, body)
	if err != nil {
		s.log.Error("remnawave webhook", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if !handled {
		s.log.Info("remnawave webhook ignored")
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePlatega(w http.ResponseWriter, r *http.Request) {
	body, err := readAllLimited(r, 256*1024)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	handled, err := s.handlers.HandlePlategaWebhook(ctx, body)
	if err != nil {
		s.log.Error("platega webhook", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if !handled {
		s.log.Info("platega webhook ignored")
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleHeleket(w http.ResponseWriter, r *http.Request) {
	body, err := readAllLimited(r, 256*1024)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	handled, err := s.handlers.HandleHeleketWebhook(ctx, body)
	if err != nil {
		s.log.Error("heleket webhook", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if !handled {
		s.log.Info("heleket webhook ignored")
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleTribute(w http.ResponseWriter, r *http.Request) {
	sig := r.Header.Get("trbt-signature")
	body, err := readAllLimited(r, 256*1024)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	handled, err := s.handlers.HandleTributeWebhook(ctx, sig, body)
	if errors.Is(err, ErrUnauthorized) {
		s.log.Warn("tribute webhook", "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		s.log.Error("tribute webhook", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if !handled {
		s.log.Info("tribute webhook ignored")
	}
	w.WriteHeader(http.StatusOK)
}

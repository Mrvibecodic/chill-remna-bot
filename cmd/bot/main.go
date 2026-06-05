package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"remnabot/internal/app"
	"remnabot/internal/config"
	"remnabot/internal/crypto"
	"remnabot/internal/web"

	_ "remnabot/internal/storage/drivers"
)

var (
	commit    = "dev"
	buildDate = ""
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("конфигурация", "err", err)
		os.Exit(1)
	}
	cfg.Commit = commit
	cfg.BuildDate = buildDate

	crypter, err := crypto.LoadOrCreate(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		log.Error("ключ шифрования", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a := app.New(cfg, crypter, log)
	if err := a.Bootstrap(ctx); err != nil {
		log.Error("инициализация", "err", err)
		os.Exit(1)
	}

	addr, domain, cacheDir := a.WebhookServer()
	var webSrv *web.Server
	if domain != "" {
		webSrv = web.NewAutocert(domain, cacheDir, a, log)
	} else {
		webSrv = web.New(addr, a, log)
	}

	var wg sync.WaitGroup
	wg.Add(5)
	var botErr, webErr error

	go func() {
		defer wg.Done()
		defer stop()
		botErr = a.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		webErr = webSrv.Run(ctx)
	}()

	go func() {
		defer wg.Done()
		a.RunReconciler(ctx)
	}()

	go func() {
		defer wg.Done()
		a.RunReminders(ctx)
	}()

	go func() {
		defer wg.Done()
		a.RunUpdateChecker(ctx)
	}()
	wg.Wait()

	if botErr != nil {
		log.Error("работа бота", "err", botErr)
		os.Exit(1)
	}
	if webErr != nil {
		log.Error("работа web-сервера", "err", webErr)
		os.Exit(1)
	}
	log.Info("остановлен")
}

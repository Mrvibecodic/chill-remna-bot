package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

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

// shutdownGrace — сколько ждём недоделанную фоновую работу после сигнала
// остановки. Docker по умолчанию убивает контейнер через 10 секунд, поэтому
// больше обещать нечестно: остаток уйдёт на дренаж веб-сервера.
const shutdownGrace = 6 * time.Second

// Без этого веб-сервер молча перестал бы отвечать 503 во время
// восстановления базы, если сигнатура разойдётся.
var _ web.WorkTracker = (*app.App)(nil)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("конфигурация", "err", err)
		os.Exit(1)
	}
	cfg.Commit = commit
	cfg.BuildDate = buildDate
	// Подробность лога — из окружения. По умолчанию прежняя (info).
	log = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.SlogLevel()}))

	crypter, err := crypto.LoadOrCreate(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		log.Error("ключ шифрования", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a := app.New(cfg, crypter, log)
	// После восстановления базы бот гасит себя сам, а docker поднимает заново.
	a.SetRestart(stop)
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
	webSrv.SetMiniApp(a)
	webSrv.SetStaticDir(cfg.StaticDir)

	var wg sync.WaitGroup
	wg.Add(11)
	var botErr, webErr error

	go func() {
		defer wg.Done()
		defer stop()
		botErr = a.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		webErr = webSrv.Run(ctx)
		if webErr == nil || ctx.Err() != nil {
			return
		}
		// Веб-сервер умирал молча: в Telegram бот бодро отвечал, а вебхуки
		// платёжек, мини-апп и кабинет были мертвы, и текст ошибки печатался
		// только при выключении.
		log.Error("веб-сервер остановлен", "err", webErr)
		a.AlertAdmin("web.down", webErr)
		if a.WebRequired() {
			// Что-то из веб-части включено — работать «наполовину» нельзя:
			// платежи не доедут. Гасимся, дальше поднимет docker.
			stop()
		}
	}()

	go func() {
		defer wg.Done()
		a.RunReconciler(ctx)
	}()

	go func() {
		defer wg.Done()
		a.RunSubRepair(ctx)
	}()

	go func() {
		defer wg.Done()
		a.RunAutoPay(ctx)
	}()
	go func() {
		defer wg.Done()
		a.RunReminders(ctx)
	}()

	go func() {
		defer wg.Done()
		a.RunUpdateChecker(ctx)
	}()
	go func() {
		defer wg.Done()
		a.RunTorrentUnblocker(ctx)
	}()
	go func() {
		defer wg.Done()
		a.RunTrialReset(ctx)
	}()
	go func() {
		defer wg.Done()
		a.RunBonusTrafficSweep(ctx)
	}()
	go func() {
		defer wg.Done()
		a.RunBackups(ctx)
	}()
	wg.Wait()
	// Даём доиграть недоделанному: выдаче по звёздам, возврату на баланс.
	// Бюджет короткий намеренно — docker убивает контейнер через 10 секунд
	// после SIGTERM, и обещать больше нечестно.
	if !a.Drain(shutdownGrace) {
		log.Warn("не всё фоновое успело завершиться за отведённое время")
	}

	if a.RestartRequested() {
		// Ненулевой код: docker поднимает контейнер заново и при политике
		// on-failure, а не только always/unless-stopped.
		log.Info("перезапуск после восстановления базы")
		os.Exit(3)
	}
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

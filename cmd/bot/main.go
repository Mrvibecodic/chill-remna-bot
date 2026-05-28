// Команда bot — точка входа Telegram-бота Remnawave.
//
// Окружение (bootstrap-минимум, остальное настраивается мастером в Telegram):
//
//	BOT_TOKEN          — токен бота (обязательно)
//	ADMIN_TELEGRAM_ID  — Telegram ID администратора (обязательно)
//	DATA_DIR           — каталог данных (по умолчанию /data)
//	DB_KIND            — необязательно: sqlite|postgres (иначе спросит мастер)
//	DATABASE_URL       — DSN PostgreSQL (если DB_KIND=postgres)
//	SECRET_KEY         — ключ шифрования секретов (иначе сгенерируется в DATA_DIR)
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

	_ "remnabot/internal/storage/drivers" // регистрация драйверов БД (sqlite, pgx)
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("конфигурация", "err", err)
		os.Exit(1)
	}

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

	// Параллельно с long-polling'ом поднимаем HTTP-сервер для входящих
	// вебхуков (YooKassa, CryptoBot, Remnawave) и /healthz. Адрес читаем
	// из BotConfig.Webhook — если выключено, всё равно стартуем на :8080,
	// чтобы /healthz отдавал статус (нужно для docker healthcheck).
	addr, _, _ := a.WebhookConfig()
	webSrv := web.New(addr, a, log)

	var wg sync.WaitGroup
	wg.Add(3)
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
	// Фоновый реконсилятор: добивает «оплачено-но-не-выдано» (пропущенные вебхуки).
	go func() {
		defer wg.Done()
		a.RunReconciler(ctx)
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

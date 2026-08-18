---
title: Установка за 4 шага
description: "Установка бота на сервер через Docker Compose: файл, два значения, запуск, первый вход."
---

Образ собирает CI и публикует в GitHub Container Registry (GHCR): `ghcr.io/mrvibecodic/chill-remna-bot`. В `docker-compose.yml` по умолчанию стоит тег `:v1` — приходят все релизы `1.x.y` (см. [«Каналы релизов»](/ops/channels/)). На сервере вы только берёте `docker-compose.yml`, вписываете два значения и запускаете — **никакой сборки на сервере**.

## Шаг 1. Создайте папку и возьмите docker-compose.yml

```bash
mkdir -p /opt/remnachillbot && cd /opt/remnachillbot
curl -fsSL -o docker-compose.yml \
  https://raw.githubusercontent.com/Mrvibecodic/chill-remna-bot/main/docker-compose.yml
```

## Шаг 2. Впишите два значения

Откройте `docker-compose.yml` в редакторе:

```bash
nano docker-compose.yml
```

Найдите и замените две строки (остальное не трогайте):

```yaml
      BOT_TOKEN: "123456789:AAEabc..."     # токен от @BotFather
      ADMIN_TELEGRAM_ID: "000000000"        # ваш Telegram ID (число)
```

Сохраните: в `nano` это `Ctrl+O`, `Enter`, затем выход `Ctrl+X`.

> Если ставите **не** в `/opt/remnachillbot` — поправьте в этом же файле путь `/opt/remnachillbot:/compose` и переменную `COMPOSE_HOST_DIR` на свой каталог. Если контейнер панели называется не `remnawave` — задайте `PANEL_CONTAINER` с правильным именем.

## Шаг 3. Скачайте образ и запустите

```bash
docker compose pull
docker compose up -d
```

Образ скачивается готовым из GHCR (его собрал CI) — это быстрее сборки и не грузит сервер. Если `pull` отвечает `denied` — у владельца репозитория пакет GHCR не переведён в Public (GitHub → Packages → chill-remna-bot → Package settings → Change visibility). Посмотреть логи:

```bash
docker compose logs -f
```

(выход из просмотра логов — `Ctrl+C`, бот при этом продолжает работать).

## Шаг 4. Откройте бота и пройдите мастер

Напишите вашему боту в Telegram команду **`/start`** — запустится мастер настройки (см. [«Мастер настройки»](/start/wizard/)).

**Готово.** Бот стартует без базы данных; БД и связь с панелью вы зададите в мастере.

Порты 80/443 нужны только для встроенного HTTPS вебхуков (см. [«Вебхуки»](/payments/webhooks/)). Для базовой продажи открывать их не обязательно.

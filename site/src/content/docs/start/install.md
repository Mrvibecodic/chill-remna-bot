---
title: Установка за 4 шага
description: "Установка бота на сервер через Docker Compose: файл, два значения, запуск, первый вход."
---

Бот ставится готовым образом из `ghcr.io/mrvibecodic/chill-remna-bot` — на сервере ничего не собирается. По умолчанию в `docker-compose.yml` стоит тег `:v1`: приходят все релизы `1.x.y` (см. [Каналы релизов](/ops/channels/)).

## Шаг 1. Создайте папку и возьмите docker-compose.yml

```bash
mkdir -p /opt/remnachillbot && cd /opt/remnachillbot
curl -fsSL -o docker-compose.yml \
  https://raw.githubusercontent.com/Mrvibecodic/chill-remna-bot/main/docker-compose.yml
```

## Шаг 2. Впишите два значения

```bash
nano docker-compose.yml
```

Замените заглушки `ВПИШИТЕ_ТОКЕН_БОТА` и `ВПИШИТЕ_ВАШ_TELEGRAM_ID`, остальное не трогайте:

```yaml
      BOT_TOKEN: "123456789:AAEabc..."     # токен от @BotFather
      ADMIN_TELEGRAM_ID: "000000000"        # ваш Telegram ID (число)
```

Сохраните и выйдите: `Ctrl+O`, `Enter`, `Ctrl+X`.

:::note
Ставите не в `/opt/remnachillbot` — поменяйте в этом же файле путь `/opt/remnachillbot:/compose` и `COMPOSE_HOST_DIR` на свою папку. Контейнер панели называется не `remnawave` — впишите его имя в `PANEL_CONTAINER`.
:::

## Шаг 3. Скачайте образ и запустите

```bash
docker compose pull
docker compose up -d
```

Логи: `docker compose logs -f` (выход — `Ctrl+C`, бот продолжит работать).

## Шаг 4. Откройте бота и пройдите мастер

Напишите боту **`/start`** — запустится [мастер настройки](/start/wizard/). В нём вы выберете базу данных и подключите панель.

Для первого запуска открывать порты не нужно. Публичный HTTPS-адрес понадобится позже — для вебхуков платёжек, Mini App и веб-кабинета: встроенный (порты 80/443) или через ваш reverse-proxy. См. [Вебхуки](/payments/webhooks/).

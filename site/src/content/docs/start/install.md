---
title: Установка за 4 шага
description: "Установка бота на сервер: скачать один файл, вписать два значения, запустить и написать боту."
---

Бот ставится готовым образом, на сервере ничего не собирается. Что нужно до установки — [Что подготовить](/start/requirements/).

## Шаг 1. Создайте папку и скачайте файл

```bash
mkdir -p /opt/remnachillbot && cd /opt/remnachillbot
curl -fsSL -o docker-compose.yml \
  https://raw.githubusercontent.com/Mrvibecodic/chill-remna-bot/main/docker-compose.yml
```

## Шаг 2. Впишите два значения

```bash
nano docker-compose.yml
```

В файле нужно заменить две заглушки — `ВПИШИТЕ_ТОКЕН_БОТА` и `ВПИШИТЕ_ВАШ_TELEGRAM_ID`:

```yaml
      BOT_TOKEN: "123456789:AAEabc..."     # токен от @BotFather
      ADMIN_TELEGRAM_ID: "000000000"        # ваш Telegram ID (число)
```

Сохранение и выход: `Ctrl+O`, `Enter`, `Ctrl+X`.

:::note[Другая папка или имя контейнера панели]
- Другая папка — в этом же файле нужно поменять путь `/opt/remnachillbot:/compose` и значение `COMPOSE_HOST_DIR`.
- Контейнер панели называется не `remnawave` — его имя нужно вписать в `PANEL_CONTAINER`.
:::

## Шаг 3. Запустите

```bash
docker compose pull
docker compose up -d
```

Логи: `docker compose logs -f` (выход — `Ctrl+C`, бот продолжит работать).

## Шаг 4. Напишите боту

Команда **`/start`** в чате с ботом запускает [мастер настройки](/start/wizard/): база данных и подключение к панели.

## Что дальше

- Для первого запуска открытые порты не нужны.
- Адрес с HTTPS нужен для уведомлений платёжных систем, мини-аппа и веб-кабинета: [Вебхуки и свой домен](/payments/webhooks/).
- В файле стоит тег `:v1` — приходят все релизы `1.x`: [Каналы релизов](/ops/channels/).

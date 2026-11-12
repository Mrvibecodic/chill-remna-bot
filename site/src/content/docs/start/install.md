---
title: Установка за 4 шага
description: "Установка бота на сервер: скачать один файл, вписать два значения, запустить и написать боту."
---

Бот ставится готовым образом — на сервере ничего не собирается. Сначала соберите всё из списка [Что подготовить](/start/requirements/).

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

Замените заглушки `ВПИШИТЕ_ТОКЕН_БОТА` и `ВПИШИТЕ_ВАШ_TELEGRAM_ID`, остальное не трогайте:

```yaml
      BOT_TOKEN: "123456789:AAEabc..."     # токен от @BotFather
      ADMIN_TELEGRAM_ID: "000000000"        # ваш Telegram ID (число)
```

Сохраните и выйдите: `Ctrl+O`, `Enter`, `Ctrl+X`.

:::note[Если у вас не как в примере]
- Ставите в другую папку — поменяйте в этом же файле путь `/opt/remnachillbot:/compose` и значение `COMPOSE_HOST_DIR`.
- Контейнер панели называется не `remnawave` — впишите его имя в `PANEL_CONTAINER`.
:::

## Шаг 3. Запустите

```bash
docker compose pull
docker compose up -d
```

Логи: `docker compose logs -f` (выход — `Ctrl+C`, бот продолжит работать).

## Шаг 4. Напишите боту

Отправьте боту **`/start`** — запустится [мастер настройки](/start/wizard/): база данных и подключение к панели.

## Что дальше

- Порты для первого запуска открывать не нужно.
- Адрес с HTTPS понадобится позже — для уведомлений платёжных систем, мини-аппа и веб-кабинета: [Вебхуки и свой домен](/payments/webhooks/).
- По умолчанию стоит тег `:v1` — приходят все релизы `1.x`: [Каналы релизов](/ops/channels/).

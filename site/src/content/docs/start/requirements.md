---
title: Что нужно перед стартом
description: Сервер, Docker, панель Remnawave, токен бота и Telegram ID администратора.
---

1. **Сервер с Linux** (Ubuntu/Debian подойдут) на процессоре **x86-64 (amd64)** или **ARM64 (aarch64)** — образ собирается под обе архитектуры, `docker pull` подберёт нужную сам. На сервере установлены **Docker** и **Docker Compose v2**. Проверка:
   ```bash
   docker version
   docker compose version
   ```
   Если команд нет — поставьте Docker по официальной инструкции: https://docs.docker.com/engine/install/

2. **Работающая панель Remnawave** (бот к ней подключается, сам её не ставит). Поддерживаются **2.7.4+ и 3.x** — бот сам определяет, с какой версией API имеет дело (в 3.0.0 панель убрала `uuid` пользователя и поиск по Telegram ID, бот умеет оба варианта), так что обновлять панель и бота можно в любом порядке.

3. **Токен бота.** Откройте [@BotFather](https://t.me/BotFather) → команда `/newbot` → задайте имя → получите строку вида `123456789:AAEabc...`. Это `BOT_TOKEN`.

4. **Ваш Telegram ID** (число). Откройте [@userinfobot](https://t.me/userinfobot) → он пришлёт ваш `Id`, например `000000000`. Это `ADMIN_TELEGRAM_ID` — у этого аккаунта будет доступ в админку.

5. **API-токен панели Remnawave** — создаётся в дашборде панели (раздел с API-ключами, роль API). Его **не** надо вписывать в файл — введёте в мастере внутри бота.
   На панели **3.x** у токенов есть скоупы. При обновлении панели они мигрируют сами, но если создаёте токен заново — боту нужны:
   `users:` list, stream, by-username, create, update, delete, reset-traffic, revoke-subscription; `hwid-user-devices:` list-by-user, delete-all; `internal-squads:list`, `external-squads:list`, `hosts:list`, `system:stats`.
   Без `users:stream` бот не сможет найти пользователя по Telegram ID.
   Если пользуетесь торрент-блокером, добавьте ещё `node-plugins:` list, get, update, executor — без них кнопки «Снять блокировку IP» и «В исключения блокера» ответят 403.

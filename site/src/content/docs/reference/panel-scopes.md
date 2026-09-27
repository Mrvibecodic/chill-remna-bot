---
title: Права токена панели
description: Какие скоупы API-токена Remnawave 3.x нужны боту — готовый список для вставки и разбивка по функциям.
---

На панели Remnawave **3.x** у API-токена есть скоупы: токен может только то, что в них перечислено. Если права не хватает, панель отвечает `403 Forbidden`, и функция не работает.

На панели **2.x** скоупов нет — токен может всё, эта страница не нужна. При обновлении панели до 3.x старые токены переносятся сами.

## Готовый список

Всё, что нужно боту со всеми функциями:

```
system:remnawave-health
system:stats
users:list
users:stream
users:by-id
users:by-username
users:create
users:update
users:delete
users:reset-traffic
users:revoke-subscription
internal-squads:list
external-squads:list
hosts:list
hwid-user-devices:list-by-user
hwid-user-devices:delete-all
node-plugins:list
node-plugins:update
node-plugins:executor
subscription-page-configs:list
subscriptions:subpage-config
```

## Обязательный минимум

Без этих прав бот не работает: не проверит связь, не найдёт пользователя, не выдаст и не продлит подписку.

| Скоуп | Зачем |
|---|---|
| `system:remnawave-health` | проверка связи с панелью — с неё начинаются мастер и экран «Подключение к панели» |
| `system:stats` | число пользователей панели в мастере, `/status` и «Системе» |
| `users:stream` | поиск аккаунта по Telegram ID |
| `users:by-username` | поиск аккаунтов без Telegram ID (например, зарегистрированных в кабинете по почте) |
| `users:by-id` | привязка существующего аккаунта панели к человеку в админке |
| `users:list` | обход всех аккаунтов: переезд с другого бота, сверки, доп-подписка |
| `users:create` | новая подписка и триал |
| `users:update` | продление, лимиты, сквады, блокировка и разблокировка |
| `users:delete` | удаление аккаунта из админки и снятие доп-подписки |
| `users:reset-traffic` | обнуление трафика |
| `users:revoke-subscription` | перевыпуск ссылки подписки |
| `internal-squads:list` | выбор внутренних сквадов в мастере, тарифах и триале |
| `external-squads:list` | выбор внешнего сквада |

## По функциям

Нужны, только если функция включена. Без них остальное работает.

| Функция | Скоупы | Что будет без них |
|---|---|---|
| Страны и число конфигов в карточке тарифа | `hosts:list` | строки со странами не будет, продажа не пострадает |
| «Мои устройства»: список и сброс | `hwid-user-devices:list-by-user`, `hwid-user-devices:delete-all` | список пуст, сброс отвечает ошибкой |
| Торрент-блокер: «Снять блокировку IP» и «В исключения» | `node-plugins:list`, `node-plugins:update`, `node-plugins:executor` | кнопки отвечают `403` |
| Мини-апп «Подключение»: приложения из настроек страницы подписки | `subscription-page-configs:list`, `subscriptions:subpage-config` | бот покажет свой встроенный список приложений |

## Короткий вариант

Панель принимает и широкие права: `*` (всё), `ресурс:*` (например, `users:*`), `ресурс:read` или `ресурс:write` (все чтения или все записи ресурса). Поэтому вместо списка выше подойдёт:

```
users:*
system:read
internal-squads:list
external-squads:list
hosts:list
hwid-user-devices:*
node-plugins:*
subscription-page-configs:list
subscriptions:subpage-config
```

Прав будет больше, чем нужно, но бот заработает.

## Если панель отвечает 403

- **Токен не сохраняется, «панель не на связи».** Скорее всего, нет `system:remnawave-health`: проверка связи начинается с него, и без него токен выглядит нерабочим целиком.
- **403 на кнопках торрент-блокера.** Нет `node-plugins:update` или `node-plugins:executor`.
- **Бот «теряет» подписку и заводит второй аккаунт.** Проверьте `users:stream` и `users:by-username`.

Токен меняется в боте: **Система → 🔌 Подключение к панели → 🔑 API-token**. Бот проверяет его на панели до сохранения, так что нехватку прав видно сразу.

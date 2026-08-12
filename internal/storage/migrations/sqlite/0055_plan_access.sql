-- Списки допущенных к тарифу: режим доступности «по списку» (см. model.Plan).
-- Отдельная таблица, а не поле в строке тарифа: выдача доступа из карточки
-- пользователя не должна гоняться с правкой тарифа за одну строку — записи
-- применяются немедленно и по одной.
--
-- Запись либо про Telegram-аккаунт (telegram_id != 0), либо про e-mail-аккаунт
-- кабинета (email != ''): у таких аккаунтов синтетический отрицательный
-- telegram_id, и сопоставление идёт по почте.
CREATE TABLE IF NOT EXISTS plan_access (
    plan_code   TEXT   NOT NULL,
    telegram_id BIGINT NOT NULL DEFAULT 0,
    email       TEXT   NOT NULL DEFAULT '',
    created_at  TEXT   NOT NULL DEFAULT '',
    PRIMARY KEY (plan_code, telegram_id, email)
);

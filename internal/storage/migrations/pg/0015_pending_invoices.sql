-- Рабочий список реконсилятора: выставленные, но не подтверждённые инвойсы
-- (YooKassa/CryptoBot). Фоновый проход добивает выдачу, если вебхук не дошёл.
CREATE TABLE IF NOT EXISTS pending_invoices (
    id          BIGINT PRIMARY KEY,
    method      TEXT NOT NULL,
    ext_id      TEXT NOT NULL,
    telegram_id BIGINT NOT NULL,
    months      INTEGER NOT NULL,
    created_at  TEXT NOT NULL DEFAULT '',
    resolved    INTEGER NOT NULL DEFAULT 0
)

-- Счета трафик-тарифа и пакетов трафика. Отдельно от pending_invoices:
-- предыдущий образ бота проводит любой незнакомый счёт оттуда как покупку
-- срока, а эту таблицу не видит вовсе.
CREATE TABLE IF NOT EXISTS traffic_invoices (
    id            INTEGER PRIMARY KEY,
    method        TEXT NOT NULL,
    ext_id        TEXT NOT NULL,
    telegram_id   BIGINT NOT NULL,
    purpose       TEXT NOT NULL DEFAULT '',
    kopecks       BIGINT NOT NULL DEFAULT 0,
    plan_snapshot TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT '',
    resolved      INTEGER NOT NULL DEFAULT 0
)

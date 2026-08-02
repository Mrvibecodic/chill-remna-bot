CREATE TABLE IF NOT EXISTS autopay (
    telegram_id INTEGER PRIMARY KEY,
    method      TEXT NOT NULL DEFAULT 'yookassa',
    method_id   TEXT NOT NULL DEFAULT '',
    title       TEXT NOT NULL DEFAULT '',
    months      INTEGER NOT NULL DEFAULT 1,
    amount      TEXT NOT NULL DEFAULT '',
    currency    TEXT NOT NULL DEFAULT 'RUB',
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL DEFAULT '',
    last_pay_at TEXT NOT NULL DEFAULT '',
    paid_period TEXT NOT NULL DEFAULT '',
    next_try_at TEXT NOT NULL DEFAULT '',
    fails       INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT ''
)

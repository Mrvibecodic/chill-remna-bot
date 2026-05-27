CREATE TABLE IF NOT EXISTS p2p_requests (
    id          BIGINT PRIMARY KEY,
    telegram_id BIGINT NOT NULL,
    months      INTEGER NOT NULL,
    price       TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    screenshot  TEXT NOT NULL DEFAULT '',
    comment     TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT '',
    decided_at  TEXT NOT NULL DEFAULT ''
)

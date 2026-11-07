-- Кто пришёл по приглашению: по строке на человека и код.
CREATE TABLE IF NOT EXISTS invite_uses (
    code        TEXT NOT NULL,
    telegram_id BIGINT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (code, telegram_id)
)

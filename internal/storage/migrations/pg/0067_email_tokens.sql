-- Одноразовые ссылки из писем: подтверждение почты и сброс пароля.
--
-- Хранится только отпечаток (SHA-256) — утечка базы не должна давать вход в
-- чужой кабинет. used_at гасит повторное использование, expires_at — срок.
CREATE TABLE IF NOT EXISTS email_tokens (
    hash       TEXT   PRIMARY KEY,
    tg_id      BIGINT NOT NULL,
    purpose    TEXT   NOT NULL,
    email      TEXT   NOT NULL DEFAULT '',
    created_at TEXT   NOT NULL DEFAULT '',
    expires_at TEXT   NOT NULL DEFAULT '',
    used_at    TEXT   NOT NULL DEFAULT ''
);

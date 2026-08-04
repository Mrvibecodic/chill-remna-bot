CREATE TABLE IF NOT EXISTS invites (
    code       TEXT PRIMARY KEY,
    max_uses   INTEGER NOT NULL DEFAULT 1,
    used       INTEGER NOT NULL DEFAULT 0,
    expires_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT '',
    revoked    INTEGER NOT NULL DEFAULT 0,
    note       TEXT NOT NULL DEFAULT ''
)

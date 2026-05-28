CREATE TABLE IF NOT EXISTS media_cache (
    section    TEXT PRIMARY KEY,
    file_id    TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
)

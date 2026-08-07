-- Индекс под счётчик повторных нарушений по пользователю за период.
CREATE INDEX IF NOT EXISTS idx_torrent_reports_user ON torrent_reports (telegram_id, created_at)

-- Момент последней автоблокировки по торрентам: с него начинается новый отсчёт
-- нарушений, иначе вернувшего доступ пользователя выключало бы снова.
CREATE TABLE IF NOT EXISTS torrent_strikes (tg_id BIGINT PRIMARY KEY, struck_at TEXT NOT NULL)

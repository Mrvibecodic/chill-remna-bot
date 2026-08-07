-- Отбор нарушителя без Telegram идёт по username панели: без индекса это полный
-- скан журнала на каждый отчёт такого аккаунта.
CREATE INDEX IF NOT EXISTS idx_torrent_reports_name ON torrent_reports (username, created_at)

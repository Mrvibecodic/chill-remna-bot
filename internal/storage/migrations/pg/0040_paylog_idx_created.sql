-- Индекс по времени: под окно выборки («за сутки», «за 7 дней») и под чистку
-- старых записей, которая раньше тоже шла полным сканом.
CREATE INDEX IF NOT EXISTS idx_paylog_created ON payment_log(created_at)

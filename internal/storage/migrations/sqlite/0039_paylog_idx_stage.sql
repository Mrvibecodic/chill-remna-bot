-- Индекс по этапу: выгрузка «только ошибки» отбирает записи по stage, и без
-- индекса на нагруженном боте это полный скан самой быстрорастущей таблицы.
CREATE INDEX IF NOT EXISTS idx_paylog_stage ON payment_log(stage)

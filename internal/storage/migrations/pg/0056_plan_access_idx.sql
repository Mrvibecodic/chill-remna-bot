-- Удаление пользователя чистит его записи во всех тарифах — поиск по
-- telegram_id не должен перебирать всю таблицу.
CREATE INDEX IF NOT EXISTS idx_plan_access_tg ON plan_access(telegram_id);

-- Сверка лимитов ищет последнюю покупку каждого пользователя; без индекса это
-- полный скан payments на каждого.
CREATE INDEX IF NOT EXISTS idx_payments_user_created ON payments (telegram_id, created_at)

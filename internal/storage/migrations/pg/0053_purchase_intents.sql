-- Намерение покупки: что человек выбрал на экране «выбор срока».
--
-- Раньше выбор жил только в памяти процесса (uiState.buyMonths). Экран с
-- кнопками переживает рестарт и остаётся рабочим, поэтому выбравший 12 месяцев
-- после перезапуска бота получал счёт на 1 — молча.
--
-- Строка ровно одна на человека и означает «что выбрано сейчас». Условия
-- выставленных счетов здесь НЕ хранятся: у них своя таблица
-- (invoice_snapshots), иначе счёт, выставленный из мини-аппа, перебивал бы
-- выбор, сделанный в чате.
CREATE TABLE IF NOT EXISTS purchase_intents (
    telegram_id   BIGINT PRIMARY KEY,
    plan_code     TEXT NOT NULL DEFAULT '',
    months        INTEGER NOT NULL DEFAULT 0,
    days          INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT ''
)

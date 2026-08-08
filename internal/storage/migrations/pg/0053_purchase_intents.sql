-- Намерение покупки: что человек выбрал на экране «выбор срока».
--
-- Раньше выбор жил только в памяти процесса (uiState.buyMonths). Экран с
-- кнопками переживает рестарт и остаётся рабочим, поэтому выбравший 12 месяцев
-- после перезапуска бота получал счёт на 1 — молча. Плюс это единственный
-- носитель тарифа для Stars: их payload трогать нельзя (разбор превращает в
-- число весь остаток строки, и любой добавленный сегмент отклонит легитимную
-- оплату на предпроверке).
CREATE TABLE IF NOT EXISTS purchase_intents (
    telegram_id   BIGINT PRIMARY KEY,
    plan_code     TEXT NOT NULL DEFAULT '',
    months        INTEGER NOT NULL DEFAULT 0,
    days          INTEGER NOT NULL DEFAULT 0,
    plan_snapshot TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT ''
)

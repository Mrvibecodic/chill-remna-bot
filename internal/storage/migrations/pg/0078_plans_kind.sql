-- Вид тарифа: '' — срочный, 'traffic' — трафик (бессрочно, пакеты гигабайт).
ALTER TABLE plans ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT '';

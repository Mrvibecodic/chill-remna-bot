-- Вид тарифа: '' — срочный, 'traffic' — трафик (бессрочно, пакеты гигабайт).
ALTER TABLE plans ADD COLUMN kind TEXT NOT NULL DEFAULT '';

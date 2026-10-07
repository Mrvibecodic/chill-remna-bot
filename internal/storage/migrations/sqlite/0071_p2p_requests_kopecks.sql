-- Сумма пополнения в копейках. Админ может исправить её до одобрения, если
-- клиент перевёл не столько, сколько указал.
ALTER TABLE p2p_requests ADD COLUMN kopecks INTEGER NOT NULL DEFAULT 0;

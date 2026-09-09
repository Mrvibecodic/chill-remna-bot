-- Счётчик недавно выданных ссылок (порог отправки) и гашение прежних при
-- выдаче новой ходят по паре «аккаунт + назначение».
CREATE INDEX IF NOT EXISTS idx_email_tokens_owner ON email_tokens(tg_id, purpose);

-- Назначение заявки на перевод: пусто — покупка подписки, topup — пополнение
-- баланса.
ALTER TABLE p2p_requests ADD COLUMN IF NOT EXISTS purpose TEXT NOT NULL DEFAULT '';

-- Выбранный пакет трафика ('' — выбран срок или трафик-тариф).
ALTER TABLE purchase_intents ADD COLUMN pack_id TEXT NOT NULL DEFAULT '';

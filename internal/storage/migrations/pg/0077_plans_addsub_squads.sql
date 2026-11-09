-- Сквады доп-подписки у тарифа (JSON-список); '' — как в общих настройках.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS addsub_squads TEXT NOT NULL DEFAULT '';

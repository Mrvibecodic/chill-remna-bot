-- Сквады доп-подписки у тарифа (JSON-список); '' — как в общих настройках.
ALTER TABLE plans ADD COLUMN addsub_squads TEXT NOT NULL DEFAULT '';

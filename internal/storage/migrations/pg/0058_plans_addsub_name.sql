-- Название опции доп-подписки для этого тарифа ('' — общее).
ALTER TABLE plans ADD COLUMN addsub_name TEXT NOT NULL DEFAULT '';

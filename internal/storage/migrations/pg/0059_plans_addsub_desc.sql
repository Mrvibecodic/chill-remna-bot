-- Описание опции доп-подписки для этого тарифа ('' — общее).
ALTER TABLE plans ADD COLUMN addsub_desc TEXT NOT NULL DEFAULT '';

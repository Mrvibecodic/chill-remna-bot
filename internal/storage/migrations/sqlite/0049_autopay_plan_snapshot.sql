-- Снимок в автосписании: продлевать надо то, на что человек подписался, а не
-- то, что сейчас в конфиге.
ALTER TABLE autopay ADD COLUMN plan_snapshot TEXT NOT NULL DEFAULT '';

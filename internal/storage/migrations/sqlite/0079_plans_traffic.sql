-- Условия трафик-тарифа (JSON: цена, звёзды, пакеты, сброс доп-подписки).
ALTER TABLE plans ADD COLUMN traffic TEXT NOT NULL DEFAULT '';

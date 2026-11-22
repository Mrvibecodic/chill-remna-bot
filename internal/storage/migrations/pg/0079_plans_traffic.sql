-- Условия трафик-тарифа (JSON: цена, звёзды, пакеты, сброс доп-подписки).
ALTER TABLE plans ADD COLUMN IF NOT EXISTS traffic TEXT NOT NULL DEFAULT '';

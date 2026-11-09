-- Трафик доп-подписки у тарифа в ГБ: -1 — как в общих настройках, 0 — безлимит.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS addsub_traffic_gb INTEGER NOT NULL DEFAULT -1;

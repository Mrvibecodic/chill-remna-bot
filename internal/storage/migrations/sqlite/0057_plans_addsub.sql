-- Режим доп-подписки у тарифа: '' = наследовать глобальный переключатель
-- (поведение до появления поля), 'on' / 'off' — явный выбор админа.
ALTER TABLE plans ADD COLUMN addsub TEXT NOT NULL DEFAULT '';

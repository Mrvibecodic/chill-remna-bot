-- Тарифы (см. model.Plan). Отдельная таблица, а не поле конфига: конфиг лежит
-- одним зашифрованным JSON-блобом, и предыдущий образ бота молча выбрасывает
-- незнакомые поля при любом сохранении — про таблицу он не знает и испортить
-- её не может.
CREATE TABLE IF NOT EXISTS plans (
    code         TEXT PRIMARY KEY,
    name         TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    icon         TEXT NOT NULL DEFAULT '',
    sort_order   INTEGER NOT NULL DEFAULT 0,
    enabled      INTEGER NOT NULL DEFAULT 0,
    traffic_gb   INTEGER NOT NULL DEFAULT 0,
    device_limit INTEGER NOT NULL DEFAULT 0,
    strategy     TEXT NOT NULL DEFAULT '',
    int_squads   TEXT NOT NULL DEFAULT '',
    ext_squad    TEXT NOT NULL DEFAULT '',
    availability TEXT NOT NULL DEFAULT 'all',
    currency     TEXT NOT NULL DEFAULT '',
    durations    TEXT NOT NULL DEFAULT '',
    from_config  INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL DEFAULT ''
)

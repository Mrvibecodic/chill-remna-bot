-- Журнал торрент-блокера: одна запись на каждый отчёт панели
-- torrent_blocker.report. По нему считаются повторные нарушения и
-- отправляются отложенные уведомления о снятии блокировки.
CREATE TABLE IF NOT EXISTS torrent_reports (
    id               INTEGER PRIMARY KEY,
    telegram_id      INTEGER NOT NULL DEFAULT 0,
    username         TEXT NOT NULL DEFAULT '',
    node             TEXT NOT NULL DEFAULT '',
    ip               TEXT NOT NULL DEFAULT '',
    protocol         TEXT NOT NULL DEFAULT '',
    inbound          TEXT NOT NULL DEFAULT '',
    source           TEXT NOT NULL DEFAULT '',
    destination      TEXT NOT NULL DEFAULT '',
    block_seconds    INTEGER NOT NULL DEFAULT 0,
    will_unblock_at  TEXT NOT NULL DEFAULT '',
    unblock_notified INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL DEFAULT ''
)

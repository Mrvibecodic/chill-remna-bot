-- Индекс под выборку «пора уведомить о снятии блокировки».
CREATE INDEX IF NOT EXISTS idx_torrent_reports_due ON torrent_reports (unblock_notified, will_unblock_at)

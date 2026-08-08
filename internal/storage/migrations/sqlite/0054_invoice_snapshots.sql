-- Условия сделки по выставленному счёту, у которого нет строки в
-- pending_invoices. Такой счёт ровно один — Stars: его payload менять нельзя
-- (разбор превращает в число весь остаток строки, и любой добавленный сегмент
-- отклонит легитимную оплату на предпроверке), а в очередь незакрытых счетов
-- его класть тоже нельзя — реконсилятор гасит незнакомые ему методы.
--
-- Ключ — человек, способ оплаты и срок: счёт из чата и счёт из мини-аппа живут
-- в разных строках и друг друга не трогают.
CREATE TABLE IF NOT EXISTS invoice_snapshots (
    telegram_id   INTEGER NOT NULL,
    method        TEXT NOT NULL,
    months        INTEGER NOT NULL,
    plan_snapshot TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (telegram_id, method, months)
)

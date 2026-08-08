-- Снимок в незакрытом счёте: реконсилятор добивает оплату именно из этой
-- строки, а не из payload провайдера, — значит и условия должен брать отсюда.
ALTER TABLE pending_invoices ADD COLUMN plan_snapshot TEXT NOT NULL DEFAULT '';

-- Снимок в заявке на перевод: между выставлением реквизитов и одобрением
-- админом проходят часы, за которые цены и лимиты могли поменяться.
ALTER TABLE p2p_requests ADD COLUMN plan_snapshot TEXT NOT NULL DEFAULT '';

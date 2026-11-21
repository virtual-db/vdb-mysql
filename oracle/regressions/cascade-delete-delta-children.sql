-- vdb-mysql oracle regression: source / DELETE FROM ORC_583A44B_W2_A _A / table contents
-- seed: 2
-- mode: source
UPDATE {p}_a JOIN {p}_b ON {p}_b.a_id = {p}_a.id SET {p}_b.v = {p}_b.v + 1 WHERE {p}_a.k < 2;
-- prepared: DELETE FROM {p}_a WHERE t < '2026-01-05';

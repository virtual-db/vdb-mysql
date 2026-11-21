-- vdb-mysql oracle regression: source / SELECT ORC_A634935_W2_A.ID, ORC_A634935_W2_S.R _A.ID / rows
-- seed: 2
-- mode: source
INSERT INTO {p}_a (id, k, s, ci, l, n, t, ts, u, e, j, f) SELECT id + 100001, k, s, ci, l, n, t, ts, NULL, e, j, f FROM {p}_a WHERE id BETWEEN 1134 AND 1175 ORDER BY id LIMIT 5;
SELECT COUNT(*) FROM {p}_b;
-- prepared: INSERT INTO {p}_b (id, a_id, v, w) VALUES (100030, 606, 42, 'n1');
DELETE FROM {p}_a WHERE u IS NULL AND k < 18;
SELECT {p}_a.id, {p}_s.r FROM {p}_a JOIN {p}_s ON {p}_s.q = {p}_a.k WHERE {p}_a.id < 73 ORDER BY {p}_a.id, {p}_s.id;

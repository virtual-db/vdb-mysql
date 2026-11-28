-- vdb-mysql oracle regression: source / INSERT ... SELECT FROM _A ORDER BY ID with NULL JSON heads / crash
-- seed: 1
-- mode: source
-- An index-order read of Source and Delta rows whose JSON column is NULL,
-- into a statement that also writes the table (vdb.so copied a NULL BLOB's
-- unwritten bytes from its head buffer; mysqld then crashed reading them).
UPDATE {p}_a SET j = NULL ORDER BY id LIMIT 3;
UPDATE {p}_a SET j = '{"v": 7}' WHERE j IS NULL ORDER BY id DESC LIMIT 1;
INSERT INTO {p}_a (id, k, s, ci, l, n, t, ts, u, e, j, f) SELECT id + 100001, k, s, ci, l, n, t, ts, NULL, e, j, f FROM {p}_a ORDER BY id LIMIT 40;
INSERT INTO {p}_a (id, k, s, ci, l, n, t, ts, u, e, j, f) SELECT id + 200001, k, s, ci, l, n, t, ts, NULL, e, NULL, f FROM {p}_a ORDER BY id LIMIT 80;
SELECT id, j FROM {p}_a ORDER BY id;

-- vdb-mysql oracle regression: source / SELECT ID, JSON_EXTRACT(J, _A / error
-- seed: 8
-- mode: source
SELECT id FROM {p}_a WHERE ci = 'Gamma2' ORDER BY id;
SELECT id FROM {p}_a WHERE id IN (SELECT a_id FROM {p}_b WHERE v > 12) ORDER BY id;
-- prepared: SELECT x, y, COUNT(*) FROM {p}_c GROUP BY x, y ORDER BY x, y;
SELECT id FROM {p}_a WHERE id IN (SELECT a_id FROM {p}_b WHERE v > 35) ORDER BY id;
SELECT id, JSON_EXTRACT(j, '$.v'), ts FROM {p}_a WHERE n > 126 ORDER BY id;

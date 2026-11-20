-- vdb-mysql oracle regression: single / SELECT ID, (SELECT _B / error
-- seed: 8
-- mode: single
ALTER TABLE {p}_a ADD INDEX ix_n (n);
SELECT id, (SELECT MAX(v) FROM {p}_b WHERE {p}_b.a_id = {p}_a.id) FROM {p}_a WHERE n > 309 ORDER BY id;

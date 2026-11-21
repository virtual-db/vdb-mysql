-- vdb-mysql oracle regression: source / SELECT COUNT(*) FROM _S / rows
-- seed: 6
-- mode: source
-- source: ALTER TABLE {p}_s ADD COLUMN z1 INT NOT NULL DEFAULT 4;
SELECT COUNT(*) FROM {p}_s;

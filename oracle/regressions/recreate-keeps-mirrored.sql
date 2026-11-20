-- vdb-mysql oracle regression: single / SELECT G, COUNT(*), _D / rows
-- seed: 7
-- mode: single
SELECT k, COUNT(*), SUM(n), MIN(s), MAX(t) FROM {p}_a GROUP BY k HAVING COUNT(*) > 0 ORDER BY k;
UPDATE {p}_a JOIN {p}_b ON {p}_b.a_id = {p}_a.id SET {p}_b.v = {p}_b.v + 1 WHERE {p}_a.k < 3;
SELECT x.id, y.id FROM {p}_a x JOIN {p}_a y ON x.k = y.k AND x.id < y.id WHERE x.id < 93 AND y.id < 89;
INSERT IGNORE INTO {p}_a (id, k, s, ci, l, n, t, ts, u, e, j, f) VALUES (188, 19, 'Ünder3', 'Beta0', 'm3', 244.50, '2026-02-18 00:00:00.000', '2026-04-01 08:00:00', NULL, 'x', '{"v": 29}', 3.5), (254, 18, 'zeta 1', 'x0', 'm2', 853.50, '2026-02-09 00:00:00.000', '2026-04-01 08:00:00', 3017, 'x', '{"v": 26}', 14.5);
SELECT g, COUNT(*), MAX(h) FROM {p}_d GROUP BY g ORDER BY g;

# The `vdb` schema

Every account can read the `vdb` schema. Rows about sessions and requests show only the reader's own, unless the
reader's Source account holds `PROCESS`.

| Table | Rows |
|---|---|
| `vdb.status` | `name`, `value`: state, Source and `mysqld` versions, protocol version, Delta bytes, memory ceiling and use, lock entries, persistence, grant mode, pushdown |
| `vdb.tables` | Every table: kind (mirrored, local, detached), Delta rows, touched keys, Delta bytes, unproven collations, last Source change |
| `vdb.sessions` | Open sessions: snapshot age, Source requests and rows, statements without Batched Key Access |
| `vdb.source_requests` | The last Source requests: shape, table, duration, rows, bytes, error |
| `vdb.accounts` | Mirrored accounts: method, when grants were read, whether VDB can ever match them, why not |
| `vdb.catalog_errors` | Objects that could not be created, and discovery warnings |
| `vdb.delta_sql` | The Delta as SQL: DDL, inserts, updates and deletes that would reproduce it. Never executed. |
| `vdb.schema_changes` | Source schema changes applied, and their effect on the Delta |
| `vdb.metrics` | Counters and gauges (`name`, `labels`, `value`) |

The engine's own counters are `mysqld` status variables: `SHOW GLOBAL STATUS LIKE 'vdb%'`.

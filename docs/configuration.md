# Configuration

Settings come from environment variables, then `/etc/vdb/vdb.yaml` (`VDB_CONFIG_FILE`; keys are the names below in
lower case without `VDB_`), then defaults. An invalid value stops start-up with a message naming the variable.

| Variable | Default | Meaning |
|---|---|---|
| `VDB_SOURCE_HOST`, `VDB_SOURCE_PORT` | required, 3306 | The Source |
| `VDB_SOURCE_USER`, `VDB_SOURCE_PASSWORD` or `VDB_SOURCE_PASSWORD_FILE` | required | The service account |
| `VDB_SOURCE_DATABASES` | every visible database | Comma-separated databases to expose |
| `VDB_SOURCE_TLS_FINGERPRINT` | empty | SHA-256 of a self-signed Source certificate to trust (`tls.md`) |
| `VDB_ENV_ID` | host name | Tags every Source statement's comment |
| `VDB_ENV_CLASS` | `small` | `small`, `medium` or `large`: `mysqld` buffers and connection limits |
| `VDB_LISTEN_PORT` | 3306 | The client port |
| `VDB_CLIENT_TLS_CERT_FILE`, `VDB_CLIENT_TLS_KEY_FILE` | empty | Client TLS certificate; otherwise `mysqld` generates one |
| `VDB_MEMORY_RESERVE` | 0.10 | Fraction of memory kept free; writes past it fail with 1114 |
| `VDB_DELTA_MAX_BYTES` | 0 | A fixed cap on the Delta's size (0: none) |
| `VDB_AUTO_INCREMENT_GAP` | 1000000 | Gap above the Source's AUTO_INCREMENT for new rows of mirrored tables |
| `VDB_BATCH_ROWS`, `VDB_BATCH_BYTES` | 4096, 524288 | Rows and bytes per batch and per Source page |
| `VDB_INSERT_BATCH`, `VDB_LOOKUP_BATCH` | 1000, 1000 | Rows per bulk insert batch; keys per Source lookup |
| `VDB_SOURCE_MAX_CONNECTIONS` | from the class | Snapshot connections to the Source |
| `VDB_SOURCE_CONNECT_WAIT` | 5s | Wait for a Source connection |
| `VDB_SOURCE_REQUEST_TIMEOUT` | 30s | `max_execution_time` of every Source statement |
| `VDB_SOURCE_REQUEST_MAX_ROWS` | 100000 | Rows per Source request |
| `VDB_MAX_SOURCE_ROWS` | 0 | Rows one statement may read from the Source (0: no limit) |
| `VDB_SOURCE_RETRY_WINDOW` | 10s | Retries before a snapshot opens |
| `VDB_SOURCE_SNAPSHOT` | `on` | `off` reads without a consistent snapshot |
| `VDB_SOURCE_SNAPSHOT_IDLE_TIMEOUT`, `VDB_SOURCE_SNAPSHOT_MAX_AGE` | 60s, 15m | Snapshot limits |
| `VDB_SOURCE_SNAPSHOT_POLICY` | `strict` | `relaxed` renews an expired snapshot with a warning |
| `VDB_SOURCE_BUSY_THREADS` | 64 | Background reads pause above this many running Source threads |
| `VDB_COUNT_MAX_SHADOWED` | 100000 | Above this many changed keys, `COUNT(*)` counts rows instead |
| `VDB_PUSHDOWN` | `on` | Push conditions to the Source |
| `VDB_SCHEMA_POLL`, `VDB_GRANT_POLL` | 60s, 30s | Polling intervals |
| `VDB_SOURCE_LOG_SIZE`, `VDB_SOURCE_LOG_FILE` | 10000, empty | The Source request log |
| `VDB_LOG_FORMAT` | `json` | `json` or `text` |
| `VDB_LOG_LEVEL` | `info` | `debug` adds debug-level logs (pushdown decisions, Source requests); environment only |
| `VDB_PLUGIN_DIR` | `/var/lib/vdb/plugins` | VDB plugins (none ship in v1) |
| `VDB_MYSQLD_STOP_TIMEOUT`, `VDB_MYSQLD_RESTART_WINDOW` | 30s, 5m | `mysqld` shutdown; three crashes in the window stop the container |
| `VDB_MYSQLD_SORT_BUFFER_SIZE` and the other `VDB_MYSQLD_*` sizes | from the class | Override one class value |

# Configuration

vdb-mysql is configured entirely through environment variables. There are no configuration files.

## Environment Variables

| Variable | Default | Required | Description |
|---|---|---|---|
| `VDB_SOURCE_DSN` | _(empty)_ | **Yes** | DSN for the source MySQL server: `user:password@tcp(host:port)/dbname` |
| `VDB_AUTH_SOURCE_ADDR` | _(empty)_ | **Yes** | `host:port` of the source MySQL server, used for auth probes. |
| `VDB_LISTEN_ADDR` | `:3306` | No | TCP address and port vdb-mysql listens on. |
| `VDB_DB_NAME` | `appdb` | No | Database name exposed to clients. Must match the database in `VDB_SOURCE_DSN`. |
| `VDB_PLUGIN_DIR` | `plugins` | No | Directory scanned one level deep for plugin subdirectories. Empty means no plugins are loaded. |
| `VDB_TLS_CERT_FILE` | _(empty)_ | No | Path to a PEM-encoded TLS certificate. Enables TLS on the listener. Requires `VDB_TLS_KEY_FILE`. |
| `VDB_TLS_KEY_FILE` | _(empty)_ | No | Path to a PEM-encoded private key. Required when `VDB_TLS_CERT_FILE` is set. |

---

## Source Database Setup

### Read-only service account

vdb-mysql needs a dedicated, read-only MySQL account to fetch schema metadata and row data from the source. This account is separate from your application users. Granting only `SELECT` ensures vdb-mysql can never modify the source at the database layer, regardless of what any plugin does.

```sql
CREATE USER IF NOT EXISTS 'vdb_user'@'%' IDENTIFIED BY '<choose-a-password>';
GRANT SELECT ON <your_database>.* TO 'vdb_user'@'%';
FLUSH PRIVILEGES;
```

Use this account's credentials in `VDB_SOURCE_DSN`. Your application users do not need to change — vdb-mysql proxies their credentials to the source on every connection.

### Outbound connections

vdb-mysql opens two types of outbound connections to the source MySQL server:

| Type | Frequency | Purpose |
|---|---|---|
| Auth probe | Once per client connection | Verify credentials via the MySQL handshake, then immediately closed |
| Data pool | Persistent, shared | Fetch schema from `INFORMATION_SCHEMA` and read row data |

`VDB_AUTH_SOURCE_ADDR` and the host in `VDB_SOURCE_DSN` should point to the same MySQL instance.

---

## TLS

TLS on the vdb-mysql listener is optional. Provide a certificate and key to enable it.

```sh
VDB_TLS_CERT_FILE=/etc/vdb/server.crt \
VDB_TLS_KEY_FILE=/etc/vdb/server.key \
./vdb-mysql
```

**With TLS enabled:**
- The listener advertises `caching_sha2_password`. The SHA2 fast-auth cache-hit path is permanently disabled so that every connection goes through full-auth, which is required to obtain the plaintext password for the source-database credential probe.
- Minimum TLS version is 1.2.
- The system certificate pool is loaded at startup. Client certificate authentication is not currently supported, so the pool has no active effect in this release.

**Without TLS:**
- The listener advertises `mysql_clear_password` and accepts plaintext connections.
- Most MySQL clients refuse `mysql_clear_password` by default. You must enable cleartext auth explicitly on every connecting client (e.g. `--enable-cleartext-plugin` for the MySQL CLI, `allowCleartextPasswords=true` in a Go DSN). See [TLS configuration](./tls.md#connecting-without-tls) for per-client details.
- Suitable for local development or environments where network security is handled at the infrastructure level (private VPC, mTLS service mesh, etc.) and where you can control client configuration.
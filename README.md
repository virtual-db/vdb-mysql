# vdb-mysql

A MySQL-protocol proxy that sits in front of your existing MySQL database, intercepts every query and write, and routes them through a plugin layer — without touching the source data.

---

## Quick Start

The fastest path to a running instance is Docker Compose. The example below starts vdb-mysql alongside a MySQL 8 source database.

**1. Create the source database users**

Run the following against your MySQL source before starting vdb-mysql:

```sql
-- Read-only account used by vdb-mysql to fetch schema and row data
CREATE USER IF NOT EXISTS 'vdb_user'@'%' IDENTIFIED BY 'vdbuserpass';
GRANT SELECT ON myapp.* TO 'vdb_user'@'%';

-- Application account used by your app to connect through vdb-mysql
CREATE USER IF NOT EXISTS 'myapp_user'@'%' IDENTIFIED BY 'myapppass';
GRANT ALL PRIVILEGES ON myapp.* TO 'myapp_user'@'%';

FLUSH PRIVILEGES;
```

**2. Create a `docker-compose.yml`**

```yaml
services:

  source-db:
    image: mysql:8.0
    restart: unless-stopped
    environment:
      MYSQL_ROOT_PASSWORD: rootpassword
      MYSQL_DATABASE: myapp
    volumes:
      - db-data:/var/lib/mysql
    healthcheck:
      test: ["CMD", "mysqladmin", "ping", "-h", "127.0.0.1", "-u", "root", "-prootpassword", "--silent"]
      interval: 2s
      timeout: 5s
      retries: 30
      start_period: 10s

  vdb:
    image: ghcr.io/anqordx/vdb-mysql:latest
    restart: unless-stopped
    environment:
      VDB_LISTEN_ADDR: ":3306"
      VDB_DB_NAME: myapp
      VDB_SOURCE_DSN: "vdb_user:vdbuserpass@tcp(source-db:3306)/myapp"
      VDB_AUTH_SOURCE_ADDR: "source-db:3306"
    ports:
      - "3306:3306"
    depends_on:
      source-db:
        condition: service_healthy

volumes:
  db-data:
```

**3. Start**

```
docker compose up -d
```

**4. Connect**

```
mysql -h 127.0.0.1 -P 3306 -u myapp_user -pmyapppass myapp
```

Your application now connects to vdb-mysql on port 3306 exactly as it would connect to MySQL directly. The source database remains on its original port and is not exposed to your application.

---

## Requirements

- **Source database**: MySQL 8.x
- **Docker**: 20.10 or later (for container deployment)
- **Go**: 1.23.3 or later (only if building from source)
- **Network**: vdb-mysql must have TCP access to the source MySQL server

---

## Getting vdb-mysql

### Docker image

A pre-built image is available from the GitHub Container Registry:

```
docker pull ghcr.io/anqordx/vdb-mysql:latest
```

Images are tagged by release version (e.g. `ghcr.io/anqordx/vdb-mysql:v0.1.0`) and `latest` always points to the most recent stable release.

### Pre-built binary

Linux/amd64 binaries are attached to every tagged [release](https://github.com/AnqorDX/vdb-mysql/releases):

```
curl -Lo vdb-mysql https://github.com/AnqorDX/vdb-mysql/releases/latest/download/vdb-mysql-linux-amd64
chmod +x vdb-mysql
```

### Building from source

All dependencies are published to the public Go module proxy:

```
git clone https://github.com/AnqorDX/vdb-mysql
cd vdb-mysql
CGO_ENABLED=0 go build -trimpath -o vdb-mysql .
```

### Building your own Docker image

```dockerfile
FROM golang:1.23-alpine AS builder
RUN CGO_ENABLED=0 go install github.com/AnqorDX/vdb-mysql@latest

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /go/bin/vdb-mysql /usr/local/bin/vdb-mysql
EXPOSE 3306
ENTRYPOINT ["/usr/local/bin/vdb-mysql"]
```

---

## Connecting

vdb-mysql speaks standard MySQL protocol. Any client that works with MySQL 8.x works with vdb-mysql. Point it at `VDB_LISTEN_ADDR` and use credentials that exist on the source database.

**mysql CLI**

```
mysql -h 127.0.0.1 -P 3306 -u myapp_user -pmyapppass myapp
```

**Go (`database/sql`)**

```go
import _ "github.com/go-sql-driver/mysql"

db, err := sql.Open("mysql", "myapp_user:myapppass@tcp(127.0.0.1:3306)/myapp")
```

**Python**

```python
import mysql.connector
conn = mysql.connector.connect(
    host="127.0.0.1", port=3306,
    user="myapp_user", password="myapppass",
    database="myapp"
)
```

**Connection notes**

- The database name must match `VDB_DB_NAME`.
- TLS is not supported on the vdb-mysql listener. Disable SSL in your client if it is enabled by default.
- Only TCP connections are accepted — no Unix socket.

---

## Source Database Setup

vdb-mysql requires two MySQL accounts on the source database.

### Service account (vdb-mysql → source)

vdb-mysql uses this account to read schema metadata and row data. It must have `SELECT` only — this enforces at the database layer that vdb-mysql can never write to the source, regardless of what any plugin does.

```sql
CREATE USER IF NOT EXISTS 'vdb_user'@'%' IDENTIFIED BY '<password>';
GRANT SELECT ON <your_database>.* TO 'vdb_user'@'%';
FLUSH PRIVILEGES;
```

### Application account (your app → vdb-mysql)

Your application connects to the vdb-mysql endpoint using this account. vdb-mysql proxies the authentication handshake to the source on every connection — it does not maintain its own user list.

```sql
CREATE USER IF NOT EXISTS 'myapp_user'@'%' IDENTIFIED BY '<password>';
GRANT ALL PRIVILEGES ON <your_database>.* TO 'myapp_user'@'%';
FLUSH PRIVILEGES;
```

### Network access

vdb-mysql opens two types of connections to the source:

- **Auth probe**: one short-lived TCP connection per client connection, used to verify credentials during the MySQL handshake, closed immediately after.
- **Data pool**: a persistent connection pool used to query `INFORMATION_SCHEMA` and fetch row data.

Both use the same host — `VDB_AUTH_SOURCE_ADDR` and `VDB_SOURCE_DSN` should point to the same MySQL server.

---

## Configuration

vdb-mysql is configured entirely through environment variables. There are no config files.

| Variable | Default | Required | Description |
|---|---|---|---|
| `VDB_LISTEN_ADDR` | `:3306` | No | TCP address and port to listen on. |
| `VDB_DB_NAME` | `appdb` | No | Database name exposed to clients. Must match the source. |
| `VDB_SOURCE_DSN` | _(empty)_ | **Yes** | DSN for the source MySQL server: `user:password@tcp(host:port)/dbname` |
| `VDB_AUTH_SOURCE_ADDR` | _(empty)_ | **Yes** | `host:port` of the source MySQL server, used for the auth proxy handshake. |
| `VDB_PLUGIN_DIR` | `plugins` | No | Directory containing plugin subdirectories. Leave empty to run without plugins. |

---

## How It Works

When a client connects to vdb-mysql:

1. **Authentication** — vdb-mysql opens a short-lived connection to the real MySQL source and replays the MySQL handshake byte-for-byte. The source validates the credentials. vdb-mysql never stores or evaluates credentials itself.
2. **Query execution** — SQL is parsed and planned by the embedded [go-mysql-server](https://github.com/dolthub/go-mysql-server) engine.
3. **Reads** — rows are fetched from the source, passed through the plugin layer, and returned to the client.
4. **Writes** — each affected row is delivered to plugins. No write reaches the source database.

---

## Plugins

Plugins are standalone executables that extend vdb-mysql behaviour. They run as child processes and communicate with the framework over a Unix socket using JSON-RPC 2.0.

### Directory layout

Set `VDB_PLUGIN_DIR` to a directory containing one subdirectory per plugin:

```
/etc/vdb/plugins/
  my-plugin/
    manifest.json
    my-plugin        # the plugin executable
```

### manifest.json

```json
{
  "name":    "my-plugin",
  "version": "1.0.0",
  "command": ["./my-plugin"],
  "env": {
    "MY_PLUGIN_CONFIG": "/etc/my-plugin/config.json"
  }
}
```

| Field | Type | Description |
|---|---|---|
| `name` | string | Plugin identifier used in logs. Defaults to the directory name if omitted. |
| `version` | string | Informational only. Appears in startup logs. |
| `command` | []string | Command and arguments to launch the plugin. Working directory is the plugin subdirectory. |
| `env` | object | Additional environment variables passed to the plugin process. |

On startup, vdb-mysql scans `VDB_PLUGIN_DIR`, launches each plugin, and waits up to 10 seconds for it to connect and send a `declare` notification registering its pipeline handlers and event subscriptions. Plugins that fail to start or declare in time are logged and skipped.

Plugin development documentation and the JSON-RPC protocol specification are maintained in the [vdb-core](https://github.com/AnqorDX/vdb-core) repository.

---

## License

Elastic License 2.0. See [LICENSE.md](LICENSE.md).

The EL v2 license allows free use, modification, and redistribution for any purpose that does not involve offering the software as a hosted or managed service to third parties. See [CLA.md](CLA.md) for contributor requirements.

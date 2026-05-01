# vdb-mysql

[![Release](https://img.shields.io/github/v/release/virtual-db/vdb-mysql?include_prereleases&label=version&color=blue)](https://github.com/virtual-db/vdb-mysql/releases/latest)
[![Build](https://github.com/virtual-db/vdb-mysql/actions/workflows/release.yml/badge.svg)](https://github.com/virtual-db/vdb-mysql/actions/workflows/release.yml)
[![License: ELv2](https://img.shields.io/badge/license-ELv2-lightgrey)](LICENSE.md)

**VirtualDB** is a MySQL-compatible proxy that sits between your application and your MySQL database. It intercepts every query and write, routes them through a programmable plugin layer, and returns results — without ever modifying the source.

Reads come from your real database. Writes are captured in an in-memory delta and reflected back to your application. **Nothing is written to the source.**

> **Status**: Early alpha. Not recommended for production without a thorough evaluation. See [Known Limitations](docs/known-limitations.md).

---

## What it's for

- Virtualizing or transforming data without changing your schema or application
- Overlaying synthetic or sanitized rows on top of live data (e.g. for testing)
- Observing every read and write through a plugin pipeline
- Capturing change events without modifying the source database

---

## Quick Start

**1. Create a read-only service account on your source database**

```sql
CREATE USER IF NOT EXISTS 'vdb_user'@'%' IDENTIFIED BY '<password>';
GRANT SELECT ON <your_database>.* TO 'vdb_user'@'%';
FLUSH PRIVILEGES;
```

**2. Run vdb-mysql**

```sh
export VDB_SOURCE_DSN="vdb_user:secret@tcp(db.internal:3306)/myapp"
export VDB_AUTH_SOURCE_ADDR="db.internal:3306"
export VDB_DB_NAME="myapp"

./vdb-mysql
```

**3. Connect your application**

Point your app at vdb-mysql exactly as you would a normal MySQL server. Use the same credentials you already use — vdb-mysql proxies authentication to the source on every connection.

```sh
mysql -h 127.0.0.1 -P 3306 -u myuser -pmypassword myapp
```

---

## Installation

| Method | Instructions |
|---|---|
| Pre-built binary (linux/amd64) | [Download from Releases](https://github.com/virtual-db/vdb-mysql/releases) |
| Docker (official image) | `docker pull ghcr.io/virtual-db/vdb-mysql:latest` — see [Docker usage](docs/installation.md#docker) |
| Build from source | [Build instructions](docs/installation.md#build-from-source) |

---

## Configuration

vdb-mysql is configured entirely through environment variables — no config files.

| Variable | Required | Default | Description |
|---|---|---|---|
| `VDB_SOURCE_DSN` | **Yes** | — | DSN for the source MySQL server |
| `VDB_AUTH_SOURCE_ADDR` | **Yes** | — | `host:port` used for auth probes |
| `VDB_LISTEN_ADDR` | No | `:3306` | Address vdb-mysql listens on |
| `VDB_DB_NAME` | No | `appdb` | Database name exposed to clients |
| `VDB_PLUGIN_DIR` | No | `plugins` | Directory containing plugin subdirectories |
| `VDB_TLS_CERT_FILE` | No | — | PEM certificate path (enables TLS) |
| `VDB_TLS_KEY_FILE` | No | — | PEM private key path (required with cert) |

---

## Plugins

Plugins are standalone executables that vdb-mysql launches at startup. They attach handlers to pipeline points and subscribe to events — giving them the ability to transform rows, intercept queries, or observe writes.

Each plugin lives in its own subdirectory under `VDB_PLUGIN_DIR` and declares itself via a `manifest.json` or `manifest.yaml`.

→ [Plugin development guide](docs/plugins.md)  
→ [Pipelines and events reference](docs/pipelines-and-events.md)

---

## Further Reading

- [How vdb-mysql works](docs/how-it-works.md)
- [Installation](docs/installation.md)
- [TLS configuration](docs/tls.md)
- [Plugin development](docs/plugins.md)
- [Pipelines and events reference](docs/pipelines-and-events.md)
- [Known limitations](docs/known-limitations.md)

---

## Contributing

Public contributions are not yet open. Open a [GitHub Issue](https://github.com/virtual-db/vdb-mysql/issues) to report bugs, request features, or start a discussion.

See [CONTRIBUTING.md](CONTRIBUTING.md) for repository structure and where different concerns live.

---

## License

Elastic License 2.0. See [LICENSE.md](LICENSE.md).

You can run vdb-mysql for any purpose, including commercially. You may not offer it as a hosted or managed service to third parties.

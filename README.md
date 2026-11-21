# vdb-mysql

VDB for MySQL: a MySQL-compatible database environment over a read-only Source. Clients connect to a stock
`mysqld` 8.4 that loads the VDB plug-ins (`vdb.so`, from `vdb-mysql-plugins`); every table merges the Source's rows
with the environment's own changes, the Delta, kept in memory by `vdb-core`. The Source is never written.

This repository assembles VDB for MySQL and is the only one that publishes it (D-91):

| Part | What it is |
|---|---|
| `main.go` | Wires `vdb-core` and `vdb-mysql-engine` (the engine: supervision, the plug-in protocol, the Source adapter, identity, discovery) |
| `Dockerfile` | Builds MySQL 8.4 and `vdb.so` from source (`vdb-mysql-plugins` at a pinned ref) and `vdb-mysql`, into the image |
| `oracle/` | The oracle and the safety checker; a tag runs them against the image it built, and publishes only if they pass |

## Run

```sh
docker run -p 3306:3306 \
  -e VDB_SOURCE_HOST=db.example.com -e VDB_SOURCE_USER=vdb_svc -e VDB_SOURCE_PASSWORD=... \
  ghcr.io/virtual-db/vdb-mysql:<version>
```

Log in with your Source credentials. The port opens when the environment is complete, so a port check is the
readiness probe. Settings are in `docs/configuration.md`; the service account's grants in `docs/installation.md`.

## Documentation

| Page | Covers |
|---|---|
| `docs/installation.md` | The image, the service account and its grants |
| `docs/configuration.md` | Every `VDB_*` setting |
| `docs/tls.md` | TLS to the Source with nothing to set up; pinning a self-signed Source |
| `docs/known-limitations.md` | What VDB does differently from InnoDB, and why |

## Development

`make image-local` builds the image and the reference from `../vdb-mysql-plugins`; `make oracle` runs the oracle
against them (`oracle/run.sh`). How the engine works is in `vdb-mysql-engine/docs/`. Detailed suites live in
`vdb-tests`. Licence: Elastic License 2.0 (`LICENSE.md`).

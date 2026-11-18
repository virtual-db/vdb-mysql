# vdb-mysql

VDB for MySQL: a MySQL-compatible database environment over a read-only Source. Clients connect to a stock
`mysqld` 8.4 that loads the VDB plug-ins (`vdb.so`, from `vdb-mysql-plugins`); every table merges the Source's rows
with the environment's own changes, the Delta, kept in memory by `vdb-core`. The Source is never written.

`vdb-mysql` is the Go process in the environment image: it supervises `mysqld`, serves the plug-ins over the plug-in
protocol (`protocol/`), reads the Source through one service account over verified TLS, relays logins to the Source,
mirrors its accounts and privileges, and rebuilds `mysqld`'s data directory at every start.

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
| `docs/how-it-works.md` | Processes, the merge, transactions, logins, rebuilds |
| `docs/installation.md` | The image, the service account and its grants |
| `docs/configuration.md` | Every `VDB_*` setting |
| `docs/tls.md` | TLS to the Source with nothing to set up; pinning a self-signed Source |
| `docs/vdb-schema.md` | The `vdb` schema's tables |
| `docs/pipelines-and-events.md`, `docs/plugins.md` | Extending VDB through `vdb-core` |
| `docs/known-limitations.md` | What VDB does differently from InnoDB, and why |

## Development

`make test`, `make lint`, `make proto` (regenerates `protocol/vdb.pb.go` in a container). Integration suites live in
`vdb-tests`. Licence: Elastic License 2.0 (`LICENSE.md`).

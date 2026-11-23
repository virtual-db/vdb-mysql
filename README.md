# vdb-mysql

VDB for MySQL: a MySQL-compatible database environment over a read-only Source. Clients connect to a stock
`mysqld` 8.4 that loads the VDB plug-ins (`vdb.so`, from `vdb-mysql-plugins`); every table merges the Source's rows
with the environment's own changes, the Delta, kept in memory by `vdb-core`. The Source is never written.

This repository assembles VDB for MySQL and is the only one that publishes it (D-91):

| Part | What it is |
|---|---|
| `main.go` | Wires `vdb-core` and `vdb-mysql-engine` (the engine: supervision, the plug-in protocol, the Source adapter, identity, discovery) |
| `Dockerfile` | Builds MySQL 8.4 and `vdb.so` from source (`vdb-mysql-plugins` at the tag in `PLUGINS_REF`, and the MySQL release it names) and `vdb-mysql`, into the image |
| `oracle/` | The oracle and the safety checker; every release runs them against the exact image it publishes |

## Run

```sh
docker run -p 3306:3306 \
  -e VDB_SOURCE_HOST=db.example.com -e VDB_SOURCE_USER=vdb_svc -e VDB_SOURCE_PASSWORD=... \
  ghcr.io/virtual-db/vdb-mysql:<version>
```

Log in with your Source credentials. The port opens when the environment is complete, so a port check is the
readiness probe. Settings are in `docs/configuration.md`; the service account's grants in `docs/installation.md`.

Images are published for `linux/amd64` and `linux/arm64` as `ghcr.io/virtual-db/vdb-mysql:vX.Y.Z`, plus `vX.Y` and
`latest` for stable releases.

## Releases

Versions are plain git tags `vX.Y.Z` (and `oracle/vX.Y.Z` for the oracle module on the same commit); a tag starts
nothing. Publishing a GitHub release for a tag runs `.github/workflows/release.yml`:

1. **Oracle (amd64)** and **Oracle (arm64)**, in parallel on native runners: build the production image once, push it
   by digest only (untagged), build the reference, and run the oracle against that digest.
2. **Publish**, only if both pass: tag the two tested digests as one multi-architecture image. Nothing is rebuilt.

The `mysqld` layers come from the build cache in `ghcr.io/virtual-db/vdb-mysql-buildcache` (one tag per
architecture), so a release rebuilds only what changed. CI (`ci.yml`) runs Lint, Test and an `amd64` image build on
pull requests.

## Oracle

The oracle generates schemas and statements from a seed and runs each on VDB and on the reference (the same `mysqld`
without `vdb.so`, loaded with the same data), comparing results, errors and the tables after each write. It
runs three modes (single session; through the Source's own writes; three sessions at different isolation levels
interleaved), replays every case in `oracle/regressions/` first, and ends with the safety checker over the Source's
general log, which fails if VDB sent the Source anything but its read-only shapes. Releases run the seeds in
`oracle/seeds.txt`; a mismatch writes a minimal case to `oracle/out/`.

## Documentation

| Page | Covers |
|---|---|
| `docs/installation.md` | The image, the service account and its grants |
| `docs/configuration.md` | Every `VDB_*` setting |
| `docs/tls.md` | TLS to the Source with nothing to set up; pinning a self-signed Source |
| `docs/known-limitations.md` | What VDB does differently from InnoDB, and why |

## Development

`make image` builds the image and the reference from the pinned plug-ins tag, `make image-local` from
`../vdb-mysql-plugins`; `make oracle` runs the oracle against them (`oracle/run.sh`, ports 13306 to 13308).
A cold build compiles `mysqld` and takes a while; later builds reuse Docker's cache. Requires Go 1.27.4 and Docker. How the engine works is in `vdb-mysql-engine/docs/`. Detailed suites live in
`vdb-tests`. Licence: Elastic License 2.0 (`LICENSE.md`).

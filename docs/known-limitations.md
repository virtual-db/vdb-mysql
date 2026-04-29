# Known Limitations

VDB is in active early-alpha development. This page is an honest accounting of the current boundaries so you can make an informed decision before building on it.

---

## Writes are not forwarded to the source

All `INSERT`, `UPDATE`, and `DELETE` statements are captured into VDB's in-memory delta store. They are reflected back to the querying client and remain visible to subsequent reads within the same process lifetime — **but they never reach the source database**.

This is a deliberate architectural choice, not a bug. VDB is designed for use cases like data virtualization, test data overlays, and read-model construction where you *want* a separation between what the application sees and what the source contains.

If your use case requires writes to flow through to the source (e.g. transparent proxying, auditing with persistence, or read/write splitting), VDB is not the right tool yet. Persisted write-through is on the roadmap.

---

## The delta is in-memory and process-scoped

The delta does not persist across restarts. All captured writes are lost when the process exits. There is currently no snapshot, replay, or export mechanism.

---

## SQL compatibility is bounded by go-mysql-server

VDB uses [go-mysql-server](https://github.com/dolthub/go-mysql-server) as its SQL engine. It covers a broad subset of MySQL 8 SQL, but it is not a complete implementation. You may encounter differences in:

- Stored procedures and complex user-defined functions
- Certain window functions or advanced analytical queries
- Edge cases in type coercion and implicit conversions
- `INFORMATION_SCHEMA` completeness

If a query works against MySQL directly but not against vdb-mysql, check the go-mysql-server issue tracker before filing a VDB bug.

---

## Only a single exposed database

vdb-mysql exposes one database name to clients, set via `VDB_DB_NAME`. Multi-database routing within a single vdb-mysql instance is not currently supported.

---

## TCP only — no Unix socket

vdb-mysql accepts only TCP connections. Unix domain socket connections are not supported.

---

## Prepared statement parameter metadata

`COM_STMT_PREPARE` returns no column or parameter metadata. Clients that rely on this metadata (e.g. to pre-allocate result buffers or validate parameter types before execution) may behave unexpectedly.

---

## Pre-built binary is linux/amd64 only

The binary attached to GitHub releases targets `linux/amd64`. Building for other platforms — including macOS (`darwin/arm64`, `darwin/amd64`) and `linux/arm64` — requires compiling from source. See the [README](../README.md) for build instructions.

---

## Alpha stability — interfaces may change

All of the following are subject to change before a stable v1 release:

- Environment variable names
- The JSON-RPC plugin protocol and message shapes
- Pipeline names, point names, and point ordering
- Event names and payload schemas

Pin to a specific release tag rather than `latest` until the project reaches a stable release.
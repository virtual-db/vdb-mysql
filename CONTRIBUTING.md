# Contributing to vdb-mysql

> [!IMPORTANT]
> ### Public Contributions Are Not Yet Open
>
> VirtualDB is not currently accepting public pull requests. Please open an issue instead.

## What belongs here

Everything MySQL-specific in Go: the plug-in protocol (`protocol/`, public), the Source adapter, pushdown,
statistics, identity, discovery, schema polling, the supervisor and rebuild, the `vdb` schema. Database-agnostic logic
(the Delta, merge, transactions, locks, foreign keys) belongs in `vdb-core`; C++ belongs in `vdb-mysql-plugins`.

## Rules

- One package per concern under `internal/`; unit tests in `internal/<pkg>/tests/` need no database and no network.
- New direct dependencies need BMO's approval.
- `log/slog` with the `pkg` field; never log a password or a row value.
- `gofmt`, `go vet` and `go test -race ./...` are clean; `make proto` leaves no diff.

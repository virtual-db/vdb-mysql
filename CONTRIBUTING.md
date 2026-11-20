# Contributing to vdb-mysql

> [!IMPORTANT]
> ### Public Contributions Are Not Yet Open
>
> VirtualDB is not currently accepting public pull requests. Please open an issue instead.

## What belongs here

The assembly of VDB for MySQL: `main.go`, which only wires `vdb-core` and `vdb-mysql-engine`; the `Dockerfile`; the
oracle and the safety checker (`oracle/`); the image releases. Engine code belongs in `vdb-mysql-engine`, the
database-agnostic core in `vdb-core`, C++ in `vdb-mysql-plugins`.

## Rules

- `main.go` stays wiring only: configuration, `core.New`, `engine.NewDriver`, `UseDriver`, `Run`.
- New direct dependencies need BMO's approval.
- `log/slog` with the `pkg` field; never log a password or a row value.
- `gofmt` and `go vet` are clean; the oracle passes against the image (`make image-local oracle`).

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

- `main.go` stays wiring only: configuration, `core.New`, `engine.NewDriver`, `UseDriver`, `Run`, plus the image
  build's `check-collations` subcommand.
- New direct dependencies need BMO's approval.
- `log/slog` with the `pkg` field; never log a password or a row value.
- `gofmt` and `go vet` are clean; the oracle passes against the image (`make image-local oracle`).


## A new mysqld release

The image build fails when vdb-mysql-engine's `internal/collations/proven.yaml` has no entry for the image's mysqld
release (the `collations` stage of the `Dockerfile`, `vdb-mysql check-collations <release>`). Without that entry
every non-binary string index would read whole tables. So before a release is added to vdb-mysql-plugins'
`build/mysql-releases.txt`, or made the default:

1. Run the collation proof suite for it in vdb-tests (`runner collations`, see its README).
2. Add the result to `proven.yaml` by pull request to vdb-mysql-engine, with the raw output under vdb-tests'
   `results/collations/<release>/`.
3. Update this repository's `go.mod` to that engine version.

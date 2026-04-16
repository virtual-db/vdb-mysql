# Contributing to vdb-mysql

Thank you for your interest in contributing. This document covers how to submit changes, what is expected of contributors, and how to work with the local development environment.

---

## Contributor License Agreement

Before any contribution can be merged, you must sign the Anqor Contributor License Agreement (CLA). The CLA grants Anqor the rights necessary to distribute your contribution under the Elastic License 2.0 and any future license versions without requiring further permission from you.

**You must read and agree to [CLA.md](CLA.md) before submitting a pull request.**

If you are contributing on behalf of an employer or other legal entity, an authorized representative of that entity must also sign the CLA.

---

## What This Repository Is

`vdb-mysql` is the runnable server binary. It wires together `vdb-core` and `vdb-mysql-driver` into a deployable process. The logic for how data is intercepted and transformed lives in those two downstream modules, not here.

Contributions to this repository are narrow in scope:

- Changes to the `main.go` entry point (environment variable handling, startup wiring).
- Dockerfile or CI workflow improvements.
- Documentation corrections.

If you want to contribute to query interception, row transformation, authentication, schema handling, the plugin protocol, or the delta layer, the correct repositories are:

| Area | Repository |
|---|---|
| MySQL protocol, schema, rows, auth proxy | [vdb-mysql-driver](https://github.com/AnqorDX/vdb-mysql-driver) |
| Delta layer, plugin protocol, framework pipelines | [vdb-core](https://github.com/AnqorDX/vdb-core) |
| Integration test suite | [vdb-tests](https://github.com/AnqorDX/vdb-tests) |

---

## Development Setup

**Requirements**

- Go 1.23.3 or later.
- All sibling repositories cloned into the same parent directory (see [Building from Source](README.md#building-from-source)).

**Build and run locally**

```
# From the parent directory:
go work init ./dispatch ./pipeline ./vdb-core ./vdb-mysql-driver ./vdb-mysql

cd vdb-mysql
CGO_ENABLED=0 go build -trimpath -o vdb-mysql .

export VDB_SOURCE_DSN="user:pass@tcp(localhost:3306)/mydb"
export VDB_AUTH_SOURCE_ADDR="localhost:3306"
export VDB_DB_NAME="mydb"
./vdb-mysql
```

**Run tests**

```
go test -race ./...
```

---

## Submitting a Pull Request

1. Fork the repository and create a branch from `main`.
2. Make your changes.
3. Run `go test -race ./...` and confirm all tests pass.
4. Run `go vet ./...`.
5. Open a pull request against `main`. Describe what the change does and why.
6. Sign the CLA if prompted. The CLA bot will comment with instructions if you have not signed yet.

Pull requests that do not pass tests, do not have a description, or are missing CLA sign-off will not be reviewed until those issues are resolved.

---

## Reporting Bugs

Open an issue. Include:

- Go version and OS.
- The MySQL source version you are connecting to.
- The vdb-mysql binary version (from the release tag or `git describe`).
- Relevant environment variable values (with credentials redacted).
- Observed vs. expected behaviour, with any relevant log output.

---

## Security Issues

Do not open a public issue for security vulnerabilities. Email the maintainers directly. You will receive a response within 5 business days.

---

## License

By contributing to this repository, you agree that your contributions will be licensed under the Elastic License 2.0. See [LICENSE.md](LICENSE.md) and [CLA.md](CLA.md).

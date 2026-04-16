# vdb-mysql

Top-level composition root for VirtualDB MySQL. Wires `vdb-core` and `vdb-mysql-driver` into a runnable MySQL proxy.

Imports `github.com/AnqorDX/vdb-core` and `github.com/AnqorDX/vdb-mysql-driver`. Does **not** directly import go-mysql-server (GMS) — all GMS types are encapsulated by the driver.

## Module ecosystem

```
vdb-mysql  ← this module
  ├── github.com/AnqorDX/vdb-core
  └── github.com/AnqorDX/vdb-mysql-driver
        └── github.com/AnqorDX/vdb-core
```

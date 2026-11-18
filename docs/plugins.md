# Plugins

VDB plugins are out-of-process programs in `VDB_PLUGIN_DIR`, launched by `vdb-core` and connected over JSON-RPC
(`vdb-core/docs/plugins.md`). None ship in v1. A plugin that transforms column values (masking) declares them in its
`declare` notification as `transforms: ["db.table.column", ...]`; VDB then never pushes conditions on those columns to
the Source and reads their indexes unordered. A persistence plugin attaches to `vdb.transaction.commit.apply` below
priority 10 and returns change sets at `vdb.server.start.configure`.

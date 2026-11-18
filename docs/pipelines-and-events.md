# Pipelines and events

VDB's extension points are `vdb-core`'s pipelines and events (`vdb-core/docs/pipelines.md`, `events.md`). In VDB for
MySQL they run at these moments:

| Pipeline or event | When |
|---|---|
| `vdb.connection.opened`, `.closed` | A client connects to or disconnects from `mysqld` |
| `vdb.query.received` | Before a statement is parsed; synchronous only when a handler is attached to `intercept` |
| `vdb.transaction.begin`, `.commit`, `.rollback` | A transaction starts, commits (with its change set) or rolls back |
| `vdb.records.source`, `vdb.records.merged` | Each batch of Source rows, and each batch leaving the core |
| `vdb.write.insert`, `.update`, `.delete`, `.truncate` | Each write |
| `vdb.query.completed` | After each statement |
| `vdb.source.requested`, `vdb.limit.reached`, `vdb.snapshot.expired`, `vdb.key.conflict` | A Source request; a memory or lock limit refusal; an expired snapshot; a Source row hidden by an equal Delta key |
| `vdb.schema.loaded`, `vdb.schema.invalidated` | A table discovered; a table changed by the Source or a client |

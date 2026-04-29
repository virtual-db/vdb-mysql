# Pipelines & Events Reference

VDB's extension model is built on two primitives: **pipelines** and **events**.

A **pipeline** is an ordered sequence of named points. When something happens — a query arrives, a row is fetched, a transaction commits — VDB runs the corresponding pipeline. Plugin handlers attach to specific points within a pipeline and are called in priority order. A handler may inspect or modify the payload before passing it along.

An **event** is a fire-and-forget notification emitted after a pipeline completes. Events are for observation, not transformation. A plugin subscribes to an event to react to something that already happened without blocking the path that caused it.

---

## Pipelines

### `vdb.context.create`

Fired once at startup, before any connection is accepted. Used to build and seal the global context shared across all sessions.

| Point | Description |
|---|---|
| `vdb.context.create.build_context` | Allocates the initial global context object. |
| `vdb.context.create.contribute` | Allows handlers to add values to the context before it is sealed. |
| `vdb.context.create.seal` | Seals the context. No further contributions are accepted after this point. |
| `vdb.context.create.emit` | Fires any startup-time events. |

---

### `vdb.server.start`

Fired after the global context is sealed, just before the server binds its port.

| Point | Description |
|---|---|
| `vdb.server.start.build_context` | Builds the context for the start sequence. |
| `vdb.server.start.configure` | Applies final server configuration. |
| `vdb.server.start.launch` | Starts the server goroutine and binds the TCP listener. |
| `vdb.server.start.emit` | Emits the server-started notification. |

---

### `vdb.server.stop`

Fired when VDB receives a shutdown signal (`SIGTERM` or `SIGINT`) or when `Stop()` is called programmatically.

| Point | Description |
|---|---|
| `vdb.server.stop.build_context` | Builds the context for the stop sequence. |
| `vdb.server.stop.drain` | Waits for in-flight requests to complete. |
| `vdb.server.stop.halt` | Closes the listener and terminates the server. |
| `vdb.server.stop.emit` | Emits `vdb.server.stopped`. |

---

### `vdb.connection.opened`

Fired after a client completes the MySQL auth handshake.

| Point | Description |
|---|---|
| `vdb.connection.opened.build_context` | Builds the per-connection context, including the authenticated user and remote address. |
| `vdb.connection.opened.accept` | Gate point. A handler returning an error refuses the connection. |
| `vdb.connection.opened.track` | Registers the connection in the framework's connection state map. |
| `vdb.connection.opened.emit` | Emits `vdb.connection.opened`. |

---

### `vdb.connection.closed`

Fired when a client disconnects, regardless of the reason.

| Point | Description |
|---|---|
| `vdb.connection.closed.build_context` | Builds the context for the teardown sequence. |
| `vdb.connection.closed.cleanup` | Discards any in-progress transaction delta for this connection. |
| `vdb.connection.closed.release` | Removes the connection from the state map. |
| `vdb.connection.closed.emit` | Emits `vdb.connection.closed`. |

---

### `vdb.transaction.begin`

Fired when a transaction starts, either via an explicit `BEGIN` / `START TRANSACTION` or implicitly by the SQL engine wrapping a single statement in autocommit mode.

| Point | Description |
|---|---|
| `vdb.transaction.begin.build_context` | Builds the context, including whether the transaction is read-only. |
| `vdb.transaction.begin.authorize` | Gate point. A handler returning an error refuses the transaction. |
| `vdb.transaction.begin.emit` | Emits `vdb.transaction.started`. |

---

### `vdb.transaction.commit`

Fired when a transaction is committed.

| Point | Description |
|---|---|
| `vdb.transaction.commit.build_context` | Builds the context for the commit sequence. |
| `vdb.transaction.commit.apply` | Merges the transaction's private delta into the live session delta. |
| `vdb.transaction.commit.emit` | Emits `vdb.transaction.committed`. |

---

### `vdb.transaction.rollback`

Fired when a transaction is rolled back, either fully or to a named savepoint.

| Point | Description |
|---|---|
| `vdb.transaction.rollback.build_context` | Builds the context, including the savepoint name (empty string for a full rollback). |
| `vdb.transaction.rollback.apply` | Discards the transaction's private delta. |
| `vdb.transaction.rollback.emit` | Emits `vdb.transaction.rolledback`. |

---

### `vdb.query.received`

Fired when a SQL statement is received, before the SQL engine executes it. This is the primary interception point for query rewriting.

| Point | Description |
|---|---|
| `vdb.query.received.build_context` | Builds the context, including the raw SQL string and the current database name. |
| `vdb.query.received.intercept` | Transformation point. A handler may return a modified SQL string; VDB executes the replacement instead of the original. |
| `vdb.query.received.emit` | Emits nothing directly — `vdb.query.completed` is emitted after execution finishes. |

---

### `vdb.records.source`

Fired after rows are fetched from the source database, before the delta overlay is applied. This is where you can add, remove, or reshape rows as they arrive from the source.

| Point | Description |
|---|---|
| `vdb.records.source.build_context` | Builds the context, including the table name and the raw `[]map[string]any` record slice. |
| `vdb.records.source.transform` | Transformation point. Handlers may return a modified record slice. |
| `vdb.records.source.emit` | Notifies subscribers that source records are available. |

---

### `vdb.records.merged`

Fired after the delta has been overlaid on the source rows, producing the final record set that will be returned to the client. This is the last opportunity to modify rows before they leave VDB.

| Point | Description |
|---|---|
| `vdb.records.merged.build_context` | Builds the context with the post-merge record slice. |
| `vdb.records.merged.transform` | Transformation point. Handlers may make final adjustments to the outgoing records. |
| `vdb.records.merged.emit` | Notifies subscribers that the merged records are ready. |

---

### `vdb.write.insert`

Fired once per row when an `INSERT` statement is processed.

| Point | Description |
|---|---|
| `vdb.write.insert.build_context` | Builds the context with the table name and the new row as `map[string]any`. |
| `vdb.write.insert.apply` | Records the row as a net-new insert in the delta. Handlers at this point may modify the record before it is stored. |
| `vdb.write.insert.emit` | Emits `vdb.record.inserted`. |

---

### `vdb.write.update`

Fired once per row when an `UPDATE` statement is processed.

| Point | Description |
|---|---|
| `vdb.write.update.build_context` | Builds the context with the table name and both the old and new row as `map[string]any`. |
| `vdb.write.update.apply` | Records the update overlay in the delta, preserving the stable source key through key-change chains. Handlers may modify the new record before it is stored. |
| `vdb.write.update.emit` | Emits `vdb.record.updated`. |

---

### `vdb.write.delete`

Fired once per row when a `DELETE` statement is processed.

| Point | Description |
|---|---|
| `vdb.write.delete.build_context` | Builds the context with the table name and the deleted row as `map[string]any`. |
| `vdb.write.delete.apply` | Records a tombstone in the delta for the row, removing any prior update overlay for the same source key. |
| `vdb.write.delete.emit` | Emits `vdb.record.deleted`. |

---

## Events

Events are emitted after their corresponding pipeline completes. Subscribe to an event when you need to react to state changes without blocking the request path.

| Event | Emitted After |
|---|---|
| `vdb.server.stopped` | The server has fully shut down and the port has been released. |
| `vdb.connection.opened` | A client connection has been accepted and tracked. |
| `vdb.connection.closed` | A client connection has been released from the state map. |
| `vdb.transaction.started` | A transaction has been authorized and begun. |
| `vdb.transaction.committed` | A transaction has been committed and its delta merged into the live store. |
| `vdb.transaction.rolledback` | A transaction has been rolled back (full or to a savepoint). |
| `vdb.query.completed` | A query has finished executing. Payload includes the SQL string, rows affected, and any error. |
| `vdb.record.inserted` | A row has been stored in the delta as an insert. |
| `vdb.record.updated` | An update overlay has been recorded in the delta. |
| `vdb.record.deleted` | A tombstone has been recorded in the delta. |
| `vdb.schema.loaded` | Schema metadata for a table has been loaded from the source `INFORMATION_SCHEMA`. Payload includes the table name, column list, and primary key column. |
| `vdb.schema.invalidated` | Schema metadata for a table has been invalidated and will be reloaded on next access. |

---

## Handler Priority

When multiple handlers are registered at the same pipeline point, they are called in ascending priority order (lower numbers run first). Priorities do not need to be unique or contiguous — any integer is valid. Built-in framework handlers occupy reserved priority ranges; consult the [vdb-core](https://github.com/virtual-db/core) documentation for reserved values.

---

## Plugin-Declared Pipelines and Events

Plugins are not limited to the 14 standard pipelines. A plugin may declare its own pipelines and events in its `declare` notification, making them available for other plugins to attach to or subscribe to. See the [Plugin development guide](./plugins.md) for details on the `declare` notification and the full JSON-RPC protocol.
# Plugins

Plugins are standalone executables that extend vdb-mysql. They run as child
processes and communicate with the framework over a Unix socket using
JSON-RPC 2.0.

---

## Directory Layout

Set `VDB_PLUGIN_DIR` to a directory. vdb-mysql scans it one level deep and
launches every subdirectory that contains a manifest file.

```
/etc/vdb/plugins/
  my-plugin/
    manifest.json
    my-plugin          ← the plugin executable
  another-plugin/
    manifest.yaml
    another-plugin
```

---

## Manifest

Each plugin subdirectory must contain a manifest named `manifest.json`,
`manifest.yaml`, or `manifest.yml`.

```json
{
  "name":    "my-plugin",
  "version": "1.0.0",
  "command": ["./my-plugin"],
  "env": {
    "MY_PLUGIN_API_KEY": "..."
  }
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | No | Identifier used in logs. Defaults to the subdirectory name. |
| `version` | string | No | Informational. Printed at startup. |
| `command` | []string | **Yes** | Command and arguments to launch the plugin. The working directory is set to the plugin's subdirectory. |
| `env` | object | No | Extra environment variables passed to the plugin process. |

---

## Startup Handshake

When vdb-mysql starts:

1. It launches each plugin subprocess.
2. It passes the Unix socket path to the plugin via the `VDB_SOCKET` environment variable.
3. The plugin connects to the socket and sends a `declare` JSON-RPC 2.0 notification.
4. vdb-mysql waits up to **10 seconds** for the plugin to connect to the socket. Once connected, the `declare` notification is read immediately with no separate timeout. Plugins that fail to connect within the deadline are logged and skipped — the server continues without them.

### The `declare` notification

```json
{
  "jsonrpc": "2.0",
  "method": "declare",
  "params": {
    "plugin_id": "my-plugin",
    "pipeline_handlers": [
      { "point": "vdb.records.source.transform", "priority": 10 }
    ],
    "event_subscriptions": [
      "vdb.query.completed"
    ],
    "event_declarations": [
      "my-plugin.row.flagged"
    ],
    "pipeline_declarations": []
  }
}
```

| Field | Description |
|---|---|
| `plugin_id` | The plugin's identifier (informational). |
| `pipeline_handlers` | Points the plugin handles, each with a numeric priority. Lower runs first. |
| `event_subscriptions` | Events the plugin wants to receive as fire-and-forget notifications. |
| `event_declarations` | Events this plugin will emit. Must be declared so other components can subscribe before the server starts. |
| `pipeline_declarations` | Custom pipelines the plugin owns. Other plugins can attach handlers to these. |

---

## Inbound Calls from vdb-mysql

Once declared, vdb-mysql will call the plugin for each registered point or
subscribed event.

### `handle_pipeline_point`

Called synchronously when the pipeline reaches a point the plugin registered.
The plugin's response payload replaces the pipeline's running payload.

```json
{
  "jsonrpc": "2.0",
  "method": "handle_pipeline_point",
  "id": 1,
  "params": {
    "point": "vdb.records.source.transform",
    "payload": { ... }
  }
}
```

Expected response:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "payload": { ... }
  }
}
```

### `handle_event`

Delivered as a fire-and-forget notification (no `id`, no response expected).

```json
{
  "jsonrpc": "2.0",
  "method": "handle_event",
  "params": {
    "event": "vdb.query.completed",
    "payload": { ... }
  }
}
```

### `shutdown`

Sent when vdb-mysql is shutting down. The plugin **must send a JSON-RPC response** to acknowledge the request, then clean up and exit. vdb-mysql waits up to 10 seconds for the response; if no response arrives the process is killed. After a response is received, vdb-mysql waits up to a further 10 seconds for the process to exit before sending SIGKILL.

```json
{
  "jsonrpc": "2.0",
  "method": "shutdown",
  "id": 42,
  "params": {}
}
```

---

## Outbound Calls from the Plugin

### `emit_event`

A plugin can emit an event it previously declared in its `declare` notification.

```json
{
  "jsonrpc": "2.0",
  "method": "emit_event",
  "id": 1,
  "params": {
    "event": "my-plugin.row.flagged",
    "payload": { ... }
  }
}
```

vdb-mysql acks with an empty result, then forwards the event to all subscribers.
Attempting to emit an undeclared event is logged and silently dropped.

---

## Error Handling

- If a pipeline point handler returns a JSON-RPC error, the pipeline stops and
  the error is returned to the client as a SQL error.
- If a plugin process exits unexpectedly, vdb-mysql logs the exit and continues
  operating without that plugin. There is no automatic restart.
- Event handler errors are logged but do not affect other subscribers.

---

## Further Reading

Full plugin SDK documentation and reference implementations are maintained in
the [vdb-core](https://github.com/virtual-db/core) repository.
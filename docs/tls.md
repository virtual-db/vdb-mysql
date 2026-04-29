# TLS Configuration

vdb-mysql supports TLS on its MySQL listener. TLS is optional and is enabled
entirely through environment variables — no configuration files are involved.

---

## Enabling TLS

Set both variables before starting vdb-mysql:

```sh
export VDB_TLS_CERT_FILE=/etc/vdb/server.crt
export VDB_TLS_KEY_FILE=/etc/vdb/server.key
./vdb-mysql
```

Both must be set together. Setting `VDB_TLS_CERT_FILE` without
`VDB_TLS_KEY_FILE` causes vdb-mysql to exit with a fatal error at startup.
Setting `VDB_TLS_KEY_FILE` alone has no effect — TLS is only enabled when the
certificate file is present.

---

## What changes when TLS is enabled

| Behaviour | TLS enabled | TLS disabled |
|---|---|---|
| Auth plugin advertised to clients | `caching_sha2_password` | `mysql_clear_password` |
| Minimum TLS version | 1.2 | N/A |
| SHA2 fast-auth (cache-hit) path | Permanently disabled — see below | N/A |
| `AllowClearTextWithoutTLS` on listener | Not set | Set to `true` |

### `caching_sha2_password` and the forced full-auth path

When TLS is enabled, vdb-mysql advertises `caching_sha2_password`. However,
the SHA2 fast-auth cache-hit path is **permanently disabled** by design.
Every connection — regardless of whether the client has connected before —
is forced through the full-auth exchange.

This is intentional. vdb-mysql holds no local password store or hash cache.
The only way to verify credentials is to open a short-lived probe connection
to the source MySQL database, which requires the client's plaintext password.
The full-auth path is the only way Vitess will deliver that plaintext password
to the auth layer. TLS on the listener ensures the password is transmitted
encrypted between the client and vdb-mysql.

### Without TLS: `mysql_clear_password`

When TLS is not configured, vdb-mysql advertises `mysql_clear_password` and
sets `AllowClearTextWithoutTLS` on the listener. The plaintext password is
delivered directly to the same auth probe mechanism described above.

**Important:** most MySQL clients refuse to use `mysql_clear_password` by
default, because it sends the password in plaintext. `AllowClearTextWithoutTLS`
only means vdb-mysql will *accept* cleartext auth — it does not force the
client to send it. You must explicitly enable cleartext auth on every client
that connects to vdb-mysql. See [Connecting without TLS](#connecting-without-tls)
below for per-client instructions.

This mode is appropriate for local development or environments where transport
security is provided at the infrastructure level (private VPC, mTLS service
mesh, etc.) — and where you can control client configuration.

---

## Scope of TLS configuration

`VDB_TLS_CERT_FILE` and `VDB_TLS_KEY_FILE` configure TLS on the **listener**
only — that is, the connection between your application and vdb-mysql. They
have no effect on the connections vdb-mysql opens to the source database, where
vdb-mysql is simply a MySQL client. TLS on that leg is governed entirely by
your source database's requirements.

---


## Certificate requirements

- Both the certificate and the key must be PEM-encoded.
- The certificate file may contain the full chain (leaf + intermediates in
  order).
- The private key must correspond to the leaf certificate's public key.
- The minimum TLS version accepted from clients is **1.2**.
- vdb-mysql loads the system certificate pool at startup. On minimal container
  images where the system pool is unavailable (e.g. `scratch` or `distroless`
  images without `ca-certificates` installed), vdb-mysql falls back to an empty
  pool and continues — it does not fail. Install `ca-certificates` in your
  image to avoid the empty pool.

Note: client certificate authentication is not currently supported. The system
certificate pool is loaded but has no active effect in this release.

---

## Generating a self-signed certificate (development)

For local development a self-signed certificate is sufficient. Most MySQL
clients default to verifying the server certificate; use `--ssl-mode=REQUIRED`
(which requires TLS but skips server-cert verification) to connect against a
self-signed cert without needing to distribute the CA.

```sh
openssl req -x509 -newkey rsa:4096 -nodes \
  -keyout server.key \
  -out server.crt \
  -days 365 \
  -subj "/CN=vdb-mysql" \
  -addext "subjectAltName=IP:127.0.0.1,DNS:localhost"
```

```sh
VDB_TLS_CERT_FILE=./server.crt \
VDB_TLS_KEY_FILE=./server.key \
./vdb-mysql
```

Connect with the MySQL CLI:

```sh
mysql -h 127.0.0.1 -P 3306 -u myuser -pmypassword myapp \
  --ssl-mode=REQUIRED
```

---

## Docker

Mount your certificate and key into the container and pass the paths via
environment variables:

```sh
docker run --rm \
  -e VDB_SOURCE_DSN="vdb_user:secret@tcp(db.internal:3306)/myapp" \
  -e VDB_AUTH_SOURCE_ADDR="db.internal:3306" \
  -e VDB_DB_NAME="myapp" \
  -e VDB_TLS_CERT_FILE=/run/secrets/vdb-tls-cert \
  -e VDB_TLS_KEY_FILE=/run/secrets/vdb-tls-key \
  -v /etc/vdb/server.crt:/run/secrets/vdb-tls-cert:ro \
  -v /etc/vdb/server.key:/run/secrets/vdb-tls-key:ro \
  -p 3306:3306 \
  ghcr.io/virtual-db/mysql:latest
```

With Docker Compose secrets:

```yaml
services:
  vdb:
    image: ghcr.io/virtual-db/mysql:latest
    restart: unless-stopped
    environment:
      VDB_SOURCE_DSN: "vdb_user:secret@tcp(db.internal:3306)/myapp"
      VDB_AUTH_SOURCE_ADDR: "db.internal:3306"
      VDB_DB_NAME: myapp
      VDB_TLS_CERT_FILE: /run/secrets/vdb_tls_cert
      VDB_TLS_KEY_FILE: /run/secrets/vdb_tls_key
    secrets:
      - vdb_tls_cert
      - vdb_tls_key
    ports:
      - "3306:3306"

secrets:
  vdb_tls_cert:
    file: ./certs/server.crt
  vdb_tls_key:
    file: ./certs/server.key
```

---

## Connecting without TLS

Without TLS, vdb-mysql advertises `mysql_clear_password`. Because most clients
refuse this plugin by default, you must enable it explicitly **in addition to**
disabling TLS on the client. Both steps are required.

### MySQL CLI

```sh
mysql -h 127.0.0.1 -P 3306 -u myuser -pmypassword myapp \
  --ssl-mode=DISABLED \
  --enable-cleartext-plugin
```

### Go (`go-sql-driver/mysql`)

Append both parameters to the DSN:

```
user:password@tcp(127.0.0.1:3306)/myapp?tls=false&allowCleartextPasswords=true
```

### Other clients

Consult your client's documentation for how to enable `mysql_clear_password`
(sometimes called "cleartext plugin" or "plain password auth"). Without this,
the client will reject the server's auth plugin advertisement and the
connection will fail at the handshake stage.

---

## Related

- [Configuration reference](./configuration.md)
- [Installation](./installation.md)
- [Known limitations](./known-limitations.md)
# Installation

Run the image `ghcr.io/virtual-db/vdb-mysql:<version>` with the Source settings of `configuration.md`. Mount a volume
at `/var/lib/vdb/tmp` to size `mysqld`'s temporary space (below).

## Temporary space: `/var/lib/vdb/tmp`

`mysqld` writes its temporary data to `/var/lib/vdb/tmp` (its `tmpdir`): sorts that do not fit in memory, internal
temporary tables for `GROUP BY`, `DISTINCT`, `UNION` and derived tables, and the files of `ALTER TABLE`. This is
MySQL's own behaviour, and VDB leaves it as is.

The folder can grow to about the size of the rows one statement sorts or groups. On a large Source that can be large:
an `ORDER BY` on a column without an index over a 50 GB table can write tens of GB here. It is emptied when the
statement ends.

Without a volume, the folder lives in the container's writable layer, on whatever disk Docker uses. Mount a volume
there to choose the disk and its size. The image runs as user `vdb` (uid 1000), which must be able to write to it.

Docker:

```sh
docker volume create vdb-tmp
docker run -d --name vdb -p 3306:3306 \
  -e VDB_SOURCE_HOST=db.example.com -e VDB_SOURCE_USER=vdb_svc -e VDB_SOURCE_PASSWORD_FILE=/run/secrets/vdb \
  -v vdb-tmp:/var/lib/vdb/tmp \
  ghcr.io/virtual-db/vdb-mysql:<version>
```

A host folder works too (`-v /mnt/fast-disk/vdb-tmp:/var/lib/vdb/tmp`), once `chown 1000 /mnt/fast-disk/vdb-tmp`.

Compose:

```yaml
services:
  vdb:
    image: ghcr.io/virtual-db/vdb-mysql:<version>
    ports: ["3306:3306"]
    environment:
      VDB_SOURCE_HOST: db.example.com
      VDB_SOURCE_USER: vdb_svc
      VDB_SOURCE_PASSWORD_FILE: /run/secrets/vdb
    volumes:
      - vdb-tmp:/var/lib/vdb/tmp
volumes:
  vdb-tmp:
```

When the disk is full, the statement that needs more space fails with MySQL's own error, for example
`ERROR 1114 (HY000): The table '/var/lib/vdb/tmp/#sql...' is full` or `Errcode: 28 - No space left on device`.
Other statements and the Delta are not affected. Give the volume more space, or add an index that lets the query
avoid the sort.

## The service account

VDB reads the Source with one account. It needs to read the exposed data and the grant tables, and nothing else:

```sql
CREATE USER 'vdb_svc'@'%' IDENTIFIED BY '...' REQUIRE SSL;
GRANT SELECT, SHOW VIEW ON app.* TO 'vdb_svc'@'%';
GRANT SELECT (User, Host, plugin, ssl_type, account_locked, password_expired, User_attributes, Process_priv,
  Select_priv, Insert_priv, Update_priv, Delete_priv, Create_priv, Drop_priv, Alter_priv, Index_priv,
  References_priv, Create_view_priv, Show_view_priv, Create_routine_priv, Alter_routine_priv, Execute_priv,
  Trigger_priv, Create_tmp_table_priv, Lock_tables_priv) ON mysql.user TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.db TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.tables_priv TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.columns_priv TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.procs_priv TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.role_edges TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.default_roles TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.global_grants TO 'vdb_svc'@'%';
GRANT SHOW_ROUTINE ON *.* TO 'vdb_svc'@'%';
```

The password hash column of `mysql.user` is never granted and never read. Without the grant-table reads, VDB learns
accounts at their first login instead (`vdb.accounts` says which mode is in use).

# Installation

Run the image `ghcr.io/virtual-db/vdb-mysql:<version>` with the Source settings of `configuration.md`. Mount a volume
at `/var/lib/vdb/tmp` to size `mysqld`'s temporary space.

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

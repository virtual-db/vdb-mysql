-- init/01-users.sql: Source accounts for the test environment (doc 15 §1).
--
--   test_admin  full access; the runner seeds the Source with it and logs in
--               to VDB with it (VDB relays the login to the Source)
--   vdb_svc     VDB's service account, with exactly the grants of product
--               doc 11 §6: read the exposed data and the grant tables, nothing else
--   app_ro      a read-only application account for identity tests
CREATE USER IF NOT EXISTS 'test_admin'@'%' IDENTIFIED WITH caching_sha2_password BY 'testadmin';
GRANT ALL PRIVILEGES ON *.* TO 'test_admin'@'%' WITH GRANT OPTION;

CREATE USER IF NOT EXISTS 'vdb_svc'@'%' IDENTIFIED WITH caching_sha2_password BY 'vdbsvc' REQUIRE SSL;
GRANT SELECT, SHOW VIEW ON testdb.* TO 'vdb_svc'@'%';
-- Granted columns of mysql.user only: never the password hash (product doc 11 §6).
GRANT SELECT (User, Host, plugin, ssl_type, account_locked, password_expired, User_attributes, Process_priv, Select_priv, Insert_priv, Update_priv, Delete_priv, Create_priv, Drop_priv, Alter_priv, Index_priv, References_priv, Create_view_priv, Show_view_priv, Create_routine_priv, Alter_routine_priv, Execute_priv, Trigger_priv, Create_tmp_table_priv, Lock_tables_priv) ON mysql.user TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.db TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.tables_priv TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.columns_priv TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.procs_priv TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.role_edges TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.default_roles TO 'vdb_svc'@'%';
GRANT SELECT ON mysql.global_grants TO 'vdb_svc'@'%';
GRANT SHOW_ROUTINE ON *.* TO 'vdb_svc'@'%';

CREATE USER IF NOT EXISTS 'app_ro'@'%' IDENTIFIED WITH caching_sha2_password BY 'appro';
GRANT SELECT ON testdb.* TO 'app_ro'@'%';

-- SYS-001 checks the service account cannot write.
CREATE USER IF NOT EXISTS 'vdb_user'@'%' IDENTIFIED WITH caching_sha2_password BY 'vdbuser';
GRANT SELECT ON testdb.* TO 'vdb_user'@'%';
FLUSH PRIVILEGES;

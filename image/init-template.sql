-- init-template.sql: run once while building the template data directory
-- (implementation plan doc 12 §3.1). It creates the control account, which
-- logs in through the Unix socket as OS user vdb, and removes every other
-- account mysqld's initialisation created.
-- auth_socket is installed here (persisted in mysql.plugin): --plugin-load-add is
-- not applied while --initialize runs.
INSTALL PLUGIN auth_socket SONAME 'auth_socket.so';
CREATE USER 'vdb_admin'@'localhost' IDENTIFIED WITH auth_socket AS 'vdb';
GRANT ALL PRIVILEGES ON *.* TO 'vdb_admin'@'localhost' WITH GRANT OPTION;
GRANT PROXY ON ''@'' TO 'vdb_admin'@'localhost' WITH GRANT OPTION;
DROP USER IF EXISTS 'root'@'localhost';

-- init-template.sql: run once while building the template data directory
-- (implementation plan doc 12 §3.1). It creates the control account, which
-- logs in through the Unix socket as OS user vdb, and removes every other
-- account mysqld's initialisation created.
CREATE USER 'vdb_admin'@'localhost' IDENTIFIED WITH auth_socket AS 'vdb';
GRANT ALL PRIVILEGES ON *.* TO 'vdb_admin'@'localhost' WITH GRANT OPTION;
GRANT PROXY ON ''@'' TO 'vdb_admin'@'localhost' WITH GRANT OPTION;
DROP USER IF EXISTS 'root'@'localhost';

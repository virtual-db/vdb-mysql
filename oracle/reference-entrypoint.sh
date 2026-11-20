#!/bin/sh
# entrypoint.sh: the InnoDB side of the oracle (doc 15 §2.1). The same mysqld
# build as VDB, without vdb.so, with one account for the runner.
set -e
D=/var/lib/vdb/reference
if [ ! -d "$D/mysql" ]; then
  cat > /tmp/init.sql <<SQL
CREATE USER 'test_admin'@'%' IDENTIFIED BY 'testadmin';
GRANT ALL PRIVILEGES ON *.* TO 'test_admin'@'%';
CREATE DATABASE IF NOT EXISTS testdb;
SQL
  /usr/local/mysql/bin/mysqld --no-defaults --initialize-insecure --datadir="$D" --init-file=/tmp/init.sql \
    --log-error=/tmp/init.err || { cat /tmp/init.err; exit 1; }
fi
exec /usr/local/mysql/bin/mysqld --no-defaults --datadir="$D" --port=3306 --bind-address=0.0.0.0 \
  --socket=/tmp/reference.sock --skip-log-bin --default-storage-engine=InnoDB --log-error=stderr \
  --mysql-native-password=ON

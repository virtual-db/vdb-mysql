#!/bin/sh
# gen.sh: a test CA and a server certificate for source-db (SAN source-db), so
# VDB verifies the Source with VERIFY_IDENTITY through its CA directory.
set -e
cd /certs
[ -f ca.pem ] && exit 0
apk add --no-cache openssl >/dev/null
openssl req -x509 -newkey rsa:2048 -nodes -keyout ca-key.pem -out ca.pem -days 3650 -subj "/CN=vdb-test-ca"
openssl req -newkey rsa:2048 -nodes -keyout server-key.pem -out server.csr -subj "/CN=source-db"
printf 'subjectAltName=DNS:source-db,DNS:localhost,IP:127.0.0.1\n' > ext.cnf
openssl x509 -req -in server.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial -out server-cert.pem -days 3650 -extfile ext.cnf
chmod 644 *.pem

#!/bin/sh
# run.sh: starts the oracle's environment with $VDB_IMAGE and $REFERENCE_IMAGE,
# runs the oracle from the host with the given flags, saves the logs to out/,
# and tears the environment down. The exit code is the oracle's.
set -e
cd "$(dirname "$0")"
: "${VDB_IMAGE:=vdb-mysql:dev}"
: "${REFERENCE_IMAGE:=vdb-mysql:reference}"
export VDB_IMAGE REFERENCE_IMAGE
P=vdb-oracle
docker compose -p $P down -v >/dev/null 2>&1 || true
mkdir -p out/source-log
chmod 777 out/source-log
docker compose -p $P up -d --wait
set +e
go run ./cmd/oracle -general-log out/source-log/general.log -out out "$@"
code=$?
docker compose -p $P logs --no-color > out/compose.log 2>&1
docker compose -p $P down -v >/dev/null 2>&1
exit $code

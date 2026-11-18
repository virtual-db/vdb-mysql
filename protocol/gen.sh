#!/bin/sh
# gen.sh: regenerates vdb.pb.go inside the golang image (make proto).
set -e
apt-get update -qq >/dev/null
apt-get install -y -qq protobuf-compiler >/dev/null
GOBIN=/tmp/bin go install "google.golang.org/protobuf/cmd/protoc-gen-go@$1"
PATH=/tmp/bin:$PATH protoc -I protocol --go_out=protocol --go_opt=paths=source_relative protocol/vdb.proto

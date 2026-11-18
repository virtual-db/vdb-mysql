# Makefile: developer targets for vdb-mysql.
GO_IMAGE ?= golang:1.26.9
PROTOC_GEN_GO ?= v1.36.12

.PHONY: proto test lint build image

# proto regenerates protocol/vdb.pb.go from protocol/vdb.proto in a container,
# so no local protoc is needed. CI fails if the result differs (doc 07 §5).
proto:
	docker run --rm -v "$(CURDIR)":/src -w /src $(GO_IMAGE) sh /src/protocol/gen.sh $(PROTOC_GEN_GO)

test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o vdb-mysql .

image:
	docker build -t vdb-mysql:dev .

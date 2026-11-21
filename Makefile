# Makefile: developer targets for vdb-mysql.
.PHONY: lint build image image-local oracle

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o vdb-mysql .

# image builds from the pinned vdb-mysql-plugins ref; image-local from ../vdb-mysql-plugins.
image:
	docker buildx build --target runtime --load -t vdb-mysql:dev .
	docker buildx build --target reference --load -t vdb-mysql:reference .

image-local:
	docker buildx build --build-context plugins-src=../vdb-mysql-plugins --target runtime --load -t vdb-mysql:dev .
	docker buildx build --build-context plugins-src=../vdb-mysql-plugins --target reference --load -t vdb-mysql:reference .

oracle:
	cd oracle && VDB_IMAGE=vdb-mysql:dev REFERENCE_IMAGE=vdb-mysql:reference ./run.sh

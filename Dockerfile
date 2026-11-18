# vdb-mysql: the environment image (implementation plan doc 14 §3).
#
#   go        build the vdb-mysql binary
#   mysql     mysqld and vdb.so from a pinned vdb-mysql-plugins image
#   template  the template data directory: mysqld's system schema and the
#             control account vdb_admin@localhost with auth_socket
#   runtime   oraclelinux:9-slim with mysqld, vdb-mysql, the template, the
#             CA bundles and the licences; runs as user vdb
#
# Build the debug variant with --build-arg PLUGINS_IMAGE=<...>-debug.

ARG PLUGINS_IMAGE=ghcr.io/virtual-db/vdb-mysql-plugins:8.4.11-v0.1.0
ARG BASE=oraclelinux:9-slim

FROM golang:1.26.9 AS go
WORKDIR /build
COPY go.mod go.sum ./
# Private modules (vdb-core, dispatch, pipeline) are fetched with the build
# secret gh_token when one is given: docker build --secret id=gh_token,env=TOKEN
ENV GOPRIVATE=github.com/AnqorDX/*,github.com/virtual-db/*
RUN --mount=type=secret,id=gh_token,required=false \
    if [ -s /run/secrets/gh_token ]; then \
      export GIT_CONFIG_COUNT=1 \
        GIT_CONFIG_KEY_0="url.https://x-access-token:$(cat /run/secrets/gh_token)@github.com/.insteadOf" \
        GIT_CONFIG_VALUE_0="https://github.com/"; \
    fi; \
    go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /vdb-mysql .

FROM ${PLUGINS_IMAGE} AS mysql

FROM ${BASE} AS base
RUN microdnf install -y libaio openssl-libs ncurses-libs numactl-libs ca-certificates tzdata shadow-utils procps-ng \
    && microdnf clean all \
    && useradd -u 1000 -m -s /sbin/nologin vdb
COPY --from=mysql /usr/local/mysql /usr/local/mysql

FROM base AS template
COPY image/init-template.sql /tmp/init-template.sql
RUN mkdir -p /usr/share/vdb && chown vdb /usr/share/vdb
USER vdb
RUN /usr/local/mysql/bin/mysqld --no-defaults --initialize-insecure \
         --datadir=/usr/share/vdb/template --plugin-dir=/usr/local/mysql/lib/plugin \
         --plugin-load-add=auth_socket.so --init-file=/tmp/init-template.sql \
         --log-error=/tmp/template.err \
    || (cat /tmp/template.err; exit 1)

FROM base AS runtime
COPY --from=go /vdb-mysql /usr/local/bin/vdb-mysql
COPY --from=template /usr/share/vdb/template /usr/share/vdb/template
COPY ca/ /usr/share/vdb/ca/
COPY --from=mysql /usr/local/mysql/share/vdb/ /usr/share/vdb/licenses/
RUN mkdir -p /var/lib/vdb/mysql /var/lib/vdb/tmp /var/lib/vdb/files /var/lib/vdb/plugins /run/vdb /etc/vdb \
    && chown -R vdb /var/lib/vdb /run/vdb \
    && chmod -R a-w /usr/share/vdb/template
LABEL org.opencontainers.image.title="VDB for MySQL" \
      org.opencontainers.image.description="A MySQL-compatible database environment over a read-only Source" \
      org.opencontainers.image.licenses="Elastic-2.0 AND GPL-2.0-only"
USER vdb
EXPOSE 3306
ENTRYPOINT ["/usr/local/bin/vdb-mysql"]

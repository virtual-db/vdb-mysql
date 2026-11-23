# vdb-mysql: the environment image, built from source (implementation plan
# doc 09 §2.4, D-91). No published image is an input.
#
#   plugins-src  vdb-mysql-plugins at PLUGINS_REF, from its public repository.
#                Override with a local checkout:
#                  docker build --build-context plugins-src=../vdb-mysql-plugins .
#   toolchain    compilers and MySQL's build dependencies (the plug-ins'
#                build/toolchain.sh, the one list of them)
#   server       MySQL's source (by default the plug-ins' default release, the
#                first line of build/mysql-releases.txt, with its SHA-256)
#                configured and built; cached, so it rebuilds only when that
#                release or the plug-ins' build scripts change
#   mysql        vdb.so built into that tree, installed into /usr/local/mysql
#   go           the vdb-mysql binary (vdb-core and vdb-mysql-engine from go.mod)
#   collations   fails the build unless the engine's proven.yaml covers the
#                mysqld release (vdb-mysql check-collations)
#   base         oraclelinux:10-slim with mysqld and the user vdb
#   template     the template data directory with the control account
#   reference    the same mysqld without vdb.so, for the oracle (doc 15 §3)
#   runtime      the image that is published (default)
#
# Build the debug variant with --build-arg VARIANT=debug.

ARG BASE=oraclelinux:10-slim
ARG PLUGINS_REF=v0.1.0-alpha.5
ARG MYSQL_RELEASE=

FROM scratch AS plugins-src
ARG PLUGINS_REF
ADD --keep-git-dir=false https://github.com/virtual-db/vdb-mysql-plugins.git#${PLUGINS_REF} /

FROM ${BASE} AS toolchain
COPY --from=plugins-src /build/toolchain.sh /tmp/toolchain.sh
RUN /tmp/toolchain.sh

FROM toolchain AS server
ARG MYSQL_RELEASE
ARG VARIANT=release
ARG JOBS=4
ENV SRC=/work/mysql BLD=/work/build VDB_SRC=/vdb/src VARIANT=${VARIANT} JOBS=${JOBS}
COPY --from=plugins-src /build/ /vdb/build/
RUN --mount=type=cache,target=/cache /vdb/build/build.sh fetch "${MYSQL_RELEASE}"
RUN /vdb/build/build.sh configure
RUN /vdb/build/build.sh server

FROM server AS mysql
COPY --from=plugins-src /src/ /vdb/src/
RUN /vdb/build/build.sh plugin \
    && DESTDIR=/out /vdb/build/build.sh install \
    && mkdir -p /out/usr/local/mysql/share/vdb \
    && cat /work/mysql/.vdb-release > /out/usr/local/mysql/share/vdb/release
COPY --from=plugins-src /COPYING /out/usr/local/mysql/share/vdb/COPYING
COPY --from=plugins-src /docs/source-offer.md /out/usr/local/mysql/share/vdb/source-offer.md

FROM golang:1.27.4 AS go
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /vdb-mysql .

# The image's mysqld release must have proven collations (vdb-mysql#23):
# without them every non-binary string index reads whole tables. The
# runtime stage copies /proven, so the published image cannot be built
# when this fails.
FROM go AS collations
COPY --from=mysql /out/usr/local/mysql/share/vdb/release /tmp/release
RUN /vdb-mysql check-collations "$(cat /tmp/release)" && touch /proven

FROM ${BASE} AS base
RUN microdnf install -y libaio openssl-libs ncurses-libs numactl-libs ca-certificates tzdata shadow-utils procps-ng \
    && microdnf clean all \
    && useradd -u 1000 -m -s /sbin/nologin vdb
COPY --from=mysql /out/usr/local/mysql /usr/local/mysql

FROM base AS template
COPY image/init-template.sql /tmp/init-template.sql
RUN mkdir -p /usr/share/vdb && chown vdb /usr/share/vdb
USER vdb
# The template is created with the smallest redo log (8 MB, not the default
# 100 MB of zeros); mysqld grows it to its configured capacity when it starts.
# It is made read-only here, in the same layer: a chmod in a later layer would
# store every file of it a second time.
RUN /usr/local/mysql/bin/mysqld --no-defaults --initialize-insecure \
         --datadir=/usr/share/vdb/template --plugin-dir=/usr/local/mysql/lib/plugin \
         --plugin-load-add=auth_socket.so --init-file=/tmp/init-template.sql \
         --innodb-redo-log-capacity=8M --log-error=/tmp/template.err \
    || (cat /tmp/template.err; exit 1) \
    && chmod -R a-w /usr/share/vdb/template

FROM base AS reference
COPY oracle/reference-entrypoint.sh /usr/local/bin/reference-entrypoint
RUN mkdir -p /var/lib/vdb && chown vdb /var/lib/vdb
USER vdb
EXPOSE 3306
ENTRYPOINT ["/usr/local/bin/reference-entrypoint"]

FROM base AS runtime
COPY --from=collations /proven /usr/share/vdb/collations-proven
COPY --from=go /vdb-mysql /usr/local/bin/vdb-mysql
COPY --from=template /usr/share/vdb/template /usr/share/vdb/template
COPY ca/ /usr/share/vdb/ca/
COPY --from=mysql /out/usr/local/mysql/share/vdb/ /usr/share/vdb/licenses/
RUN mkdir -p /var/lib/vdb/mysql /var/lib/vdb/tmp /var/lib/vdb/files /var/lib/vdb/plugins /run/vdb /etc/vdb \
    && chown -R vdb /var/lib/vdb /run/vdb
ARG PLUGINS_REF
LABEL org.opencontainers.image.title="VDB for MySQL" \
      org.opencontainers.image.description="A MySQL-compatible database environment over a read-only Source" \
      org.opencontainers.image.licenses="Elastic-2.0 AND GPL-2.0-only" \
      net.vdbm.plugins.ref="${PLUGINS_REF}"
USER vdb
EXPOSE 3306
ENTRYPOINT ["/usr/local/bin/vdb-mysql"]

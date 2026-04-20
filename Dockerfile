# =============================================================================
# virtual-db/mysql — Dockerfile
#
# Produces a minimal release image containing only the vdb-mysql binary.
# All dependencies are resolved from their published module versions; no local
# source replacements or go.work files are needed.
#
# Build args:
#   GIT_AUTH_TOKEN  — passed as a BuildKit secret for fetching any private Go
#                     modules (AnqorDX/dispatch, AnqorDX/pipeline) during
#                     go mod download. Supplied by the CI workflow.
#
# Runtime environment variables:
#   VDB_LISTEN_ADDR      — address:port the server listens on (default :3306)
#   VDB_DB_NAME          — database name exposed to connecting clients
#   VDB_SOURCE_DSN       — DSN for the upstream MySQL source
#   VDB_AUTH_SOURCE_ADDR — host:port of the auth source MySQL instance
#   VDB_PLUGIN_DIR       — optional: directory containing plugin subdirectories
# =============================================================================

# -----------------------------------------------------------------------------
# Stage 1: builder
# -----------------------------------------------------------------------------
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git

WORKDIR /build

# Configure git to use the auth token for all github.com requests so that
# go mod download can fetch private modules (AnqorDX/dispatch, AnqorDX/pipeline).
# The token is mounted as a BuildKit secret and never written to any image layer.
RUN --mount=type=secret,id=GIT_AUTH_TOKEN \
    git config --global url."https://x-access-token:$(cat /run/secrets/GIT_AUTH_TOKEN)@github.com/".insteadOf "https://github.com/"

# Copy only the module files first so Docker can cache the download layer
# independently of source changes.
COPY go.mod go.sum ./

RUN GOPRIVATE=github.com/AnqorDX go mod download

# Copy source and build the binary.
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/vdb-mysql .

# -----------------------------------------------------------------------------
# Stage 2: runtime
# -----------------------------------------------------------------------------
FROM alpine:3.20

# ca-certificates — required for any TLS connections the binary makes at runtime.
# tzdata         — ensures time-zone-aware SQL functions behave correctly.
RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/vdb-mysql /usr/local/bin/vdb-mysql

EXPOSE 3306

ENTRYPOINT ["/usr/local/bin/vdb-mysql"]

FROM golang:1.26.9-alpine AS builder

# The backend module replace-directs to the sibling collector/edgecore
# modules (see backend/go.mod), so the build context must be the repository
# root and those modules must be copied alongside backend/.
WORKDIR /src
COPY backend/go.mod backend/go.sum ./backend/
COPY collector/go.mod ./collector/
COPY edgecore/go.mod ./edgecore/
WORKDIR /src/backend
RUN go mod download

WORKDIR /src
COPY backend/ backend/
COPY collector/ collector/
COPY edgecore/ edgecore/
WORKDIR /src/backend
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/server ./cmd/server

FROM alpine:3.24

# apk upgrade pulls security fixes released after the base image (SEC-11).
RUN apk --no-cache upgrade && apk --no-cache add ca-certificates tzdata
# Install the binary as root:root 0755. /usr/local/bin is root-owned, so the
# copy must happen before dropping privileges to the nobody user below.
COPY --from=builder --chmod=0755 /bin/server /usr/local/bin/server
# The API contract: the server reads the lifecycle of deprecated operations
# (Deprecation/Sunset headers, API-01) from it.
COPY --chmod=0644 api/openapi.yaml /etc/reticora/openapi.yaml
ENV RETICORA_OPENAPI_SPEC_PATH=/etc/reticora/openapi.yaml
# Mount point of the collector PKI volume (COL-08): a fresh named volume
# takes over this ownership, so the unprivileged server can write it.
RUN mkdir -p /pki && chown nobody:nobody /pki

EXPOSE 8080
USER nobody:nobody
ENTRYPOINT ["server"]

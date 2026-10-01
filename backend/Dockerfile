FROM golang:1.25-alpine AS builder

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

EXPOSE 8080
USER nobody:nobody
ENTRYPOINT ["server"]

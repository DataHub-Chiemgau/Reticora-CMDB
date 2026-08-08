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

FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /bin/server /usr/local/bin/server

EXPOSE 8080
USER nobody:nobody
ENTRYPOINT ["server"]

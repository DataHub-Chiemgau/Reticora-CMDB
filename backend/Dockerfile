FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/server ./cmd/server

FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /bin/server /usr/local/bin/server

EXPOSE 8080
USER nobody:nobody
ENTRYPOINT ["server"]

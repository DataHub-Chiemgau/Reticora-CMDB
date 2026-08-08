FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.work go.work
COPY edgecore/ edgecore/
COPY collector/ collector/

WORKDIR /src/collector
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/collector ./cmd/collector

FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata
COPY --from=builder /bin/collector /usr/local/bin/collector

EXPOSE 9090
USER nobody:nobody
ENTRYPOINT ["collector"]

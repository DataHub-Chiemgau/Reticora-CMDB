FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.work go.work
COPY edgecore/ edgecore/
COPY collector/ collector/

# go.work also lists ./backend, which is not part of this build context; the
# collector module resolves edgecore through its own replace directive.
ENV GOWORK=off
WORKDIR /src/collector
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/collector ./cmd/collector

FROM alpine:3.24

# apk upgrade pulls security fixes released after the base image (SEC-11).
RUN apk --no-cache upgrade && apk --no-cache add ca-certificates tzdata
COPY --from=builder /bin/collector /usr/local/bin/collector

EXPOSE 9090
USER nobody:nobody
ENTRYPOINT ["collector"]

FROM golang:1.26.9-alpine AS builder

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
# ICMP sweep (DIS-01): the collector runs unprivileged and pings over
# unprivileged ICMP sockets, which Docker and Podman allow in containers
# (net.ipv4.ping_group_range = "0 2147483647"). No file capability is set:
# a runtime without NET_RAW would then refuse to start the binary. Where
# the sysctl is restricted, set it with --sysctl or the ICMP probe is off
# (logged at start) and the sweep uses TCP and ARP. ARP reads the
# neighbour table; it needs no capability, only host networking for the
# scanned segment.

EXPOSE 9090
USER nobody:nobody
ENTRYPOINT ["collector"]

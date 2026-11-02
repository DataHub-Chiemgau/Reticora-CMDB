FROM golang:1.26.10-alpine AS builder

WORKDIR /src
COPY go.work go.work
COPY edgecore/ edgecore/
COPY collector/ collector/

# go.work also lists ./backend, which is not part of this build context; the
# collector module resolves edgecore through its own replace directive.
ENV GOWORK=off
WORKDIR /src/collector
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/reticora-collector ./cmd/collector

FROM alpine:3.24

# apk upgrade pulls security fixes released after the base image (SEC-11).
RUN apk --no-cache upgrade && apk --no-cache add ca-certificates tzdata
# The command of the UI's enroll instruction (INS-06) is the entrypoint:
# arguments "enroll --server <url> --code <code>" run it in the container.
# "collector" stays as a link for existing calls.
COPY --from=builder /bin/reticora-collector /usr/local/bin/reticora-collector
RUN ln -s reticora-collector /usr/local/bin/collector
# ICMP sweep (DIS-01): the collector runs unprivileged and pings over
# unprivileged ICMP sockets, which Docker and Podman allow in containers
# (net.ipv4.ping_group_range = "0 2147483647"). No file capability is set:
# a runtime without NET_RAW would then refuse to start the binary. Where
# the sysctl is restricted, set it with --sysctl or the ICMP probe is off
# (logged at start as sweep_icmp) and the sweep uses TCP and ARP. ARP reads
# the neighbour table; it needs no capability, only host networking for the
# scanned segment.
# Operator note: with host networking (install-vm.sh --docker) the host's
# net.ipv4.ping_group_range applies and --sysctl is refused: set it on the
# host (sysctl -w net.ipv4.ping_group_range="0 2147483647", persisted in
# /etc/sysctl.d/). NET_RAW (raw socket) works only for root in the
# container (user "0:0"): without a file capability, --cap-add NET_RAW does
# not reach the user nobody.

EXPOSE 9090
USER nobody:nobody
ENTRYPOINT ["reticora-collector"]

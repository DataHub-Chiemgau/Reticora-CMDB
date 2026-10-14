package collectorcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"
)

// The data path over the edge (COL-08): once the collector holds its
// mutually authenticated NATS channel, it delivers records and heartbeats
// there; the server takes organization and collector from the subject the
// certificate was authorized for. Without the channel (not enrolled, no
// connection, a server that does not answer the subject yet) the HTTP path
// stays.
const (
	edgeIngestSuffix    = ".ingest"
	edgeHeartbeatSuffix = ".heartbeat"
	edgeRequestTimeout  = 60 * time.Second
)

// errNoEdge reports that the edge channel cannot take the request; the
// caller falls back to HTTP.
var errNoEdge = errors.New("edge channel unavailable")

// edgeReply is the server's answer on the edge (discovery.EdgeReply).
type edgeReply struct {
	Status          int    `json:"status"`
	Type            string `json:"type"`
	Error           string `json:"error"`
	LicenseStatus   string `json:"license_status"`
	CollectorStatus string `json:"collector_status"`
}

// edgeRequest sends data on the collector's subject tree; tests replace it.
var edgeRequest = func(ctx context.Context, suffix string, data []byte) ([]byte, error) {
	ch := scopeChannel.Load()
	if ch == nil || ch.nc == nil || !ch.nc.IsConnected() || int64(len(data)) > ch.nc.MaxPayload() {
		return nil, errNoEdge
	}
	rctx, cancel := context.WithTimeout(ctx, edgeRequestTimeout)
	defer cancel()
	msg, err := ch.nc.RequestWithContext(rctx, ch.subject+suffix, data)
	if errors.Is(err, nats.ErrNoResponders) {
		return nil, errNoEdge
	}
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

// postViaEdge delivers a records payload over the edge. errNoEdge means the
// HTTP path has to take it.
func (u *uploader) postViaEdge(ctx context.Context, payload []byte) error {
	raw, err := edgeRequest(ctx, edgeIngestSuffix, payload)
	if err != nil {
		return err
	}
	var reply edgeReply
	if err = json.Unmarshal(raw, &reply); err != nil {
		return fmt.Errorf("edge ingest reply: %w", err)
	}
	if reply.Status == http.StatusForbidden && reply.Type == problemLicenseExpired {
		u.setLicenseStatus("expired")
		return errLicenseExpired
	}
	if reply.Status >= 400 {
		return fmt.Errorf("upload failed with status %d: %s", reply.Status, reply.Error)
	}
	if reply.LicenseStatus != "" {
		u.setLicenseStatus(reply.LicenseStatus)
	}
	return nil
}

// heartbeatViaEdge sends a heartbeat over the edge; ok is false when the
// edge did not take it (the caller then uses HTTP).
func heartbeatViaEdge(ctx context.Context, body []byte) (licenseStatus string, ok, handled bool) {
	start := time.Now()
	raw, err := edgeRequest(ctx, edgeHeartbeatSuffix, body)
	if errors.Is(err, errNoEdge) {
		return "", false, false
	}
	if err != nil {
		slog.Error("heartbeat over the edge failed", "error", err)
		return "", false, true
	}
	var reply edgeReply
	if err = json.Unmarshal(raw, &reply); err != nil {
		slog.Error("heartbeat over the edge: invalid reply", "error", err)
		return "", false, true
	}
	slog.Info("heartbeat completed", "status", reply.Status, "transport", "edge", "duration_ms", time.Since(start).Milliseconds())
	if reply.Status >= 300 {
		return "", false, true
	}
	return reply.LicenseStatus, true, true
}

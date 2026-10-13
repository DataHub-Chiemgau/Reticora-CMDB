package collectorcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Discovery scopes (DIS-09): the server sends the collector its enabled
// scopes over the NATS channel; the collector discovers each scope with its
// plugins, tags the DeviceRecords with scope, client, site and VRF and
// reports every run. After the connection it reports its local subnets,
// which the server offers as scope_suggested. Without a scope list from the
// server (not enrolled, no NATS endpoint) the collector scans
// RETICORA_SCAN_SUBNETS as before.

// collectorScope is a scope as the server sends it.
type collectorScope struct {
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	Target        string   `json:"target"`
	ClientID      string   `json:"client_id,omitempty"`
	SiteID        string   `json:"site_id,omitempty"`
	VRFID         string   `json:"vrf_id,omitempty"`
	Schedule      string   `json:"schedule"`
	CredentialIDs []string `json:"credential_ids"`
	Plugins       []string `json:"plugins"`
}

type scopeCache struct {
	mu     sync.RWMutex
	scopes []collectorScope
	known  bool
}

func (c *scopeCache) set(scopes []collectorScope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scopes, c.known = scopes, true
}

func (c *scopeCache) get() ([]collectorScope, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.scopes, c.known
}

var collectorScopes = &scopeCache{}

// scopeChannel is the open NATS connection and the collector's subject
// tree while the collector is connected.
type scopeChannelState struct {
	nc      *nats.Conn
	subject string
}

var scopeChannel atomic.Pointer[scopeChannelState]

const natsRequestTimeout = 10 * time.Second

func natsRequest(ctx context.Context, nc *nats.Conn, subject string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, natsRequestTimeout)
	defer cancel()
	msg, err := nc.RequestWithContext(rctx, subject, raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(msg.Data, out)
}

// fetchScopes asks the server for the collector's enabled scopes.
func fetchScopes(ctx context.Context, nc *nats.Conn, subject string) ([]collectorScope, error) {
	var resp struct {
		Scopes []collectorScope `json:"scopes"`
		Error  string           `json:"error"`
	}
	if err := natsRequest(ctx, nc, subject+".scopes.request", struct{}{}, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New("scopes refused: " + resp.Error)
	}
	if resp.Scopes == nil {
		resp.Scopes = []collectorScope{}
	}
	return resp.Scopes, nil
}

// syncScopes refreshes the scopes until ctx ends.
func syncScopes(ctx context.Context, nc *nats.Conn, subject string) {
	ticker := time.NewTicker(bundleRefresh)
	defer ticker.Stop()
	for {
		if scopes, err := fetchScopes(ctx, nc, subject); err != nil {
			slog.Warn("discovery scopes not updated", "error", err)
		} else {
			collectorScopes.set(scopes)
			slog.Info("discovery scopes updated", "scopes", len(scopes))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// localSubnets returns the IPv4 subnets of the collector's interfaces
// (loopback, link-local and host routes excluded).
func localSubnets(addrs func() ([]net.Addr, error)) []string {
	list, err := addrs()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, a := range list {
		p, perr := netip.ParsePrefix(a.String())
		if perr != nil {
			continue
		}
		ip := p.Addr()
		if !ip.Is4() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || p.Bits() >= 31 {
			continue
		}
		cidr := p.Masked().String()
		if !seen[cidr] {
			seen[cidr] = true
			out = append(out, cidr)
		}
	}
	return out
}

// reportSubnets sends the local subnets once after the connection
// (Zero-Config, INS-06).
func reportSubnets(ctx context.Context, nc *nats.Conn, subject string, subnets []string) {
	if len(subnets) == 0 {
		return
	}
	var resp map[string]any
	if err := natsRequest(ctx, nc, subject+".subnets.report", map[string]any{"subnets": subnets}, &resp); err != nil {
		slog.Warn("local subnets not reported", "error", err)
		return
	}
	slog.Info("local subnets reported", "subnets", subnets, "suggested", resp["suggested"])
}

// scopeTargets expands a scope into the addresses or names to probe.
func scopeTargets(s *collectorScope) []string {
	switch s.Kind {
	case "cidr":
		return expandSubnets([]string{s.Target})
	case "host":
		return []string{s.Target}
	case "range":
		from, to, ok := strings.Cut(s.Target, "-")
		a, errA := netip.ParseAddr(strings.TrimSpace(from))
		b, errB := netip.ParseAddr(strings.TrimSpace(to))
		if !ok || errA != nil || errB != nil {
			return nil
		}
		var out []string
		for ip := a; ip.IsValid() && !b.Less(ip) && len(out) <= maxSubnetExpansionTargets; ip = ip.Next() {
			out = append(out, ip.String())
		}
		return out
	}
	return nil
}

// discoveryPass is one set of targets discovered with a set of protocols;
// scope is nil for the RETICORA_SCAN_SUBNETS fallback.
type discoveryPass struct {
	scope     *collectorScope
	targets   []string
	protocols []string
}

// discoveryPasses returns the passes of a cycle: one per scope when the
// server sent a scope list, otherwise the configured subnets.
func discoveryPasses(cfg *collectorConfig) []discoveryPass {
	scopes, known := collectorScopes.get()
	if !known {
		return []discoveryPass{{targets: expandSubnets(cfg.ScanSubnets), protocols: cfg.Protocols}}
	}
	out := make([]discoveryPass, 0, len(scopes))
	for i := range scopes {
		s := scopes[i]
		out = append(out, discoveryPass{scope: &s, targets: scopeTargets(&s), protocols: s.Plugins})
	}
	return out
}

// tagRecords sets the scope fields of the records (DIS-05: client and site
// come from the scope).
func tagRecords(records []plugins.DeviceRecord, s *collectorScope) {
	if s == nil {
		return
	}
	for i := range records {
		records[i].ScopeID, records[i].ClientID, records[i].SiteID, records[i].VRFID = s.ID, s.ClientID, s.SiteID, s.VRFID
	}
}

// reportScopeRun sends the result of a scope run to the server.
func reportScopeRun(ctx context.Context, s *collectorScope, started time.Time, records int, errs []string, next time.Time) {
	ch := scopeChannel.Load()
	if ch == nil || s == nil {
		return
	}
	status := "ok"
	if len(errs) > 0 {
		status = "partial"
	}
	result := map[string]any{"status": status, "records": records, "duration_ms": time.Since(started).Milliseconds()}
	if len(errs) > 0 {
		result["errors"] = errs
	}
	var resp map[string]any
	if err := natsRequest(ctx, ch.nc, ch.subject+".scopes.result", map[string]any{
		"scope_id": s.ID, "at": started.UTC(), "next_run": next.UTC(), "result": result,
	}, &resp); err != nil {
		slog.Warn("scope run not reported", "scope_id", s.ID, "error", err)
	}
}

func pluginError(protocol string, err error) string { return fmt.Sprintf("%s: %v", protocol, err) }

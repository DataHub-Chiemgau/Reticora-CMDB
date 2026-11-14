package collectorcmd

import (
	"context"
	"log/slog"
	"maps"
	"net"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The SNMP trap receiver of the collector (DIS-06, WP-977). The server
// configures it per collector group (collector_group.trap_config) and hands
// the configuration over with every assignment set (probewire.TrapConfig):
// on or off, the UDP port and the credentials whose communities (kind
// snmp_v2c) the receiver accepts; the secrets come from the credential
// bundle, the set names only their ids. The collector follows every set
// without a restart: it starts the receiver, stops it, binds again on a
// changed port and takes the communities of the credentials as the bundle
// holds them when a trap arrives. In this mode no "empty community accepts
// everything" applies: a v1/v2c trap with a community of no configured
// credential is dropped and counted (traps_dropped{reason="community"} in
// the alive message); without a snmp_v2c credential every v1/v2c trap is
// dropped.
//
// SNMPv3 (WP-978): the snmp_v3 credentials of the same credential_ids are
// the USM users of the receiver (authNoPriv or authPriv, snmp.TrapUSM);
// their keys are localized per sender engine and kept across rebinds. A v3
// trap is accepted only from a configured user with a valid digest; drops
// are counted by reason (security_level, unknown_user, auth, decrypt,
// not_in_time_window) next to community.
//
// Fallback: until a set with trap_config arrives (a collector without
// group, a server without the configuration, before the first set)
// RETICORA_SNMP_TRAP_LISTEN starts the receiver with the community of the
// local credentials as before. The first set with trap_config replaces it
// (one bind per port); enabled false stops it as well.

// trapReceiverRun runs a receiver until ctx ends (tests replace it).
type trapReceiverRun func(ctx context.Context, r *snmp.TrapReceiver) error

// trapManager owns the trap receiver of the collector.
type trapManager struct {
	// sink uploads an accepted trap (uploader.uploadTrapEvent).
	sink func(ctx context.Context, ev snmp.TrapEvent)
	// communities returns the communities of the credentials of ids from
	// the credential bundle (bundleCache.trapCommunities).
	communities func(ids []string) []string
	// v3Users returns the snmp_v3 credentials of ids from the bundle
	// (bundleCache.trapV3Users).
	v3Users func(ids []string) []map[string]string
	// usm holds the SNMPv3 users, keys and engine times of the server
	// mode; it outlives a rebind.
	usm    *snmp.TrapUSM
	run    trapReceiverRun
	logger *slog.Logger

	mu       sync.Mutex
	ctx      context.Context
	envCfg   collectorConfig // RETICORA_SNMP_TRAP_LISTEN and the local community
	server   bool            // a set with trap_config arrived
	config   probewire.TrapConfig
	active   *runningTrapReceiver
	received atomic.Int64
	dropMu   sync.Mutex
	dropped  map[string]int64
}

// runningTrapReceiver is the receiver in operation.
type runningTrapReceiver struct {
	addr   string
	server bool
	recv   *snmp.TrapReceiver
	cancel context.CancelFunc
	done   chan struct{}
}

// collectorTraps is the trap receiver of the running collector.
var collectorTraps = &trapManager{}

func (m *trapManager) log() *slog.Logger {
	if m.logger != nil {
		return m.logger
	}
	return slog.Default()
}

// start takes the context of the collector and the environment fallback
// (RETICORA_SNMP_TRAP_LISTEN with the local community) and starts the
// fallback receiver unless a set with trap_config arrived already.
func (m *trapManager) start(ctx context.Context, cfg *collectorConfig, up *uploader) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ctx = ctx
	m.envCfg = collectorConfig{TrapListenAddr: cfg.TrapListenAddr, Credentials: cfg.Credentials}
	if m.sink == nil && up != nil {
		m.sink = func(ctx context.Context, ev snmp.TrapEvent) {
			slog.Info("snmp trap received", "source", ev.SourceIP, "trap_oid", ev.TrapOID)
			if err := up.uploadTrapEvent(ctx, ev); err != nil {
				slog.Error("trap event upload failed", "source", ev.SourceIP, "trap_oid", ev.TrapOID, "count", 1, "error", err)
			}
		}
	}
	if m.communities == nil {
		m.communities = collectorBundle.trapCommunities
	}
	if m.v3Users == nil {
		m.v3Users = collectorBundle.trapV3Users
	}
	m.reconcileLocked()
}

// trapUSM returns the USM of the server mode, created on first use; the
// users are the snmp_v3 credentials of the current configuration.
func (m *trapManager) trapUSMLocked() *snmp.TrapUSM {
	if m.usm == nil {
		m.usm = &snmp.TrapUSM{Users: func() []map[string]string {
			m.mu.Lock()
			ids, users := slices.Clone(m.config.CredentialIDs), m.v3Users
			m.mu.Unlock()
			if users == nil {
				return nil
			}
			return users(ids)
		}}
	}
	return m.usm
}

// apply takes the trap configuration of an assignment set (nil: the set
// carries none, the environment fallback applies).
func (m *trapManager) apply(cfg *probewire.TrapConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg == nil {
		m.server = false
		m.reconcileLocked()
		return
	}
	if !m.server && m.envCfg.TrapListenAddr != "" {
		m.log().Info("snmp trap receiver: server configuration replaces RETICORA_SNMP_TRAP_LISTEN",
			"event", "collector.traps.server_config")
		if !cfg.Enabled {
			// The group switched the receiver off explicitly: say so, the
			// operator relied on the environment receiver until now.
			m.log().Warn("snmp trap receiver: the collector group disables the trap receiver; RETICORA_SNMP_TRAP_LISTEN no longer applies",
				"event", "collector.traps.server_disabled", "listen", m.envCfg.TrapListenAddr)
		}
	}
	m.server = true
	m.config = probewire.TrapConfig{Enabled: cfg.Enabled, Port: cfg.Port, CredentialIDs: slices.Clone(cfg.CredentialIDs)}
	m.reconcileLocked()
}

// reconcileLocked starts, stops or rebinds the receiver after the wanted
// state (environment or server configuration).
func (m *trapManager) reconcileLocked() {
	if m.ctx == nil {
		return // before start: the set is applied then
	}
	want := ""
	if m.server {
		if m.config.Enabled {
			port := m.config.Port
			if port == 0 {
				port = probewire.DefaultTrapPort
			}
			want = net.JoinHostPort("", strconv.Itoa(port))
		}
	} else {
		want = m.envCfg.TrapListenAddr
	}
	if a := m.active; a != nil {
		select {
		case <-a.done: // the receiver stopped (bind failed): start again
			m.active = nil
		default:
		}
	}
	if a := m.active; a != nil && a.addr == want && a.server == m.server {
		return
	}
	m.stopLocked()
	if want == "" {
		return
	}
	// The environment receiver checks the local community as before
	// (newTrapReceiver); the server mode checks the configured credentials
	// in handle.
	recv := &snmp.TrapReceiver{ListenAddr: want, USM: m.trapUSMLocked()}
	if !m.server {
		recv = newTrapReceiver(m.envCfg, nil)
	}
	recv.Sink = m.handle
	recv.OnDrop = m.dropReceived
	ctx, cancel := context.WithCancel(m.ctx)
	a := &runningTrapReceiver{addr: want, server: m.server, recv: recv, cancel: cancel, done: make(chan struct{})}
	m.active = a
	run := m.run
	if run == nil {
		run = func(ctx context.Context, r *snmp.TrapReceiver) error { return r.Run(ctx) }
	}
	go func() {
		defer close(a.done)
		if err := run(ctx, recv); err != nil {
			m.log().Error("snmp trap receiver stopped", "addr", want, "error", err)
		}
	}()
}

// stopLocked stops the receiver and waits until its socket is closed, so
// the port can be bound again.
func (m *trapManager) stopLocked() {
	a := m.active
	if a == nil {
		return
	}
	m.active = nil
	a.cancel()
	<-a.done
}

// handle checks a trap against the server configuration and hands it on.
func (m *trapManager) handle(ctx context.Context, ev snmp.TrapEvent) { //nolint:gocritic // signature of snmp.TrapReceiver.Sink
	m.received.Add(1)
	m.mu.Lock()
	server, ids := m.server, m.config.CredentialIDs
	communities := m.communities
	sink := m.sink
	m.mu.Unlock()
	// A v3 trap (User set) was authenticated against the configured users
	// by the receiver; v1/v2c need a configured community.
	if server && ev.User == "" && (communities == nil || !slices.Contains(communities(ids), ev.Community)) {
		m.drop(probewire.TrapDropCommunity)
		slog.Debug("snmp trap dropped: community of no configured credential", "source", ev.SourceIP)
		return
	}
	if sink != nil {
		sink(ctx, ev)
	}
}

// dropReceived counts a trap the receiver itself dropped (SNMPv3 USM,
// WP-978): it was received and is dropped by reason.
func (m *trapManager) dropReceived(reason string) {
	m.received.Add(1)
	m.drop(reason)
}

func (m *trapManager) drop(reason string) {
	m.dropMu.Lock()
	defer m.dropMu.Unlock()
	if m.dropped == nil {
		m.dropped = map[string]int64{}
	}
	m.dropped[reason]++
}

// stats returns the counters of the alive message (WP-977 AK3): the traps
// received and the traps dropped by reason since the start.
func (m *trapManager) stats() (received int64, dropped map[string]int64) {
	m.dropMu.Lock()
	defer m.dropMu.Unlock()
	if len(m.dropped) > 0 {
		dropped = maps.Clone(m.dropped)
	}
	return m.received.Load(), dropped
}

// localAddr is the bound address of the receiver (nil while none runs).
func (m *trapManager) localAddr() net.Addr {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return nil
	}
	return m.active.recv.LocalAddr()
}

// stop stops the receiver (shutdown, tests).
func (m *trapManager) stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

// trapV3Users returns the secrets of the snmp_v3 credentials of ids the
// bundle holds (WP-978): username, auth_protocol, auth_password,
// priv_protocol, priv_password.
func (c *bundleCache) trapV3Users(ids []string) []map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []map[string]string
	for _, id := range ids {
		cred, ok := c.creds[id]
		if !ok || cred.Kind != "snmp_v3" {
			continue
		}
		user := map[string]string{}
		for k, v := range cred.Secret {
			if s, isString := v.(string); isString {
				user[k] = s
			}
		}
		if len(user) > 0 {
			out = append(out, user)
		}
	}
	return out
}

// trapCommunities returns the communities of the snmp_v2c credentials of
// ids the bundle holds (WP-977).
func (c *bundleCache) trapCommunities(ids []string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	for _, id := range ids {
		cred, ok := c.creds[id]
		if !ok || cred.Kind != "snmp_v2c" {
			continue
		}
		if s, isString := cred.Secret["community"].(string); isString && s != "" {
			out = append(out, s)
		}
	}
	return out
}

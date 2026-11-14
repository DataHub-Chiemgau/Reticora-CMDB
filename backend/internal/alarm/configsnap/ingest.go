package configsnap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The ingest of the device configurations a collector sends on .config
// (ALR-07, EVT-03, WP-1054, E-207 option 2). The edge handler checks the
// collector, the feature monitoring_network and the message (schema
// version, size, SHA-256 of the decompressed content) and calls Ingest in
// the scope of the collector. Ingest assigns the version by ci_id to a CI
// the collector's scope sees whose management address is the snapshot's,
// and stores it through InsertIfChanged in one tenant transaction. A new
// version with a predecessor writes, in the same transaction, the alarm
// running-config of the CI (source config, severity info, Raise counts a
// further change while it is open) and, as the last write, the event
// ci.config_changed: hashes, snapshot ids, time, source, trigger, platform
// and the number of masked secrets, never the content. The first version
// of a CI and a repeated delivery (same hash) write neither alarm nor
// event. The daily retention.config_snapshot resolves the config alarms
// unchanged for 24 h (config_stable).

// ConfigSeries is the series of the configuration alarm of a CI.
const ConfigSeries = "running-config"

// ReasonConfigStable resolves a configuration alarm without change for
// StableAfter.
const ReasonConfigStable = "config_stable"

// StableAfter is how long a configuration alarm stays open without a
// further change.
const StableAfter = 24 * time.Hour

// Outcomes of Ingest.
const (
	OutcomeStored    = "stored"
	OutcomeUnchanged = "unchanged"
)

// ErrManagementIPMismatch: the CI's management address is not the
// snapshot's (the collector read another device than the CI it names).
var ErrManagementIPMismatch = errors.New("management_ip does not match the ci")

// IngestResult is the answer of Ingest.
type IngestResult struct {
	Outcome    string
	SnapshotID string
}

// changedData is the data of ci.config_changed: the frame of WP-867 (hash
// is the new SHA-256) with the versions, the collection and the number of
// masked secrets; no content.
type changedData struct {
	events.CIConfigChangedData
	SnapshotID         string    `json:"snapshot_id"`
	PreviousSnapshotID string    `json:"previous_snapshot_id"`
	SHA256             string    `json:"sha256"`
	PreviousSHA256     string    `json:"previous_sha256"`
	CollectedAt        time.Time `json:"collected_at"`
	Source             string    `json:"source"`
	Trigger            string    `json:"trigger"`
	Platform           string    `json:"platform"`
	MaskedCount        int       `json:"masked_count"`
}

// Ingester stores the configurations of the collectors.
type Ingester struct {
	pool   *pgxpool.Pool
	store  Store
	alarms *alarm.Service
}

// NewIngester returns the ingest on pool with the store and the alarm
// lifecycle (nil: no alarm).
func NewIngester(pool *pgxpool.Pool, store Store, alarms *alarm.Service) *Ingester {
	return &Ingester{pool: pool, store: store, alarms: alarms}
}

// Ingest stores one checked snapshot of the collector in the scope of ctx
// (the collector's). A CI the scope does not see, of another organization
// or with another management address is ErrCINotFound respectively
// ErrManagementIPMismatch; a late version ErrOutOfOrder.
func (g *Ingester) Ingest(ctx context.Context, orgID, collectorID string, snap *probewire.ConfigSnapshot, text string) (IngestResult, error) {
	var out IngestResult
	err := database.WithRequestTenant(ctx, g.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		out = IngestResult{}
		txCtx := database.ContextWithTx(ctx, tx)
		var mgmt string
		var client, site *string
		err := tx.QueryRow(ctx, `SELECT COALESCE(host(management_ip), ''), client_id::text, site_id::text FROM ci
			WHERE organization_id = $1 AND id = $2 AND deleted_at IS NULL`, orgID, snap.CIID).Scan(&mgmt, &client, &site)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCINotFound
		}
		if err != nil {
			return fmt.Errorf("read ci: %w", err)
		}
		if !sameAddress(mgmt, snap.ManagementIP) {
			return ErrManagementIPMismatch
		}
		collector := collectorID
		stored, previous, created, err := g.store.InsertIfChanged(txCtx, orgID, &Snapshot{CIID: snap.CIID, CollectorID: &collector,
			CollectedAt: snap.CollectedAt.UTC(), Source: snap.Source, Trigger: snap.Trigger, Platform: snap.Platform,
			SHA256: strings.ToLower(snap.SHA256), MaskedCount: snap.MaskedCount, Content: []byte(text)})
		if err != nil {
			return err
		}
		out.SnapshotID = stored.ID
		if !created {
			out.Outcome = OutcomeUnchanged
			return nil
		}
		out.Outcome = OutcomeStored
		if previous == nil {
			return nil
		}
		if g.alarms != nil {
			ci := snap.CIID
			if _, err := g.alarms.Raise(txCtx, orgID, &alarm.RaiseInput{SourceKind: alarm.SourceConfig, SourceID: snap.CIID,
				SeriesKey: ConfigSeries, CIID: &ci, Severity: alarm.SeverityInfo,
				Summary:    fmt.Sprintf("configuration changed (%s, %s)", snap.Platform, hashPrefix(stored.SHA256)),
				Details:    map[string]any{"snapshot_id": stored.ID, "previous_snapshot_id": previous.ID, "platform": snap.Platform},
				ObservedAt: stored.CollectedAt}); err != nil {
				return fmt.Errorf("raise configuration alarm: %w", err)
			}
		}
		data := changedData{
			CIConfigChangedData: events.CIConfigChangedData{ID: stored.ID, CIID: snap.CIID, ClientID: deref(client), SiteID: deref(site),
				Hash: stored.SHA256},
			SnapshotID: stored.ID, PreviousSnapshotID: previous.ID, SHA256: stored.SHA256, PreviousSHA256: previous.SHA256,
			CollectedAt: stored.CollectedAt.UTC(), Source: stored.Source, Trigger: stored.Trigger, Platform: stored.Platform,
			MaskedCount: stored.MaskedCount,
		}
		return database.BeforeCommit(txCtx, func() error {
			_, aerr := events.NewOutbox().Append(txCtx, tx, &events.Event{OrgID: orgID, Type: events.CIConfigChanged, EntityID: snap.CIID,
				Data: data})
			return aerr
		})
	})
	return out, err
}

// IngestConfig implements discovery.ConfigIngester: 202 stored, 200
// unchanged, 404 for a CI outside the collector's scope, of another
// organization or with another management address, 409 for a version
// older than the latest one, 400 for a version the store refuses.
func (g *Ingester) IngestConfig(ctx context.Context, orgID, collectorID string, snap *probewire.ConfigSnapshot, text string) (discovery.ConfigOutcome, error) {
	res, err := g.Ingest(ctx, orgID, collectorID, snap, text)
	switch {
	case err == nil && res.Outcome == OutcomeStored:
		return discovery.ConfigOutcome{Status: http.StatusAccepted, Outcome: res.Outcome, SnapshotID: res.SnapshotID}, nil
	case err == nil:
		return discovery.ConfigOutcome{Status: http.StatusOK, Outcome: res.Outcome, SnapshotID: res.SnapshotID}, nil
	case errors.Is(err, ErrCINotFound), errors.Is(err, ErrManagementIPMismatch):
		return discovery.ConfigOutcome{Status: http.StatusNotFound, Error: "ci_id: ci not found"}, nil
	case errors.Is(err, ErrOutOfOrder):
		return discovery.ConfigOutcome{Status: http.StatusConflict, Error: ErrOutOfOrder.Error()}, nil
	case errors.Is(err, ErrTooLarge), errors.Is(err, ErrHashMismatch), errors.Is(err, ErrInvalid):
		return discovery.ConfigOutcome{Status: http.StatusBadRequest, Error: "invalid configuration snapshot"}, nil
	}
	return discovery.ConfigOutcome{}, err
}

// sameAddress reports whether the CI's management address is the
// snapshot's (both empty counts as the same: a CI without address).
func sameAddress(ci, snap string) bool {
	snap = strings.TrimSpace(snap)
	if ci == "" {
		return snap == ""
	}
	a, err := netip.ParseAddr(snap)
	b, cerr := netip.ParseAddr(ci)
	return err == nil && cerr == nil && a.Unmap() == b.Unmap()
}

// StableAlarms resolves the configuration alarms without a further change
// (the port jobs.ConfigAlarms of retention.config_snapshot).
type StableAlarms struct{ Alarms *alarm.Service }

// ClearStable resolves the open configuration alarms of the organization
// whose last change (last_seen_at) is before before, with reason
// config_stable, in the transaction of ctx; it returns their number.
func (s StableAlarms) ClearStable(ctx context.Context, orgID string, before time.Time) (int, error) {
	if s.Alarms == nil {
		return 0, nil
	}
	f := &alarm.ListFilter{States: []string{alarm.StatePending, alarm.StateOpen, alarm.StateAcknowledged},
		SourceKinds: []string{alarm.SourceConfig}, Limit: 500}
	var keys []string
	for {
		page, err := s.Alarms.Store().List(ctx, orgID, f)
		if err != nil {
			return 0, err
		}
		for i := range page.Items {
			if page.Items[i].SeriesKey == ConfigSeries && page.Items[i].LastSeenAt.Before(before) {
				keys = append(keys, page.Items[i].DedupKey)
			}
		}
		if page.Next == nil {
			break
		}
		f.After = page.Next
	}
	n := 0
	for _, k := range keys {
		a, err := s.Alarms.Clear(ctx, orgID, k, time.Time{}, ReasonConfigStable)
		if err != nil {
			return n, err
		}
		if a != nil {
			n++
		}
	}
	return n, nil
}

func hashPrefix(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

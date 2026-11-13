package probe

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The write path of the worker probe_ingest (WP-960 AK1–AK3, AK6, AK7):
// per organization one tenant transaction (database.OrgWideScope, row
// level security and the view probe_result_v) that
//
//  1. reads the probes of the batch with the epoch and the collector of
//     their assignment and the collectors of the batch with their client
//     (one statement each); results of unknown or deleted probes are
//     dropped and counted (unknown_probe), as are results of a collector
//     outside the organization or bound to another client than the probe
//     (foreign_collector: the client boundary holds although the
//     transaction runs org-wide); a result with another epoch than the
//     assignment, from another collector than the assigned one or without
//     assignment is stored with stale_epoch = true (PRB-11) and records no
//     credential use;
//  2. writes every result with one INSERT INTO probe_result_v … SELECT …
//     FROM unnest(…) ON CONFLICT DO NOTHING (PRB-07: a repeated delivery
//     writes no row);
//  3. moves probe_ingest_cursor (stream results) of every collector of the
//     batch forward to its highest sequence number;
//  4. records the credential use the results report through the labels
//     credential_id and credential_namespace at the CI of the probe
//     (credential.RecordUsage, CRD-06).

// resultCodes numbers the reason codes for probe_result.code (smallint):
// 0 is no code, 99 another valid code. The numbers are stored; they never
// change.
var resultCodes = map[string]int16{
	"":                                   0,
	probewire.ReasonTimeout:              1,
	probewire.ReasonRefused:              2,
	probewire.ReasonUnreachable:          3,
	probewire.ReasonTLS:                  4,
	probewire.ReasonAuthFailed:           5,
	probewire.ReasonConfig:               6,
	probewire.ReasonUnsupported:          7,
	probewire.ReasonLimitExceeded:        8,
	probewire.ReasonICMPUnavailable:      9,
	probewire.ReasonCollectorUnreachable: 10,
	probewire.ReasonParentDown:           11,
	probewire.WarnThresholdExceeded:      12,
	probewire.WarnCertExpiring:           13,
	probewire.ReasonExpectMismatch:       14,
	probewire.ReasonResponseTooLarge:     15,
	probewire.WarnAnswerMismatch:         16,
}

// ResultCodeOther is the code of a valid reason code without a number of
// its own.
const ResultCodeOther int16 = 99

// ResultCode returns the number of a reason code for probe_result.code.
func ResultCode(code string) int16 {
	if n, ok := resultCodes[code]; ok {
		return n
	}
	return ResultCodeOther
}

// usageClasses maps the reason code of a failed result to the error class
// of credential_usage; a code without class says nothing about the
// credential.
var usageClasses = map[string]string{
	probewire.ReasonAuthFailed:  credential.UsageAuthFailed,
	probewire.ReasonTimeout:     credential.UsageTimeout,
	probewire.ReasonUnreachable: credential.UsageUnreachable,
	probewire.ReasonRefused:     credential.UsageUnreachable,
	probewire.ReasonTLS:         credential.UsageTLS,
}

// PGIngestStore is the IngestStore on PostgreSQL.
type PGIngestStore struct {
	pool *pgxpool.Pool
}

// NewPGIngestStore returns the store on pool.
func NewPGIngestStore(pool *pgxpool.Pool) *PGIngestStore { return &PGIngestStore{pool: pool} }

var _ IngestStore = (*PGIngestStore)(nil)

// ingestProbe is a probe of a batch with its assignment.
type ingestProbe struct {
	ciID      string
	clientID  string
	epoch     int64
	collector string
	assigned  bool
}

// foreign reports whether a result of the collector with client
// collectorClient (known: found in the organization) for p crosses the
// client boundary.
func (p *ingestProbe) foreign(collectorClient string, known bool) bool {
	return !known || collectorClient != "" && p.clientID != "" && collectorClient != p.clientID
}

// resultRows are the columns of the bulk insert.
type resultRows struct {
	times              []time.Time
	probes, collectors []string
	seqs, epochs       []int64
	statuses, codes    []int16
	rtts, losses       []*float32
	values             []*float64
	stale              []bool
}

func f32(v *float64) *float32 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	f := float32(*v)
	return &f
}

// WriteResults implements IngestStore.
func (s *PGIngestStore) WriteResults(ctx context.Context, orgID string, msgs []ResultsMessage) (IngestOutcome, error) {
	var out IngestOutcome
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, s.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		out = IngestOutcome{}
		probes, err := ingestProbes(ctx, tx, orgID, msgs)
		if err != nil {
			return err
		}
		collectorClients, err := ingestCollectors(ctx, tx, orgID, msgs)
		if err != nil {
			return err
		}
		rows := resultRows{}
		cursor := map[string][2]int64{} // collector → (last seq, its epoch)
		var usage []credential.UsageReport
		usageAt := map[int]time.Time{}
		for _, m := range msgs {
			collectorClient, known := collectorClients[m.CollectorID]
			for i := range m.Results {
				r := &m.Results[i]
				p, ok := probes[r.ProbeID]
				status, valid := ResultStatusCode(r.Status)
				if !ok || !valid {
					out.UnknownProbe++
					continue
				}
				if p.foreign(collectorClient, known) {
					out.ForeignCollector++
					continue
				}
				stale := !p.assigned || p.epoch != r.Epoch || p.collector != m.CollectorID
				if stale {
					out.Stale++
				}
				seq := int64(min(r.Seq, math.MaxInt64)) //nolint:gosec // bounded above
				rows.times = append(rows.times, r.ObservedAt.UTC())
				rows.probes = append(rows.probes, r.ProbeID)
				rows.collectors = append(rows.collectors, m.CollectorID)
				rows.seqs = append(rows.seqs, seq)
				rows.epochs = append(rows.epochs, r.Epoch)
				rows.statuses = append(rows.statuses, status)
				rows.codes = append(rows.codes, ResultCode(r.Code))
				rows.rtts = append(rows.rtts, f32(r.RTTMS))
				rows.losses = append(rows.losses, f32(r.Loss))
				rows.values = append(rows.values, r.Value)
				rows.stale = append(rows.stale, stale)
				if c, seen := cursor[m.CollectorID]; !seen || seq > c[0] {
					cursor[m.CollectorID] = [2]int64{seq, r.Epoch}
				}
				if e, ok := usageEntry(r, p.ciID); ok && !stale {
					usageAt[len(usage)] = r.ObservedAt
					usage = append(usage, e)
				}
			}
		}
		if len(rows.times) > 0 {
			tag, err := tx.Exec(ctx, `INSERT INTO probe_result_v (time, organization_id, probe_id, collector_id, seq, epoch,
					status, code, rtt_ms, loss, value, stale_epoch)
				SELECT u.time, $1, u.probe_id, u.collector_id, u.seq, u.epoch, u.status, u.code, u.rtt_ms, u.loss, u.value, u.stale
				  FROM unnest($2::timestamptz[], $3::uuid[], $4::uuid[], $5::bigint[], $6::bigint[], $7::smallint[],
					$8::smallint[], $9::real[], $10::real[], $11::double precision[], $12::boolean[])
					AS u (time, probe_id, collector_id, seq, epoch, status, code, rtt_ms, loss, value, stale)
				ON CONFLICT DO NOTHING`,
				orgID, rows.times, rows.probes, rows.collectors, rows.seqs, rows.epochs, rows.statuses, rows.codes,
				rows.rtts, rows.losses, rows.values, rows.stale)
			if err != nil {
				return fmt.Errorf("write probe results: %w", err)
			}
			out.Written = int(tag.RowsAffected())
		}
		if err := advanceCursors(ctx, tx, orgID, cursor); err != nil {
			return err
		}
		return recordUsage(ctx, tx, orgID, usage, usageAt)
	})
	return out, err
}

// ingestProbes reads the probes of the batch with the epoch of their
// assignment.
func ingestProbes(ctx context.Context, tx pgx.Tx, orgID string, msgs []ResultsMessage) (map[string]ingestProbe, error) {
	seen := map[string]bool{}
	var ids []string
	for _, m := range msgs {
		for i := range m.Results {
			if id := m.Results[i].ProbeID; !seen[id] && api.IsUUID(id) {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	out := make(map[string]ingestProbe, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text, COALESCE(p.ci_id::text, ''), COALESCE(p.client_id::text, ''),
			a.epoch, COALESCE(a.collector_id::text, '')
		FROM probe p
		LEFT JOIN probe_assignment a ON a.organization_id = p.organization_id AND a.probe_id = p.id
		WHERE p.organization_id = $1 AND p.id = ANY ($2::uuid[])`, orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("read probes of the results: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var p ingestProbe
		var epoch *int64
		if err := rows.Scan(&id, &p.ciID, &p.clientID, &epoch, &p.collector); err != nil {
			return nil, fmt.Errorf("read probes of the results: %w", err)
		}
		if epoch != nil {
			p.epoch, p.assigned = *epoch, true
		}
		out[id] = p
	}
	return out, rows.Err()
}

// ingestCollectors reads the client of every collector of the batch
// (empty for an org-wide collector); a collector missing from the map is
// not in the organization.
func ingestCollectors(ctx context.Context, tx pgx.Tx, orgID string, msgs []ResultsMessage) (map[string]string, error) {
	seen := map[string]bool{}
	var ids []string
	for _, m := range msgs {
		if !seen[m.CollectorID] && api.IsUUID(m.CollectorID) {
			seen[m.CollectorID] = true
			ids = append(ids, m.CollectorID)
		}
	}
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT c.id::text, COALESCE(c.client_id::text, '') FROM collector c
		WHERE c.organization_id = $1 AND c.id = ANY ($2::uuid[])`, orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("read collectors of the results: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, client string
		if err := rows.Scan(&id, &client); err != nil {
			return nil, fmt.Errorf("read collectors of the results: %w", err)
		}
		out[id] = client
	}
	return out, rows.Err()
}

// advanceCursors moves the results cursor of every collector of the batch
// to its highest sequence number (never back); a collector that no longer
// exists has no cursor.
func advanceCursors(ctx context.Context, tx pgx.Tx, orgID string, cursor map[string][2]int64) error {
	if len(cursor) == 0 {
		return nil
	}
	collectors := make([]string, 0, len(cursor))
	for c := range cursor {
		collectors = append(collectors, c)
	}
	sort.Strings(collectors) // a stable lock order between concurrent writers
	seqs := make([]int64, len(collectors))
	epochs := make([]int64, len(collectors))
	for i, c := range collectors {
		seqs[i], epochs[i] = cursor[c][0], max(cursor[c][1], 0)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO probe_ingest_cursor AS k (organization_id, collector_id, stream, epoch, last_seq)
		SELECT $1, u.collector_id, $5, u.epoch, u.seq
		  FROM unnest($2::uuid[], $3::bigint[], $4::bigint[]) AS u (collector_id, seq, epoch)
		  JOIN collector c ON c.organization_id = $1 AND c.id = u.collector_id
		ON CONFLICT (collector_id, stream) DO UPDATE SET
			last_seq = GREATEST(k.last_seq, EXCLUDED.last_seq),
			epoch = CASE WHEN EXCLUDED.last_seq > k.last_seq THEN EXCLUDED.epoch ELSE k.epoch END`,
		orgID, collectors, seqs, epochs, StreamResults); err != nil {
		return fmt.Errorf("advance ingest cursor: %w", err)
	}
	return nil
}

// usageEntry is the credential use a result reports through its labels
// (never a secret: ID and namespace only); false when it reports none, has
// no CI or says nothing about the credential.
func usageEntry(r *probewire.Result, ciID string) (credential.UsageReport, bool) {
	cred, ns := r.Labels[probewire.LabelCredentialID], r.Labels[probewire.LabelCredentialNamespace]
	if cred == "" || ns == "" || ciID == "" {
		return credential.UsageReport{}, false
	}
	e := credential.UsageReport{CIID: ciID, Namespace: ns, CredentialID: cred, OK: r.Status == probewire.StatusOK}
	if !e.OK {
		class, ok := usageClasses[r.Code]
		if !ok {
			return credential.UsageReport{}, false
		}
		e.Cause = class
	}
	return e, true
}

// recordUsage hands the credential uses to credential.RecordUsage: per
// credential, CI and namespace the latest success and the latest failure,
// each at the time it was observed.
func recordUsage(ctx context.Context, tx pgx.Tx, orgID string, usage []credential.UsageReport, at map[int]time.Time) error {
	if len(usage) == 0 {
		return nil
	}
	type key struct {
		cred, ci, ns string
		ok           bool
	}
	latest := map[key]int{}
	for i, e := range usage {
		k := key{e.CredentialID, e.CIID, e.Namespace, e.OK}
		if j, ok := latest[k]; !ok || !at[i].Before(at[j]) {
			latest[k] = i
		}
	}
	idx := make([]int, 0, len(latest))
	for _, i := range latest {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	txCtx := database.ContextWithTx(ctx, tx)
	for _, i := range idx {
		if _, err := credential.RecordUsage(txCtx, tx, &credential.UsageInput{OrganizationID: orgID, FromProbes: true,
			Entries: []credential.UsageReport{usage[i]}, At: at[i]}); err != nil {
			return fmt.Errorf("record credential usage: %w", err)
		}
	}
	return nil
}

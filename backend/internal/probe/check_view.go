package probe

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CheckView is the current state of a probe with what an alarm of it shows
// (ALR-02, WP-986): its CI and client, type, target, the state with reason
// and since (the collector's time), when the change that set it arrived, and
// the effective interval, timeout, down count and thresholds (Resolve; read
// only for the states down and degraded, whose alarm shows them).
type CheckView struct {
	ProbeID    string
	CIID       string
	ClientID   string
	Type       string
	Target     string
	HasState   bool
	State      string
	Reason     string
	Since      time.Time
	ReceivedAt time.Time
	IntervalS  int64
	TimeoutS   int64
	DownCount  int64
	UpCount    int64
	Thresholds map[string]any
}

// ReadCheck reads the check view of a probe in the transaction tx of the
// organization; nil without such a probe.
func ReadCheck(ctx context.Context, tx pgx.Tx, orgID, probeID string) (*CheckView, error) {
	p, err := scanProbe(tx.QueryRow(ctx, `SELECT `+probeColumns+` FROM probe p WHERE p.organization_id = $1 AND p.id = $2`, orgID, probeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read probe: %w", err)
	}
	v := &CheckView{ProbeID: p.ID, CIID: p.CIID, ClientID: p.ClientID, Type: p.Type, Target: p.Target}
	var received *time.Time
	err = tx.QueryRow(ctx, `SELECT s.state, s.reason, s.since,
			(SELECT c.received_at FROM probe_state_change c
			  WHERE c.probe_id = s.probe_id AND c.epoch = s.epoch AND c.seq = s.last_seq
			    AND c.collector_id IS NOT DISTINCT FROM s.collector_id
			  ORDER BY c.received_at DESC LIMIT 1)
		  FROM probe_state s WHERE s.organization_id = $1 AND s.probe_id = $2`, orgID, probeID).
		Scan(&v.State, &v.Reason, &v.Since, &received)
	switch {
	case err == nil:
		v.HasState = true
		if received != nil {
			v.ReceivedAt = *received
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("read probe state: %w", err)
	}
	// The effective configuration only serves the details of an alarm
	// that opens or counts (down, degraded).
	if v.State != "down" && v.State != "degraded" {
		return v, nil
	}
	layers, err := (&pgWriteTx{tx: tx, orgID: orgID}).Layers(ctx, &p)
	if err != nil {
		return nil, err
	}
	spec := Resolve(&p, layers)
	v.IntervalS, v.TimeoutS, v.DownCount, v.UpCount = spec.IntervalS.Int(), spec.TimeoutS.Int(), spec.DownCount.Int(), spec.UpCount.Int()
	if len(spec.Thresholds) > 0 {
		v.Thresholds = make(map[string]any, len(spec.Thresholds))
		for k, f := range spec.Thresholds {
			if f.Value != nil {
				v.Thresholds[k] = f.Value
			}
		}
	}
	return v, nil
}

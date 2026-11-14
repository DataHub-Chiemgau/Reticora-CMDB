package configsnap

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm/configsnap/configtext"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// PGStore keeps the configuration versions in PostgreSQL (table
// config_snapshot, migration 000314) under the row level security of the
// caller's scope.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore returns the PostgreSQL store on pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

var _ Store = (*PGStore)(nil)

// metaColumns are the columns scanMeta reads, in its order (without
// content).
const metaColumns = `id::text, organization_id::text, client_id::text, site_id::text, ci_id::text, collector_id::text,
	collected_at, received_at, source, trigger, platform, sha256, size_bytes, masked_count`

var (
	platformPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func scanMeta(row pgx.Row, extra ...any) (*Snapshot, error) {
	var s Snapshot
	dest := []any{&s.ID, &s.OrganizationID, &s.ClientID, &s.SiteID, &s.CIID, &s.CollectorID,
		&s.CollectedAt, &s.ReceivedAt, &s.Source, &s.Trigger, &s.Platform, &s.SHA256, &s.SizeBytes, &s.MaskedCount}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

// prepare validates snap, sets its hash and size and returns the
// compressed content.
func prepare(snap *Snapshot) ([]byte, error) {
	switch {
	case len(snap.Content) == 0:
		return nil, fmt.Errorf("%w: empty content", ErrInvalid)
	case len(snap.Content) > MaxSize:
		return nil, ErrTooLarge
	case snap.Source != SourceSSH && snap.Source != SourceGNMI:
		return nil, fmt.Errorf("%w: source %q", ErrInvalid, snap.Source)
	case snap.Trigger != TriggerSchedule && snap.Trigger != TriggerSNMP && snap.Trigger != TriggerTrap && snap.Trigger != TriggerGNMI:
		return nil, fmt.Errorf("%w: trigger %q", ErrInvalid, snap.Trigger)
	case !platformPattern.MatchString(snap.Platform):
		return nil, fmt.Errorf("%w: platform", ErrInvalid)
	case snap.MaskedCount < 0:
		return nil, fmt.Errorf("%w: masked_count", ErrInvalid)
	case snap.CollectedAt.IsZero():
		return nil, fmt.Errorf("%w: collected_at", ErrInvalid)
	}
	sum := sha256.Sum256(snap.Content)
	hash := hex.EncodeToString(sum[:])
	if snap.SHA256 != "" && (!sha256Pattern.MatchString(snap.SHA256) || snap.SHA256 != hash) {
		return nil, ErrHashMismatch
	}
	snap.SHA256 = hash
	snap.SizeBytes = len(snap.Content)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(snap.Content); err != nil {
		return nil, fmt.Errorf("configsnap: compress: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("configsnap: compress: %w", err)
	}
	if buf.Len() > MaxSize {
		return nil, ErrTooLarge
	}
	return buf.Bytes(), nil
}

// InsertIfChanged implements Store.
func (s *PGStore) InsertIfChanged(ctx context.Context, orgID string, snap *Snapshot) (stored, previous *Snapshot, created bool, err error) {
	compressed, err := prepare(snap)
	if err != nil {
		return nil, nil, false, err
	}
	err = database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// One writer per CI at a time: the comparison with the latest
		// version and the insert are one step.
		if _, lerr := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('config_snapshot:' || $1::text, 0))`, snap.CIID); lerr != nil {
			return fmt.Errorf("lock ci: %w", lerr)
		}
		latest, lerr := scanMeta(tx.QueryRow(ctx, `SELECT `+metaColumns+` FROM config_snapshot
			WHERE organization_id = $1 AND ci_id = $2 ORDER BY collected_at DESC, id DESC LIMIT 1`, orgID, snap.CIID))
		switch {
		case errors.Is(lerr, ErrNotFound):
		case lerr != nil:
			return fmt.Errorf("read latest version: %w", lerr)
		case latest.SHA256 == snap.SHA256:
			stored = latest
			return nil
		case !snap.CollectedAt.After(latest.CollectedAt):
			// A late delivery is not the current configuration.
			return ErrOutOfOrder
		default:
			previous = latest
		}
		var received any
		if !snap.ReceivedAt.IsZero() {
			received = snap.ReceivedAt
		}
		// Client and site are those of the CI (config_snapshot_scope
		// derives them again); an invisible CI writes nothing.
		row, ierr := scanMeta(tx.QueryRow(ctx, `
			INSERT INTO config_snapshot (organization_id, client_id, site_id, ci_id, collector_id, collected_at, received_at,
			                             source, trigger, platform, sha256, size_bytes, content, masked_count)
			SELECT $1, c.client_id, c.site_id, c.id, $3, $4, COALESCE($5::timestamptz, now()), $6, $7, $8, $9, $10, $11, $12
			  FROM ci c WHERE c.organization_id = $1 AND c.id = $2
			RETURNING `+metaColumns,
			orgID, snap.CIID, snap.CollectorID, snap.CollectedAt, received, snap.Source, snap.Trigger, snap.Platform,
			snap.SHA256, snap.SizeBytes, compressed, snap.MaskedCount))
		if errors.Is(ierr, ErrNotFound) {
			return ErrCINotFound
		}
		if pgErr := (*pgconn.PgError)(nil); errors.As(ierr, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "config_snapshot_ci_collected_key" {
			// A version of the CI with the same collection time exists.
			return ErrOutOfOrder
		}
		if ierr != nil {
			return fmt.Errorf("insert version: %w", ierr)
		}
		row.Content = snap.Content
		stored, created = row, true
		return nil
	})
	if err != nil {
		return nil, nil, false, err
	}
	if !created {
		previous = nil
	}
	return stored, previous, created, nil
}

// Latest implements Store.
func (s *PGStore) Latest(ctx context.Context, orgID, ciID string) (*Snapshot, error) {
	var out *Snapshot
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanMeta(tx.QueryRow(ctx, `SELECT `+metaColumns+` FROM config_snapshot
			WHERE organization_id = $1 AND ci_id = $2 ORDER BY collected_at DESC, id DESC LIMIT 1`, orgID, ciID))
		return err
	})
	return out, err
}

// List implements Store.
func (s *PGStore) List(ctx context.Context, orgID, ciID string, after *Cursor, limit int) (Page, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	var afterAt *time.Time
	var afterID *string
	if after != nil {
		afterAt, afterID = &after.CollectedAt, &after.ID
	}
	var items []Snapshot
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+metaColumns+` FROM config_snapshot
			WHERE organization_id = $1 AND ci_id = $2
			  AND ($3::timestamptz IS NULL OR (collected_at, id) < ($3::timestamptz, $4::uuid))
			ORDER BY collected_at DESC, id DESC LIMIT $5`, orgID, ciID, afterAt, afterID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			snap, err := scanMeta(rows)
			if err != nil {
				return err
			}
			items = append(items, *snap)
		}
		return rows.Err()
	})
	if err != nil {
		return Page{}, fmt.Errorf("configsnap: list: %w", err)
	}
	if len(items) <= limit {
		return Page{Items: items}, nil
	}
	last := items[limit-1]
	return Page{Items: items[:limit], Next: &Cursor{CollectedAt: last.CollectedAt, ID: last.ID}}, nil
}

// Get implements Store.
func (s *PGStore) Get(ctx context.Context, orgID, id string) (*Snapshot, error) {
	var out *Snapshot
	var content []byte
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanMeta(tx.QueryRow(ctx, `SELECT `+metaColumns+`, content FROM config_snapshot
			WHERE organization_id = $1 AND id = $2`, orgID, id), &content)
		return err
	})
	if err != nil {
		return nil, err
	}
	if out.Content, err = configtext.Decompress(content); err != nil {
		return nil, err
	}
	return out, nil
}

package vulnfeed

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// ErrInvalidAdvisory refuses an advisory without id or source.
var ErrInvalidAdvisory = errors.New("advisory needs an id and a source")

// ErrAdvisoryOwnedByOtherSource refuses to replace an advisory another
// source stored under the same id: a feed never takes over the advisory
// and the ranges of another one (CSAF tracking ids are unique only per
// publisher), and deleting a source removes only its own advisories.
var ErrAdvisoryOwnedByOtherSource = errors.New("advisory id belongs to another source")

// PGRepository is the catalog on PostgreSQL. Writes run in the system
// context (database.WithSystemWrite); reads run in the tenant transaction
// of the context's scope, or in the system context without one.
type PGRepository struct{ pool *pgxpool.Pool }

// NewPGRepository returns the catalog on pool (the application pool).
func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

// read runs fn in a read transaction: the tenant transaction of the scope
// in ctx (the version match of an organization) or the system transaction
// (importers and jobs without a scope). The catalog policies let every
// context read.
func (r *PGRepository) read(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if scope, ok := database.TenantScopeFromContext(ctx); ok {
		return database.WithRequestTenant(ctx, r.pool, scope.OrgID, fn)
	}
	return database.WithSystem(ctx, r.pool, fn)
}

// ReplaceAdvisory stores adv and replaces its affected ranges in one
// system transaction: a failing range leaves the advisory and its former
// ranges as they were. The KEV flag only rises here (adv.KEV, a stored
// flag, or another flagged advisory of one of its CVEs); SetKEV alone
// clears it (WP-1119), so a re-import between two KEV syncs keeps it. Vendor and product are normalized. An advisory of
// another source under the same id is left alone
// (ErrAdvisoryOwnedByOtherSource).
func (r *PGRepository) ReplaceAdvisory(ctx context.Context, adv *Advisory, affected []Affected) error {
	if adv == nil || adv.ID == "" || adv.SourceID == "" {
		return ErrInvalidAdvisory
	}
	cves := adv.CVEIDs
	if cves == nil {
		cves = []string{}
	}
	return database.WithSystemWrite(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		var stored string
		err := tx.QueryRow(ctx, `INSERT INTO vuln_advisory (id, source_id, cve_ids, title, severity, cvss_score, cvss_vector, kev,
				published_at, modified_at, url, withdrawn)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7,
				$8 OR EXISTS (SELECT 1 FROM vuln_advisory o WHERE o.kev AND o.cve_ids && $3::text[]),
				$9, $10, $11, $12)
			ON CONFLICT (id) DO UPDATE SET cve_ids = EXCLUDED.cve_ids, title = EXCLUDED.title,
				severity = EXCLUDED.severity, cvss_score = EXCLUDED.cvss_score, cvss_vector = EXCLUDED.cvss_vector,
				kev = vuln_advisory.kev OR EXCLUDED.kev,
				published_at = EXCLUDED.published_at, modified_at = EXCLUDED.modified_at, url = EXCLUDED.url, withdrawn = EXCLUDED.withdrawn
				WHERE vuln_advisory.source_id = EXCLUDED.source_id
			RETURNING id`,
			adv.ID, adv.SourceID, cves, adv.Title, adv.Severity, adv.CVSSScore, adv.CVSSVector, adv.KEV,
			adv.PublishedAt, adv.ModifiedAt, adv.URL, adv.Withdrawn).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) {
			// The id exists under another source: the WHERE of the update
			// kept the row (no takeover).
			return fmt.Errorf("store advisory %s of source %s: %w", adv.ID, adv.SourceID, ErrAdvisoryOwnedByOtherSource)
		}
		if err != nil {
			return fmt.Errorf("store advisory %s: %w", adv.ID, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM vuln_affected WHERE advisory_id = $1`, adv.ID); err != nil {
			return fmt.Errorf("drop ranges of advisory %s: %w", adv.ID, err)
		}
		for i := range affected {
			a := &affected[i]
			scheme := a.VersionScheme
			if scheme == "" {
				scheme = "generic"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO vuln_affected (advisory_id, vendor, product, version_scheme, version_exact,
					version_start, version_start_inclusive, version_end, version_end_inclusive, fixed_version, cpe)
				VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, NULLIF($8, ''), $9, NULLIF($10, ''), NULLIF($11, ''))`,
				adv.ID, Normalize(a.Vendor), Normalize(a.Product), scheme, a.VersionExact, a.VersionStart, a.VersionStartInclusive,
				a.VersionEnd, a.VersionEndInclusive, a.FixedVersion, a.CPE); err != nil {
				return fmt.Errorf("store range %d of advisory %s: %w", i, adv.ID, err)
			}
		}
		return nil
	})
}

// UpdateSource stores the sync state of a source: state, ETag, cursor and
// counters (insert or replace).
func (r *PGRepository) UpdateSource(ctx context.Context, src *Source) error {
	return database.WithSystemWrite(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO vuln_source (id, kind, url, etag, last_modified, cursor, synced_at, documents_total,
				errors_total, last_error, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
			ON CONFLICT (id) DO UPDATE SET kind = EXCLUDED.kind, url = EXCLUDED.url, etag = EXCLUDED.etag,
				last_modified = EXCLUDED.last_modified, cursor = EXCLUDED.cursor, synced_at = EXCLUDED.synced_at,
				documents_total = EXCLUDED.documents_total, errors_total = EXCLUDED.errors_total,
				last_error = EXCLUDED.last_error, updated_at = now()
			RETURNING updated_at`,
			src.ID, src.Kind, src.URL, src.ETag, src.LastModified, src.Cursor, src.SyncedAt, src.DocumentsTotal,
			src.ErrorsTotal, src.LastError).Scan(&src.UpdatedAt)
	})
}

// GetSource returns the stored state of a source (pgx.ErrNoRows wrapped
// when unknown).
func (r *PGRepository) GetSource(ctx context.Context, id string) (*Source, error) {
	var s Source
	err := r.read(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id, kind, url, etag, last_modified, cursor, synced_at, documents_total, errors_total,
				last_error, updated_at FROM vuln_source WHERE id = $1`, id).
			Scan(&s.ID, &s.Kind, &s.URL, &s.ETag, &s.LastModified, &s.Cursor, &s.SyncedAt, &s.DocumentsTotal, &s.ErrorsTotal,
				&s.LastError, &s.UpdatedAt)
	})
	if err != nil {
		return nil, fmt.Errorf("source %s: %w", id, err)
	}
	return &s, nil
}

// ByProductsQuery is the read of ByProducts; it filters on the index
// idx_vuln_affected_product (vendor, product).
const ByProductsQuery = `SELECT a.id, a.source_id, a.cve_ids, a.title, COALESCE(a.severity, ''), a.cvss_score, a.cvss_vector, a.kev,
		a.published_at, a.modified_at, a.url, a.withdrawn,
		f.id, f.vendor, f.product, f.version_scheme, COALESCE(f.version_exact, ''), COALESCE(f.version_start, ''),
		f.version_start_inclusive, COALESCE(f.version_end, ''), f.version_end_inclusive, COALESCE(f.fixed_version, ''),
		COALESCE(f.cpe, '')
	  FROM vuln_affected f
	  JOIN vuln_advisory a ON a.id = f.advisory_id
	 WHERE f.vendor = $1 AND f.product = ANY ($2)
	 ORDER BY a.id, f.id`

// ByProducts returns the affected ranges of a vendor's products with their
// advisories; vendor and products are normalized first.
func (r *PGRepository) ByProducts(ctx context.Context, vendor string, products []string) ([]Match, error) {
	names := make([]string, 0, len(products))
	for _, p := range products {
		if n := Normalize(p); n != "" {
			names = append(names, n)
		}
	}
	out := []Match{}
	if Normalize(vendor) == "" || len(names) == 0 {
		return out, nil
	}
	err := r.read(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, ByProductsQuery, Normalize(vendor), names)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Match
			a, f := &m.Advisory, &m.Affected
			if err := rows.Scan(&a.ID, &a.SourceID, &a.CVEIDs, &a.Title, &a.Severity, &a.CVSSScore, &a.CVSSVector, &a.KEV,
				&a.PublishedAt, &a.ModifiedAt, &a.URL, &a.Withdrawn,
				&f.ID, &f.Vendor, &f.Product, &f.VersionScheme, &f.VersionExact, &f.VersionStart, &f.VersionStartInclusive,
				&f.VersionEnd, &f.VersionEndInclusive, &f.FixedVersion, &f.CPE); err != nil {
				return err
			}
			f.AdvisoryID = a.ID
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("affected ranges of %s: %w", vendor, err)
	}
	return out, nil
}

// likeEscape escapes the wildcards of a LIKE pattern.
var likeEscape = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// SearchProducts returns up to limit (1..100, default 20) vendors and
// products whose normalized name contains q, ordered by vendor and
// product.
func (r *PGRepository) SearchProducts(ctx context.Context, q string, limit int) ([]Product, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	pattern := "%" + likeEscape.Replace(Normalize(q)) + "%"
	out := []Product{}
	err := r.read(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT vendor, product FROM vuln_affected
			 WHERE vendor LIKE $1 OR product LIKE $1 ORDER BY vendor, product LIMIT $2`, pattern, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Product
			if err := rows.Scan(&p.Vendor, &p.Product); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("search products: %w", err)
	}
	return out, nil
}

// CSAFCovered returns per CVE of cves the (vendor, product) pairs that a
// CSAF advisory of that CVE has ranges for (WP-1119 AK4, CSAF first).
func (r *PGRepository) CSAFCovered(ctx context.Context, cves []string) (map[string]map[[2]string]bool, error) {
	out := map[string]map[[2]string]bool{}
	if len(cves) == 0 {
		return out, nil
	}
	err := r.read(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT c.cve, f.vendor, f.product
			  FROM vuln_advisory a
			  JOIN vuln_affected f ON f.advisory_id = a.id
			  CROSS JOIN LATERAL unnest(a.cve_ids) AS c(cve)
			 WHERE a.source_id LIKE 'csaf:%' AND a.cve_ids && $1::text[] AND c.cve = ANY ($1::text[])`, cves)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var cve, vendor, product string
			if err := rows.Scan(&cve, &vendor, &product); err != nil {
				return err
			}
			if out[cve] == nil {
				out[cve] = map[[2]string]bool{}
			}
			out[cve][[2]string{vendor, product}] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("csaf coverage: %w", err)
	}
	return out, nil
}

// RemoveCoveredNVDRanges deletes the ranges of NVD advisories whose vendor
// and product a CSAF advisory of one of the same CVEs covers (WP-1119
// AK4: a CSAF advisory that arrives later wins).
func (r *PGRepository) RemoveCoveredNVDRanges(ctx context.Context) (int64, error) {
	var n int64
	err := database.WithSystemWrite(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM vuln_affected f
			 USING vuln_advisory a
			 WHERE f.advisory_id = a.id AND a.source_id = 'nvd'
			   AND EXISTS (SELECT 1 FROM vuln_advisory c JOIN vuln_affected cf ON cf.advisory_id = c.id
			                WHERE c.source_id LIKE 'csaf:%' AND c.cve_ids && a.cve_ids
			                  AND cf.vendor = f.vendor AND cf.product = f.product)`)
		n = tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("remove nvd ranges covered by csaf: %w", err)
	}
	return n, nil
}

// SetKEV sets kev for every advisory with a CVE of cves and clears it for
// the others (WP-1119 AK5); it returns the number of changed advisories.
func (r *PGRepository) SetKEV(ctx context.Context, cves []string) (int64, error) {
	if cves == nil {
		cves = []string{}
	}
	var n int64
	err := database.WithSystemWrite(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE vuln_advisory SET kev = (cve_ids && $1::text[])
			 WHERE kev IS DISTINCT FROM (cve_ids && $1::text[])`, cves)
		n = tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("set kev: %w", err)
	}
	return n, nil
}

// finishBundleSource implements bundleSourceFinisher: the state of the
// source bundle is written only when the stored one is not newer.
func (r *PGRepository) finishBundleSource(ctx context.Context, src *Source) (bool, error) {
	done := false
	err := database.WithSystemWrite(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO vuln_source (id, kind, url, etag, last_modified, cursor, synced_at, documents_total,
				errors_total, last_error, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
			ON CONFLICT (id) DO UPDATE SET kind = EXCLUDED.kind, url = EXCLUDED.url, etag = EXCLUDED.etag,
				last_modified = EXCLUDED.last_modified, cursor = EXCLUDED.cursor, synced_at = EXCLUDED.synced_at,
				documents_total = EXCLUDED.documents_total, errors_total = EXCLUDED.errors_total,
				last_error = EXCLUDED.last_error, updated_at = now()
				WHERE vuln_source.synced_at IS NULL OR vuln_source.synced_at <= EXCLUDED.synced_at
			RETURNING updated_at`,
			src.ID, src.Kind, src.URL, src.ETag, src.LastModified, src.Cursor, src.SyncedAt, src.DocumentsTotal,
			src.ErrorsTotal, src.LastError).Scan(&src.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		done = err == nil
		return err
	})
	return done, err
}

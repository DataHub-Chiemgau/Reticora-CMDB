package export

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// OrgTable is one table of the full export of an organization (TLC-05,
// WP-1149): a table or hypertable of the schema public with the column
// organization_id, or the table organization itself, whose row of the
// organization is its column id.
type OrgTable struct {
	Name string
	// OrgColumn is the column that names the organization: organization_id,
	// for the table organization id.
	OrgColumn string
	// Columns are the exported columns in their order in the table;
	// Omitted are the secret columns left out (IsSecretColumn).
	Columns []string
	Omitted []string
	// PrimaryKey are the columns of the primary key with their types
	// (format_type), in key order; empty for a table without one, whose
	// batches go by (tableoid, ctid).
	PrimaryKey []KeyColumn
	// Source is the relation the rows are read from: the table, or for a
	// table the application role may not read directly its
	// security-barrier view <table>_v (metric_sample, migration 000071:
	// TimescaleDB refuses row level security on the compressed
	// hypertable). A view has no ctid; its rows are read in the order of
	// the view.
	Source string
}

// FromView reports whether the rows are read through the view of the
// table.
func (t *OrgTable) FromView() bool { return t.Source != t.Name }

// KeyColumn is one column of a primary key.
type KeyColumn struct {
	Name string
	Type string
}

// orgExportExcluded are the tables with organization_id the export leaves
// out, each with its reason. Every other one is exported (OrgTables); a
// new table is exported without a code change.
var orgExportExcluded = map[string]string{
	"org_dek":               "key material: the wrapped data key of the organization is never exported",
	"pii_subject":           "exported decrypted as its own file pii_subject.jsonl (E-165), never with its ciphertext",
	"search_document":       "derived from the other tables by the search indexer; a reindex rebuilds it",
	"search_document_field": "derived from the other tables by the search indexer; a reindex rebuilds it",
}

// ExcludedOrgTables returns the tables the export leaves out with their
// reasons (a copy).
func ExcludedOrgTables() map[string]string {
	out := make(map[string]string, len(orgExportExcluded))
	for k, v := range orgExportExcluded {
		out[k] = v
	}
	return out
}

// orgExportSecretColumns are the secret columns (table.column) the naming
// rule of IsSecretColumn does not catch, each with its reason.
var orgExportSecretColumns = map[string]string{
	"credential.ciphertext":                "the encrypted credential payload",
	"notification_channel.config_dek":      "the wrapped data key of the channel configuration",
	"webhook_subscription.previous_secret": "the signing secret of a webhook during its rotation",
	"webhook_subscription.headers":         "custom request headers, which carry tokens of the receiver",
	"pii_subject.search_tokens":            "the blind index of the vault, derived from the data key",
}

// secretSuffixes and secretPrefixes are the naming rule of secret columns.
var (
	secretSuffixes = []string{"_hash", "_enc", "_ciphertext"}
	secretPrefixes = []string{"encrypted_", "secret"}
)

// IsSecretColumn reports whether the column of table holds a secret the
// export leaves out (AK2): its name ends in _hash, _enc or _ciphertext,
// starts with encrypted_ or secret, or it is listed in
// orgExportSecretColumns.
func IsSecretColumn(table, column string) bool {
	c := strings.ToLower(column)
	for _, s := range secretSuffixes {
		if strings.HasSuffix(c, s) {
			return true
		}
	}
	for _, p := range secretPrefixes {
		if strings.HasPrefix(c, p) {
			return true
		}
	}
	_, listed := orgExportSecretColumns[strings.ToLower(table)+"."+c]
	return listed
}

// orgCatalogSQL lists the tables and hypertables of the schema public with
// the column organization_id (partitions and the chunks of a hypertable
// are part of their parent), and the table organization: name, columns in
// their order with their types, the columns of the primary key in key
// order and the relation to read (the table, or its view <table>_v when
// the role may only read that). The operator tables have no
// organization_id and do not appear.
const orgCatalogSQL = `
SELECT c.relname::text,
       (SELECT array_agg(a.attname::text ORDER BY a.attnum) FROM pg_attribute a
         WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped),
       COALESCE((SELECT array_agg(a.attname::text ORDER BY k.ord) FROM pg_index i
                   CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
                   JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
                  WHERE i.indrelid = c.oid AND i.indisprimary), '{}'),
       COALESCE((SELECT array_agg(format_type(a.atttypid, a.atttypmod) ORDER BY k.ord) FROM pg_index i
                   CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
                   JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
                  WHERE i.indrelid = c.oid AND i.indisprimary), '{}'),
       CASE WHEN has_table_privilege(c.oid, 'SELECT') THEN c.relname::text
            WHEN has_table_privilege(to_regclass('public.' || quote_ident(c.relname || '_v')), 'SELECT')
              THEN c.relname::text || '_v'
            ELSE c.relname::text END
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
   AND (c.relname = 'organization'
        OR EXISTS (SELECT 1 FROM pg_attribute a
                    WHERE a.attrelid = c.oid AND a.attname = 'organization_id' AND NOT a.attisdropped))
 ORDER BY c.relname`

// OrgTables reads the tables of the full export from pg_catalog in the
// caller's transaction (AK1): every table and hypertable of public with
// organization_id and the table organization, without the entries of
// orgExportExcluded, each with its exported and omitted columns and its
// primary key, sorted by name. pii_subject is exported on its own
// (AK4).
func OrgTables(ctx context.Context, tx pgx.Tx) ([]OrgTable, error) {
	all, err := orgCatalog(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := make([]OrgTable, 0, len(all))
	for i := range all {
		if _, excluded := orgExportExcluded[all[i].Name]; !excluded {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// orgCatalog is OrgTables with the excluded tables.
func orgCatalog(ctx context.Context, tx pgx.Tx) ([]OrgTable, error) {
	rows, err := tx.Query(ctx, orgCatalogSQL)
	if err != nil {
		return nil, fmt.Errorf("org export catalog: %w", err)
	}
	defer rows.Close()
	var out []OrgTable
	for rows.Next() {
		var t OrgTable
		var columns, keys, keyTypes []string
		if err := rows.Scan(&t.Name, &columns, &keys, &keyTypes, &t.Source); err != nil {
			return nil, fmt.Errorf("org export catalog: %w", err)
		}
		t.OrgColumn = "organization_id"
		if t.Name == "organization" {
			t.OrgColumn = "id"
		}
		for _, c := range columns {
			if IsSecretColumn(t.Name, c) {
				t.Omitted = append(t.Omitted, c)
				continue
			}
			t.Columns = append(t.Columns, c)
		}
		for i, k := range keys {
			t.PrimaryKey = append(t.PrimaryKey, KeyColumn{Name: k, Type: keyTypes[i]})
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("org export catalog: %w", err)
	}
	return out, nil
}

// CheckCatalogComplete compares the tables with organization_id a catalog
// query found (catalog) with the tables of an export (tables): every one is
// exported or listed with a reason in orgExportExcluded, and every
// exclusion names a table of the catalog. It returns the gaps, nil when
// there are none (TestOrgExportCatalogComplete).
func CheckCatalogComplete(catalog []string, tables []OrgTable) error {
	exported := make(map[string]bool, len(tables))
	for i := range tables {
		exported[tables[i].Name] = true
	}
	var missing, stale, unreasoned []string
	for _, name := range catalog {
		_, excluded := orgExportExcluded[name]
		if !exported[name] && !excluded {
			missing = append(missing, name)
		}
	}
	for name, reason := range orgExportExcluded {
		if strings.TrimSpace(reason) == "" {
			unreasoned = append(unreasoned, name)
		}
		if !slices.Contains(catalog, name) {
			stale = append(stale, name)
		}
	}
	if len(missing)+len(stale)+len(unreasoned) == 0 {
		return nil
	}
	sort.Strings(stale)
	sort.Strings(unreasoned)
	return fmt.Errorf("org export catalog: not exported and not excluded: %v; excluded but not in the catalog: %v; excluded without reason: %v",
		missing, stale, unreasoned)
}

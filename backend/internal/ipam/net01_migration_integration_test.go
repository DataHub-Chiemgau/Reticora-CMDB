package ipam_test

// WP-592 (NET-01; F-310, E-94): migration 000110 makes the interface name
// unique per CI and renames duplicates first. Earlier builds numbered every
// duplicate by its rank without looking at the names that exist already, so
// an upgrade with 'eth0' twice and an existing 'eth0 (1)' stopped at the
// unique constraint. The test runs the down and up file of 000110 on such
// rows inside a rolled-back transaction (probe net01-dup.sql of the
// verification).
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestNet01DedupeAvoidsExistingSuffix' ./internal/ipam/

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestNet01DedupeAvoidsExistingSuffix: the upgrade succeeds and the names
// are unique per CI. The oldest row of a name keeps it, an existing suffixed
// name keeps its row, and every other duplicate takes the next free number
// of its CI and base name. Names of other CIs do not count.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestNet01DedupeAvoidsExistingSuffix(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	f := scopetest.Seed(t, "74")
	bg := context.Background()
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	down, up := read("000110_interface_net01.down.sql"), read("000110_interface_net01.up.sql")
	probe := f.CI(t, f.OrgA, "", "net01-probe")
	triple := f.CI(t, f.OrgA, "", "net01-triple")
	suffixed := f.CI(t, f.OrgA, "", "net01-suffixed")
	other := f.CI(t, f.OrgA, "", "net01-other")

	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	// Back to the schema before 000110: former column names, no unique name.
	if _, err = tx.Exec(bg, down); err != nil {
		t.Fatalf("000110 down: %v", err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seq := 0
	insert := func(ciID, name string) string {
		t.Helper()
		seq++
		var id string
		if insertErr := tx.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name, created_at)
			VALUES ($1, $2, $3, $4) RETURNING id::text`, f.OrgA, ciID, name, base.Add(time.Duration(seq)*time.Minute)).Scan(&id); insertErr != nil {
			t.Fatalf("insert %s: %v", name, insertErr)
		}
		return id
	}
	// The probe: 'vfy-eth0' twice and an existing 'vfy-eth0 (1)'.
	probeOldest := insert(probe, "vfy-eth0")
	probeSecond := insert(probe, "vfy-eth0")
	probeExisting := insert(probe, "vfy-eth0 (1)")
	// Three duplicates around an existing '(2)': the numbers skip it.
	tripleIDs := []string{insert(triple, "vfy-eth1"), insert(triple, "vfy-eth1"), insert(triple, "vfy-eth1")}
	tripleExisting := insert(triple, "vfy-eth1 (2)")
	// A suffixed name that is itself duplicated, next to duplicates of its
	// base name.
	suffixedIDs := []string{insert(suffixed, "vfy-eth0"), insert(suffixed, "vfy-eth0 (1)"),
		insert(suffixed, "vfy-eth0"), insert(suffixed, "vfy-eth0 (1)")}
	// Another CI with the same names: they stay, they are not duplicates.
	otherIDs := []string{insert(other, "vfy-eth0"), insert(other, "vfy-eth0 (2)")}

	if _, err = tx.Exec(bg, up); err != nil {
		t.Fatalf("000110 up (the upgrade): %v", err)
	}

	names := map[string]string{}
	rows, err := tx.Query(bg, `SELECT id::text, name FROM network_interface WHERE organization_id = $1`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		names[id] = name
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		ids  []string
		want []string
	}{
		{"probe", []string{probeOldest, probeSecond, probeExisting}, []string{"vfy-eth0", "vfy-eth0 (2)", "vfy-eth0 (1)"}},
		{"three duplicates", append(slices.Clone(tripleIDs), tripleExisting),
			[]string{"vfy-eth1", "vfy-eth1 (1)", "vfy-eth1 (3)", "vfy-eth1 (2)"}},
		{"duplicated suffix", suffixedIDs, []string{"vfy-eth0", "vfy-eth0 (1)", "vfy-eth0 (2)", "vfy-eth0 (1) (1)"}},
		{"other CI", otherIDs, []string{"vfy-eth0", "vfy-eth0 (2)"}},
	} {
		got := make([]string, len(c.ids))
		for i, id := range c.ids {
			got[i] = names[id]
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: names %q, want %q", c.name, got, c.want)
		}
	}
	var unique bool
	if err = tx.QueryRow(bg, `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'network_interface_ci_name_key'
		AND conrelid = 'network_interface'::regclass AND contype = 'u')`).Scan(&unique); err != nil || !unique {
		t.Errorf("unique constraint network_interface_ci_name_key after the upgrade: %v %v", unique, err)
	}
}

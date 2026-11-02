package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
)

// TestRunReportsEveryOrganization covers AUD-04 (WP-132): audit-verify
// checks each organization's chain, names the first violation of a broken
// one and exits 1 for a break, 2 for an unreadable chain.
func TestRunReportsEveryOrganization(t *testing.T) {
	results := map[string]audit.VerifyResult{
		"org-a": {Intact: true, Checked: 3},
		"org-b": {Intact: false, Checked: 4, BrokenID: "e-5", BrokenAt: 5, BrokenReason: "sequence gap"},
	}
	verify := func(_ context.Context, org string) (audit.VerifyResult, error) {
		if r, ok := results[org]; ok {
			return r, nil
		}
		return audit.VerifyResult{}, errors.New("no access")
	}
	var out bytes.Buffer
	if code := run(context.Background(), &out, []string{"org-a"}, verify); code != 0 {
		t.Errorf("intact: exit %d", code)
	}
	if code := run(context.Background(), &out, []string{"org-a", "org-b"}, verify); code != 1 {
		t.Errorf("broken: exit %d", code)
	}
	if !strings.Contains(out.String(), "BROKEN for organization org-b: entry e-5 (seq 5): sequence gap") {
		t.Errorf("output:\n%s", out.String())
	}
	if code := run(context.Background(), &out, []string{"org-b", "org-c"}, verify); code != 2 {
		t.Errorf("unreadable: exit %d", code)
	}
}

// TestRunNamesSealedEntries covers WP-680 (AUD-04, SEC-05): a chain with
// entries the pii.backfill run sealed is intact (exit 0), and the output
// names the sealed entries and how many of them the vault did not confirm.
func TestRunNamesSealedEntries(t *testing.T) {
	verify := func(_ context.Context, _ string) (audit.VerifyResult, error) {
		return audit.VerifyResult{Intact: true, Checked: 5, Sealed: 2, Unconfirmed: 1}, nil
	}
	var out bytes.Buffer
	if code := run(context.Background(), &out, []string{"org-a"}, verify); code != 0 {
		t.Errorf("sealed entries: exit %d", code)
	}
	if want := "audit chain intact for organization org-a (5 entries verified, 2 of them sealed, " +
		"1 sealed entries not confirmed against the vault)\n"; out.String() != want {
		t.Errorf("output %q, want %q", out.String(), want)
	}
}

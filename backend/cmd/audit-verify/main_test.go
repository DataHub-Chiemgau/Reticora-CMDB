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

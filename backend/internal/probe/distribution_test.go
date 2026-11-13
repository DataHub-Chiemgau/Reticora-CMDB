package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The answers of the distribution without a database (PRB-11, WP-948).

const (
	dOrg       = "11111111-1111-4111-8111-111111111111"
	dCollector = "22222222-2222-4222-8222-222222222222"
)

// specsOf returns n resolved icmp probes with type defaults.
func resolvedOf(n int) []Resolved {
	out := make([]Resolved, n)
	for i := range out {
		p := Probe{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), Type: "icmp", Target: fmt.Sprintf("10.%d.%d.%d", i/65536, i/256%256, i%256)}
		out[i] = Resolved{Probe: p, Effective: Resolve(&p, Layers{}), Epoch: 7}
	}
	return out
}

func TestAssignmentSetVersionNotModified(t *testing.T) {
	built, version := 0, int64(5)
	pages := func() ([]probewire.AssignmentSet, error) {
		built++
		return probewire.Paginate(dCollector, version, nil)
	}
	got, err := respond(dCollector, 5, &probewire.AssignmentRequest{IfNoneMatch: 5, Page: 1}, pages)
	if err != nil {
		t.Fatal(err)
	}
	if nm, ok := got.(probewire.NotModified); !ok || !nm.NotModified || nm.Version != 5 || built != 0 {
		t.Fatalf("same version: %#v (built %d), want not_modified without building", got, built)
	}
	raw, _ := json.Marshal(got)
	if string(raw) != `{"not_modified":true,"version":5}` {
		t.Errorf("not modified body %s", raw)
	}
	version = 6
	got, err = respond(dCollector, 6, &probewire.AssignmentRequest{IfNoneMatch: 5, Page: 1}, pages)
	if err != nil {
		t.Fatal(err)
	}
	if set, ok := got.(probewire.AssignmentSet); !ok || set.Version != 6 || set.Page != 1 || built != 1 {
		t.Errorf("new version: %#v, want page 1 of version 6", got)
	}
}

func TestAssignmentSetPagingStaysUnderPayloadLimit(t *testing.T) {
	specs, skipped := assignmentSpecs(resolvedOf(10000), nil, nil)
	if len(skipped) != 0 || len(specs) != 10000 {
		t.Fatalf("%d specs, %d skipped", len(specs), len(skipped))
	}
	pages, err := probewire.Paginate(dCollector, 3, specs)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for i, p := range pages {
		raw, err := probewire.Encode(&p)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > probewire.MaxPageBytes || len(p.Probes) > probewire.MaxPageProbes {
			t.Errorf("page %d: %d bytes, %d probes", i+1, len(raw), len(p.Probes))
		}
		if (i+1 < len(pages)) != (p.NextPage == i+2) {
			t.Errorf("page %d: next_page %d", i+1, p.NextPage)
		}
		total += len(p.Probes)
	}
	if total != 10000 || len(pages) < 5 {
		t.Errorf("%d probes on %d pages", total, len(pages))
	}
	if s := specs[0]; s.IntervalMS != 30000 || s.TimeoutMS != 10000 || s.DownCount != 5 || s.Epoch != 7 {
		t.Errorf("spec %+v, want the icmp defaults with the epoch", s)
	}
}

func TestAssignmentSetVersionChangedDuringPaging(t *testing.T) {
	pages := func() ([]probewire.AssignmentSet, error) {
		specs, _ := assignmentSpecs(resolvedOf(3000), nil, nil)
		return probewire.Paginate(dCollector, 9, specs)
	}
	got, err := respond(dCollector, 9, &probewire.AssignmentRequest{Page: 2, Version: 8}, pages)
	if err != nil {
		t.Fatal(err)
	}
	set, ok := got.(probewire.AssignmentSet)
	if !ok || !set.VersionChanged || set.Version != 9 || set.NextPage != 1 || len(set.Probes) != 0 {
		t.Fatalf("page 2 of version 8 at version 9: %#v, want version_changed", got)
	}
	got, _ = respond(dCollector, 9, &probewire.AssignmentRequest{Page: 2, Version: 9}, pages)
	if set, ok := got.(probewire.AssignmentSet); !ok || set.VersionChanged || set.Page != 2 || len(set.Probes) == 0 {
		t.Errorf("page 2 of the current version: %#v", got)
	}
	// Credentials outside the bundle skip the probe.
	res := resolvedOf(2)
	specs, skipped := assignmentSpecs(res, map[string][]string{res[0].Probe.ID: {"c1"}, res[1].Probe.ID: {"c2"}}, map[string]bool{"c2": true})
	if len(specs) != 1 || specs[0].CredentialIDs[0] != "c1" || len(skipped) != 1 || skipped[0].Reason != probewire.SkipCredentialMissing {
		t.Errorf("specs %+v skipped %+v", specs, skipped)
	}
}

// fakeFeatures answers the status of the monitoring feature.
type fakeFeatures struct {
	status entitlement.Status
	err    error
}

func (f fakeFeatures) Status(context.Context, string, string) (entitlement.Entitlement, entitlement.Status, error) {
	return entitlement.Entitlement{}, f.status, f.err
}

func TestDistributionRefusesDisabledPausedAndUnknownCollector(t *testing.T) {
	for _, c := range []struct {
		err      error
		code     int
		failures int
	}{
		{api.ErrNotFound, http.StatusNotFound, 0},
		{discovery.ErrCollectorDisabled, http.StatusForbidden, 0},
		{discovery.ErrCollectorPaused, http.StatusForbidden, 0},
		{errors.New("database down"), http.StatusInternalServerError, 1},
	} {
		cycles := observability.NewCycles("test_probe_distribution")
		d := NewDistributor(nil, func(context.Context, string, string) error { return c.err }).WithCycles(cycles)
		got := d.Answer(context.Background(), dOrg, dCollector, []byte(`{"page":1}`))
		if r, ok := got.(Reply); !ok || r.Status != c.code {
			t.Errorf("%v: %#v, want %d", c.err, got, c.code)
		}
		// A failed lookup counts as a failed cycle, a refusal does not (AK3).
		if n := cycles.Pass(context.Background()); n != c.failures {
			t.Errorf("%v: %d failed cycles, want %d", c.err, n, c.failures)
		}
	}
	if _, _, ok := probeSubject("collector."+dOrg+"."+dCollector+discovery.ProbesRequestSuffix, discovery.ProbesRequestSuffix); !ok {
		t.Error("the subject of the request is not read")
	}
}

func TestDistributionRefusesWithoutMonitoringFeature(t *testing.T) {
	ok := func(context.Context, string, string) error { return nil }
	cycles := observability.NewCycles("test_probe_distribution")
	d := NewDistributor(nil, ok).WithFeatures(fakeFeatures{status: entitlement.StatusNotEntitled}).WithCycles(cycles)
	got := d.Answer(context.Background(), dOrg, dCollector, nil)
	if r, isReply := got.(Reply); !isReply || r.Status != http.StatusForbidden || r.Type != api.ProblemFeatureNotEntitled {
		t.Errorf("without monitoring: %#v, want 403 feature-not-entitled", got)
	}
	if n := cycles.Pass(context.Background()); n != 0 {
		t.Errorf("refusal without monitoring: %d failed cycles, want 0", n)
	}
	d = NewDistributor(nil, ok).WithFeatures(fakeFeatures{err: errors.New("lookup failed")}).WithCycles(cycles)
	if r, isReply := d.Answer(context.Background(), dOrg, dCollector, nil).(Reply); !isReply || r.Status != http.StatusInternalServerError {
		t.Errorf("failed lookup: %#v, want 500 (fail-closed)", r)
	}
	if n := cycles.Pass(context.Background()); n != 1 {
		t.Errorf("failed entitlement lookup: %d failed cycles, want 1", n)
	}
}

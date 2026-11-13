package export_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/importer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// WP-921 (IPM-09), without a database: the filters of an IPAM export
// (csv only, no CI filter, only the filters of the entity), the header of
// both files equal to id and the targets of the IPAM import, the manifest
// without omitted fields, the rendering page by page with the formula
// guard and the prepared field-permission hook with an empty policy.
//
// Retest:
//   cd backend && go test ./internal/export/ -run TestIPAMExport

func TestIPAMExportValidatesFilters(t *testing.T) {
	yes := true
	uuid := "00000000-0000-4000-8000-000000000001"
	subnets := func(p *export.IPAMFilters) export.JobFilters {
		return export.JobFilters{Entity: export.EntitySubnet, IPAM: p}
	}
	addresses := func(p *export.IPAMFilters) export.JobFilters {
		return export.JobFilters{Entity: export.EntityIPAddress, IPAM: p}
	}
	cases := []struct {
		name    string
		format  string
		filters export.JobFilters
		want    string
	}{
		{"subnet csv", "csv", subnets(nil), ""},
		{"address csv with filters", "csv", addresses(&export.IPAMFilters{State: "active", Type: "static", Origin: "import", Q: "10.1"}), ""},
		{"subnet root and pool", "csv", subnets(&export.IPAMFilters{ParentID: "root", IsPool: &yes, Contains: "10.0.0.1"}), ""},
		{"json", "json", subnets(nil), "csv only"},
		{"datev", "datev", addresses(nil), "csv only"},
		{"ci status", "csv", export.JobFilters{Entity: export.EntitySubnet, Status: "active"}, "do not apply"},
		{"ci type", "csv", export.JobFilters{Entity: export.EntityIPAddress, CITypeID: uuid}, "do not apply"},
		{"ci ids", "csv", export.JobFilters{Entity: export.EntitySubnet, IDs: []string{uuid}}, "do not apply"},
		{"top-level client", "csv", export.JobFilters{Entity: export.EntitySubnet, ClientID: uuid}, "do not apply"},
		{"ipam with ci", "csv", export.JobFilters{IPAM: &export.IPAMFilters{}}, "filters.ipam applies"},
		{"address filter on subnets", "csv", subnets(&export.IPAMFilters{State: "active"}), "ip_address only"},
		{"subnet filter on addresses", "csv", addresses(&export.IPAMFilters{SectionID: uuid}), "subnet only"},
		{"bad uuid", "csv", subnets(&export.IPAMFilters{SectionID: "lab"}), "section_id must be a UUID"},
		{"bad state", "csv", addresses(&export.IPAMFilters{State: "up"}), "state must be"},
		{"bad contains", "csv", subnets(&export.IPAMFilters{Contains: "10.0.0.0/8"}), "contains must be"},
		{"unknown entity", "csv", export.JobFilters{Entity: "vlan"}, "must be 'ci', 'asset', 'subnet' or 'ip_address'"},
	}
	for _, tc := range cases {
		req := export.CreateJobRequest{Format: tc.format, Filters: tc.filters}
		err := req.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v, want accepted", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want an error with %q", tc.name, err, tc.want)
		}
	}
}

func TestIPAMExportColumnsAreTheImportTargets(t *testing.T) {
	if want := append([]string{"id"}, importer.SubnetTargets...); !slices.Equal(export.SubnetExportColumns, want) {
		t.Errorf("subnet columns %v, want %v", export.SubnetExportColumns, want)
	}
	if want := append([]string{"id"}, importer.AddressTargets...); !slices.Equal(export.AddressExportColumns, want) {
		t.Errorf("address columns %v, want %v", export.AddressExportColumns, want)
	}
	for _, entity := range []string{export.EntitySubnet, export.EntityIPAddress} {
		m := export.IPAMManifest(entity)
		if m.Format != "csv" || m.Omitted == nil || len(m.Omitted) != 0 || len(m.Fields) == 0 || m.Fields[0] != "id" {
			t.Errorf("manifest of %s: %+v, want all columns and omitted empty", entity, m)
		}
	}
}

// fakeIPAMRows pages through n subnets and two addresses.
type fakeIPAMRows struct {
	subnets int
	calls   []string
	// queries are the q of the first page of each call.
	queries []string
}

func (f *fakeIPAMRows) ExportSubnets(_ context.Context, _ string, filter *ipam.SubnetFilter, after string, limit int) ([]ipam.SubnetExportRow, error) {
	f.calls = append(f.calls, fmt.Sprintf("subnets after %q root %v", after, filter.Root))
	if after == "" {
		f.queries = append(f.queries, filter.Q)
	}
	var out []ipam.SubnetExportRow
	for i := range f.subnets {
		id := fmt.Sprintf("s%05d", i)
		if id > after && len(out) < limit {
			out = append(out, ipam.SubnetExportRow{ID: id, Section: "default", CIDR: fmt.Sprintf("10.%d.%d.0/24", i/256, i%256), VRF: "global"})
		}
	}
	return out, nil
}

func (f *fakeIPAMRows) ExportAddresses(_ context.Context, _ string, filter *ipam.IPFilter, after string, _ int) ([]ipam.AddressExportRow, error) {
	f.calls = append(f.calls, fmt.Sprintf("addresses after %q state %s", after, filter.State))
	if after == "" {
		f.queries = append(f.queries, filter.Q)
	}
	if after != "" {
		return nil, nil
	}
	return []ipam.AddressExportRow{
		{ID: "a1", Address: "10.0.0.1", Hostname: "gw", MAC: "02:00:00:00:00:01", State: "active", IsGateway: true, VRF: "global",
			Subnet: "10.0.0.0/24", Type: "static"},
		{ID: "a2", Address: "10.0.0.9", Description: "=cmd()", State: "unknown", VRF: "global", Subnet: "10.0.0.0/24", Type: "discovered"},
	}, nil
}

// readIPAMCSV reads a file with the CSV parser of the import: it detects
// the BOM and the separator as an import of the file would.
func readIPAMCSV(t *testing.T, data []byte) [][]string {
	t.Helper()
	if !bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("the file starts without BOM")
	}
	r, err := importer.OpenCSV(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	rows := [][]string{slices.Clone(r.Header())}
	for {
		row, err := r.Next()
		if errors.Is(err, io.EOF) {
			return rows
		}
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, slices.Clone(row.Cells))
	}
}

func TestIPAMExportRendersPageByPage(t *testing.T) {
	src := &fakeIPAMRows{subnets: 1001}
	job := &export.Job{OrganizationID: "org", Format: "csv", Filters: export.JobFilters{Entity: export.EntitySubnet,
		IPAM: &export.IPAMFilters{ParentID: "root"}}}
	var buf bytes.Buffer
	n, manifest, err := export.RenderIPAM(context.Background(), src, job, nil, &buf)
	if err != nil || n != 1001 {
		t.Fatalf("render: %d rows, %v", n, err)
	}
	rows := readIPAMCSV(t, buf.Bytes())
	if !slices.Equal(rows[0], export.SubnetExportColumns) || len(rows) != 1002 || rows[1][0] != "s00000" || rows[1001][0] != "s01000" {
		t.Fatalf("header %v, %d rows", rows[0], len(rows))
	}
	if want := []string{`subnets after "" root true`, `subnets after "s00999" root true`}; !slices.Equal(src.calls, want) {
		t.Errorf("pages %v, want %v", src.calls, want)
	}
	if !slices.Equal(manifest.Fields, export.SubnetExportColumns) || len(manifest.Omitted) != 0 {
		t.Errorf("manifest %+v", manifest)
	}

	job.Filters = export.JobFilters{Entity: export.EntityIPAddress, IPAM: &export.IPAMFilters{State: "active"}}
	buf.Reset()
	if n, _, err = export.RenderIPAM(context.Background(), src, job, nil, &buf); err != nil || n != 2 {
		t.Fatalf("render addresses: %d, %v", n, err)
	}
	rows = readIPAMCSV(t, buf.Bytes())
	want := [][]string{
		export.AddressExportColumns,
		{"a1", "10.0.0.1", "gw", "", "02:00:00:00:00:01", "active", "1", "global", "10.0.0.0/24", "static"},
		// discovered is no type of an import; a formula is neutralised.
		{"a2", "10.0.0.9", "", "'=cmd()", "", "unknown", "", "global", "10.0.0.0/24", ""},
	}
	if !slices.EqualFunc(rows, want, slices.Equal) {
		t.Errorf("addresses %q, want %q", rows, want)
	}
}

func TestIPAMExportWithoutSourceFails(t *testing.T) {
	job := &export.Job{Format: "csv", Filters: export.JobFilters{Entity: export.EntitySubnet}}
	_, _, err := export.RenderIPAM(context.Background(), nil, job, nil, &bytes.Buffer{})
	if !errors.Is(err, export.ErrIPAMExportUnavailable) {
		t.Fatalf("render without source: %v, want ErrIPAMExportUnavailable", err)
	}
}

// The renderer filters through permission.Readable once an object kind of
// IPAM exists (ipamCell); without a kind every cell stays: a render with
// the empty policy writes the same file as one without policy, and the
// manifest omits nothing.
func TestIPAMExportFieldPermissionHook(t *testing.T) {
	// The empty policy: no field permissions, every object permission.
	empty := permission.UnrestrictedPolicy(identity.AllPermissions())
	for _, entity := range []string{export.EntitySubnet, export.EntityIPAddress} {
		job := &export.Job{OrganizationID: "org", Format: "csv", Filters: export.JobFilters{Entity: entity}}
		var plain, filtered bytes.Buffer
		if _, _, err := export.RenderIPAM(context.Background(), &fakeIPAMRows{subnets: 3}, job, nil, &plain); err != nil {
			t.Fatal(err)
		}
		_, manifest, err := export.RenderIPAM(context.Background(), &fakeIPAMRows{subnets: 3}, job, empty, &filtered)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(plain.Bytes(), filtered.Bytes()) || len(readIPAMCSV(t, filtered.Bytes())) < 3 {
			t.Errorf("%s with the empty policy:\n%s\nwant\n%s", entity, filtered.String(), plain.String())
		}
		if len(manifest.Omitted) != 0 {
			t.Errorf("%s: omitted %+v, want none", entity, manifest.Omitted)
		}
	}
}

// filters.ipam.q is trimmed like the lists of WP-909 trim it.
func TestIPAMExportTrimsQ(t *testing.T) {
	src := &fakeIPAMRows{subnets: 1}
	for _, entity := range []string{export.EntitySubnet, export.EntityIPAddress} {
		job := &export.Job{OrganizationID: "org", Format: "csv", Filters: export.JobFilters{Entity: entity,
			IPAM: &export.IPAMFilters{Q: "  10.93 "}}}
		if _, _, err := export.RenderIPAM(context.Background(), src, job, nil, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"10.93", "10.93"}; !slices.Equal(src.queries, want) {
		t.Errorf("q %q, want %q", src.queries, want)
	}
}

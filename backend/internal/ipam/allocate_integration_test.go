package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestAllocationOnPostgreSQL covers the atomic allocation of IPM-03
// (WP-904) against PostgreSQL:
//   - 70 parallel allocations on an empty /26 give the 62 host addresses
//     10.0.0.1 to 10.0.0.62 once each and 8 times 409 subnet-exhausted;
//   - the gateway and a reserved address are skipped, type reserved sets
//     reserved_by, an invisible interface is 422 interface_id, a pool 409
//     is_pool;
//   - a writer without the lock (an uncommitted interface-less address,
//     written as POST /ips writes it, by the application role in a tenant
//     transaction of its own) on the chosen address leads to a retry with
//     the next free address, not to 409;
//   - an address of another client in the VRF (on the interface of its
//     CI, without subnet) that the client principal does not see is
//     skipped, not handed out again;
//   - allocate-subnet of /26 in the pool 10.1.0.0/24 with the child
//     10.1.0.0/25 gives 10.1.0.128/26, then 10.1.0.192/26, then 409
//     subnet-exhausted; each writes exactly one ipam.subnet.changed; an
//     invalid length is 422 prefix_length.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAllocationOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "cc")
	bg := context.Background()
	repo := ipam.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(orgCtx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	str := func(q string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return out
	}
	subnet := func(body string) string {
		t.Helper()
		w := call(http.MethodPost, "/api/v1/subnets", body)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST /subnets %s: %d %s", body, w.Code, w.Body.String())
		}
		var s ipam.Subnet
		_ = json.Unmarshal(w.Body.Bytes(), &s)
		return s.ID
	}
	allocate := func(subnetID, body string, want int) ipam.IPAddress {
		t.Helper()
		w := call(http.MethodPost, "/api/v1/subnets/"+subnetID+"/allocate", body)
		if w.Code != want {
			t.Fatalf("allocate %s: %d %s, want %d", body, w.Code, w.Body.String(), want)
		}
		var a ipam.IPAddress
		_ = json.Unmarshal(w.Body.Bytes(), &a)
		return a
	}

	// 70 parallel allocations on an empty /26.
	full := subnet(`{"cidr":"10.0.0.0/26"}`)
	var mu sync.Mutex
	var got []string
	codes := map[int]int{}
	exhaustedType := true
	var wg sync.WaitGroup
	for i := 0; i < 70; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := call(http.MethodPost, "/api/v1/subnets/"+full+"/allocate", `{}`)
			mu.Lock()
			defer mu.Unlock()
			codes[w.Code]++
			switch w.Code {
			case http.StatusCreated:
				var a ipam.IPAddress
				_ = json.Unmarshal(w.Body.Bytes(), &a)
				got = append(got, host(a.Address))
			case http.StatusConflict:
				if !strings.Contains(w.Body.String(), ipam.ProblemSubnetExhausted) {
					exhaustedType = false
				}
			}
		}()
	}
	wg.Wait()
	if codes[http.StatusCreated] != 62 || codes[http.StatusConflict] != 8 || !exhaustedType {
		t.Fatalf("70 parallel allocations: %v (subnet-exhausted type %v), want 62×201 and 8×409", codes, exhaustedType)
	}
	seen := map[string]bool{}
	for _, a := range got {
		seen[a] = true
	}
	if len(seen) != 62 {
		t.Errorf("62 allocations gave %d distinct addresses: %v", len(seen), got)
	}
	for i := 1; i <= 62; i++ {
		if a := "10.0.0." + strconv.Itoa(i); !seen[a] {
			t.Errorf("address %s not allocated", a)
		}
	}
	if n := str(`SELECT count(*)::text FROM ip_address WHERE subnet_id = $1`, full); n != "62" {
		t.Errorf("rows of the full subnet = %s, want 62", n)
	}

	// The gateway and a reservation are skipped; reserved sets reserved_by.
	small := subnet(`{"cidr":"10.204.1.0/29","gateway":"10.204.1.1"}`)
	if w := call(http.MethodPost, "/api/v1/subnets/"+small+"/reservations", `{"address":"10.204.1.2"}`); w.Code != http.StatusCreated {
		t.Fatalf("reservation: %d %s", w.Code, w.Body.String())
	}
	a := allocate(small, `{"dns_name":"host3.example.test"}`, http.StatusCreated)
	if host(a.Address) != "10.204.1.3" || a.Type != ipam.IPTypeStatic || a.DNSName != "host3.example.test" || a.ReservedBy != "" || a.SubnetID != small {
		t.Errorf("allocation after gateway and reservation = %+v", a)
	}
	a = allocate(small, `{"type":"reserved","description":"for the printer"}`, http.StatusCreated)
	if host(a.Address) != "10.204.1.4" || a.Type != ipam.IPTypeReserved || a.ReservedBy != f.User {
		t.Errorf("reserved allocation = %+v, want 10.204.1.4 reserved by %s", a, f.User)
	}
	for body, field := range map[string]string{
		`{"type":"dhcp"}`:                   "type",
		`{"interface_id":"` + f.ID() + `"}`: "interface_id",
	} {
		w := call(http.MethodPost, "/api/v1/subnets/"+small+"/allocate", body)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"field":"`+field+`"`) {
			t.Errorf("allocate %s = %d %s, want 422 %s", body, w.Code, w.Body.String(), field)
		}
	}
	if w := call(http.MethodPost, "/api/v1/subnets/"+f.ID()+"/allocate", `{}`); w.Code != http.StatusNotFound {
		t.Errorf("allocate in an unknown subnet = %d, want 404", w.Code)
	}

	// A writer without the lock holds the chosen address: retry, no 409.
	// The writer is POST /ips: the application role under row level
	// security in a tenant transaction of its own, with the statement of
	// PGRepository.CreateIPAddress (vrf_id from the subnet by
	// trg_ip_address_vrf, so the same unique index as the allocation); the
	// transaction stays open until the allocation waits for its row.
	contended := subnet(`{"cidr":"10.204.2.0/29"}`)
	inserted := make(chan error, 1)
	release := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- database.WithRequestTenant(orgCtx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			var id, vrfID string
			err := tx.QueryRow(ctx,
				`INSERT INTO ip_address (organization_id, subnet_id, interface_id, address, type, dns_name, description)
				 VALUES ($1,$2,$3,$4::inet,$5,$6,$7) RETURNING id::text, vrf_id::text`,
				f.OrgA, contended, nil, "10.204.2.1", ipam.IPTypeStatic, nil, nil).Scan(&id, &vrfID)
			inserted <- err
			if err != nil {
				return err
			}
			<-release
			return nil
		})
	}()
	if err := <-inserted; err != nil {
		t.Fatalf("concurrent POST /ips writer: %v", err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call(http.MethodPost, "/api/v1/subnets/"+contended+"/allocate", `{}`) }()
	// The allocation waits for the uncommitted row of the unique index.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := f.Admin.QueryRow(bg, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE 'INSERT INTO ip_address%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	if err := <-writerDone; err != nil {
		t.Fatalf("concurrent POST /ips writer: commit: %v", err)
	}
	w := <-done
	if w.Code != http.StatusCreated {
		t.Fatalf("allocate against a concurrent writer = %d %s, want 201 after a retry", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &a)
	if host(a.Address) != "10.204.2.2" {
		t.Errorf("allocate against a concurrent writer = %s, want 10.204.2.2", a.Address)
	}

	// An address of another client in the VRF outside the subnet: on the
	// interface of a client-2 CI without subnet at the lowest free host. A
	// client-1 principal does not see it, yet the allocation skips it.
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	scoped := subnet(`{"cidr":"10.204.3.0/29","client_id":"` + f.Client1 + `"}`)
	nic := str(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth0') RETURNING id::text`,
		f.OrgA, f.CI(t, f.OrgA, f.Client2, "allocate-other-client"))
	str(`INSERT INTO ip_address (organization_id, interface_id, address) VALUES ($1, $2, '10.204.3.1') RETURNING id::text`, f.OrgA, nic)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subnets/"+scoped+"/allocate", bytes.NewBufferString(`{}`)).WithContext(client1Ctx)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("client-1 allocate beside a client-2 address = %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &a)
	if host(a.Address) != "10.204.3.2" {
		t.Errorf("client-1 allocate beside a client-2 address = %s, want 10.204.3.2", a.Address)
	}

	// allocate-subnet in a pool with a child.
	pool := subnet(`{"cidr":"10.1.0.0/24","is_pool":true}`)
	subnet(`{"cidr":"10.1.0.0/25"}`)
	if w := call(http.MethodPost, "/api/v1/subnets/"+pool+"/allocate", `{}`); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), `"field":"is_pool"`) {
		t.Errorf("allocate in a pool = %d %s, want 409 is_pool", w.Code, w.Body.String())
	}
	subnetEvents := func(id string) string {
		return str(`SELECT count(*)::text FROM event_outbox WHERE organization_id = $1 AND type = $2 AND entity_id = $3`,
			f.OrgA, events.IPAMSubnetChanged, id)
	}
	var cidrs []string
	for i := 0; i < 2; i++ {
		w := call(http.MethodPost, "/api/v1/subnets/"+pool+"/allocate-subnet", `{"prefix_length":26,"name":"alloc"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("allocate-subnet %d: %d %s", i, w.Code, w.Body.String())
		}
		var s ipam.Subnet
		_ = json.Unmarshal(w.Body.Bytes(), &s)
		if s.ParentID != pool || s.Name != "alloc" {
			t.Errorf("allocated subnet = %+v, want a child of the pool", s)
		}
		if n := subnetEvents(s.ID); n != "1" {
			t.Errorf("ipam.subnet.changed of %s = %s, want 1", s.CIDR, n)
		}
		cidrs = append(cidrs, s.CIDR)
	}
	sort.Strings(cidrs)
	if strings.Join(cidrs, ",") != "10.1.0.128/26,10.1.0.192/26" {
		t.Errorf("allocated subnets = %v, want 10.1.0.128/26 and 10.1.0.192/26", cidrs)
	}
	if w := call(http.MethodPost, "/api/v1/subnets/"+pool+"/allocate-subnet", `{"prefix_length":26}`); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), ipam.ProblemSubnetExhausted) {
		t.Errorf("third allocate-subnet = %d %s, want 409 subnet-exhausted", w.Code, w.Body.String())
	}
	for _, body := range []string{`{"prefix_length":24}`, `{"prefix_length":33}`, `{}`} {
		if w := call(http.MethodPost, "/api/v1/subnets/"+pool+"/allocate-subnet", body); w.Code != http.StatusUnprocessableEntity ||
			!strings.Contains(w.Body.String(), `"field":"prefix_length"`) {
			t.Errorf("allocate-subnet %s = %d %s, want 422 prefix_length", body, w.Code, w.Body.String())
		}
	}
}

// host is the address without prefix length, as address::text adds /32.
func host(address string) string {
	return strings.SplitN(address, "/", 2)[0]
}

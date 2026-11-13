package ipam_test

// WP-1071 (IPM-15, E-218): NAT translations against PostgreSQL as
// reticora_app, and the routes without a database.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestNAT|TestIPAddressKeyCreatedOnce' ./internal/ipam/

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// natEnv is the NAT scenario: an org-wide principal and a principal of
// client 1 in organization A; device dev of client 1 with the inside
// address 10.60.1.5 in the subnet 10.60.1.0/24 of client 1 and the
// org-wide outside addresses 203.0.113.5 and 203.0.113.6.
type natEnv struct {
	t                  *testing.T
	f                  *scopetest.Fixture
	org, client1       ifaceAPI
	dev, subnet        string
	inside, out1, out2 string
}

func newNATEnv(t *testing.T) *natEnv {
	t.Helper()
	f := scopetest.Seed(t, "9e")
	scope := database.OrgWideScope(f.OrgA, f.User)
	scope.Clients = database.ScopeIDs(f.Client1)
	e := &natEnv{t: t, f: f, org: newIfaceAPI(f, f.OrgCtx(f.OrgA), f.OrgA),
		client1: newIfaceAPI(f, database.ContextWithTenantScope(context.Background(), &scope), f.OrgA)}
	e.dev = f.CI(t, f.OrgA, f.Client1, "nat-dev")
	e.subnet = e.id(e.org, "/api/v1/subnets", `{"cidr":"10.60.1.0/24","client_id":"`+f.Client1+`"}`)
	e.inside = e.id(e.org, "/api/v1/ips", `{"address":"10.60.1.5","subnet_id":"`+e.subnet+`"}`)
	e.out1 = e.id(e.org, "/api/v1/ips", `{"address":"203.0.113.5"}`)
	e.out2 = e.id(e.org, "/api/v1/ips", `{"address":"203.0.113.6"}`)
	return e
}

func (e *natEnv) call(a ifaceAPI, method, path, body string) (int, map[string]any) {
	e.t.Helper()
	w := a.call(method, path, body)
	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out
}

func (e *natEnv) id(a ifaceAPI, path, body string) string {
	e.t.Helper()
	code, out := e.call(a, http.MethodPost, path, body)
	if code != http.StatusCreated {
		e.t.Fatalf("POST %s %s = %d %v", path, body, code, out)
	}
	return out["id"].(string)
}

// refused checks a 422 on field.
func (e *natEnv) refused(method, path, body, field string) {
	e.t.Helper()
	code, out := e.call(e.org, method, path, body)
	if code != http.StatusUnprocessableEntity || out["field"] != field {
		e.t.Errorf("%s %s %s = %d %v, want 422 %s", method, path, body, code, out, field)
	}
}

// translation is a request body of a static translation of dev from the
// inside address to out1 with extra fields.
func (e *natEnv) translation(extra string) string {
	body := `{"kind":"static","device_ci_id":"` + e.dev + `","inside_address_id":"` + e.inside + `","outside_address_id":"` + e.out1 + `"`
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

// TestNATCheckInsideOutsideExactlyOnce: one reference per side, at the API
// (422 inside/outside) and in the table (nat_translation_inside_check,
// 23514, which the API answers with 422 as well).
func TestNATCheckInsideOutsideExactlyOnce(t *testing.T) {
	e := newNATEnv(t)
	base := `"kind":"static","device_ci_id":"` + e.dev + `"`
	e.refused(http.MethodPost, "/api/v1/nat-translations", `{`+base+`,"outside_address_id":"`+e.out1+`"}`, "inside")
	e.refused(http.MethodPost, "/api/v1/nat-translations", `{`+base+`,"inside_address_id":"`+e.inside+`","inside_subnet_id":"`+
		e.subnet+`","outside_address_id":"`+e.out1+`"}`, "inside")
	e.refused(http.MethodPost, "/api/v1/nat-translations", `{`+base+`,"inside_address_id":"`+e.inside+`"}`, "outside")
	_, err := e.f.Admin.Exec(context.Background(), `INSERT INTO nat_translation (organization_id, kind, device_ci_id, inside_address_id,
		inside_subnet_id, outside_address_id) VALUES ($1, 'static', $2, $3, $4, $5)`, e.f.OrgA, e.dev, e.inside, e.subnet, e.out1)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "nat_translation_inside_check" {
		t.Errorf("both inside references in the table: %v, want 23514 nat_translation_inside_check", err)
	}
	id := e.id(e.org, "/api/v1/nat-translations", e.translation(""))
	// PATCH: an inside subnet replaces the inside address; clearing both is 422.
	code, out := e.call(e.org, http.MethodPatch, "/api/v1/nat-translations/"+id, `{"inside_subnet_id":"`+e.subnet+`"}`)
	if code != http.StatusOK || out["inside_subnet_id"] != e.subnet || out["inside_address_id"] != nil || out["inside"] != "10.60.1.0/24" {
		t.Errorf("PATCH inside subnet: %d %v", code, out)
	}
	e.refused(http.MethodPatch, "/api/v1/nat-translations/"+id, `{"inside_subnet_id":""}`, "inside")
}

// TestNATPatRequiresOutsidePort: pat without outside_port is 422.
func TestNATPatRequiresOutsidePort(t *testing.T) {
	e := newNATEnv(t)
	body := strings.Replace(e.translation(`"protocol":"tcp"`), `"kind":"static"`, `"kind":"pat"`, 1)
	e.refused(http.MethodPost, "/api/v1/nat-translations", body, "outside_port")
	id := e.id(e.org, "/api/v1/nat-translations", strings.Replace(e.translation(`"protocol":"tcp","outside_port":8443,"inside_port":443`),
		`"kind":"static"`, `"kind":"pat"`, 1))
	e.refused(http.MethodPatch, "/api/v1/nat-translations/"+id, `{"outside_port":0}`, "outside_port")
	e.refused(http.MethodPost, "/api/v1/nat-translations", strings.Replace(e.translation(""), `"static"`, `"masquerade"`, 1), "kind")
}

// TestNATPortsOnlyTcpUdp: ports need tcp or udp and lie in 1–65535.
func TestNATPortsOnlyTcpUdp(t *testing.T) {
	e := newNATEnv(t)
	e.refused(http.MethodPost, "/api/v1/nat-translations", e.translation(`"inside_port":80`), "protocol")
	e.refused(http.MethodPost, "/api/v1/nat-translations", e.translation(`"protocol":"icmp","outside_port":80`), "protocol")
	e.refused(http.MethodPost, "/api/v1/nat-translations", e.translation(`"protocol":"tcp","outside_port":70000`), "outside_port")
	e.refused(http.MethodPost, "/api/v1/nat-translations", e.translation(`"protocol":"gre"`), "protocol")
	id := e.id(e.org, "/api/v1/nat-translations", e.translation(`"protocol":"udp","inside_port":53,"outside_port":5353`))
	e.refused(http.MethodPatch, "/api/v1/nat-translations/"+id, `{"protocol":"any"}`, "protocol")
	code, out := e.call(e.org, http.MethodPatch, "/api/v1/nat-translations/"+id, `{"protocol":"any","inside_port":0,"outside_port":0}`)
	if code != http.StatusOK || out["protocol"] != "any" || out["inside_port"] != nil {
		t.Errorf("PATCH to any without ports: %d %v", code, out)
	}
}

// natOf returns the nat_translations of GET /ips/{id} keyed by role.
func (e *natEnv) natOf(a ifaceAPI, ip string) map[string][]map[string]any {
	e.t.Helper()
	code, out := e.call(a, http.MethodGet, "/api/v1/ips/"+ip, "")
	if code != http.StatusOK {
		e.t.Fatalf("GET /ips/%s = %d %v", ip, code, out)
	}
	raw, ok := out["nat_translations"].([]any)
	if !ok {
		e.t.Fatalf("GET /ips/%s: no nat_translations in %v", ip, out)
	}
	byRole := map[string][]map[string]any{}
	for _, r := range raw {
		m := r.(map[string]any)
		byRole[m["role"].(string)] = append(byRole[m["role"].(string)], m)
	}
	return byRole
}

// TestNATShownOnAddress covers AK5: the address lists the translations it
// takes part in directly and through a subnet that contains it, with the
// counterpart; the outside address lists its side.
func TestNATShownOnAddress(t *testing.T) {
	e := newNATEnv(t)
	direct := e.id(e.org, "/api/v1/nat-translations", e.translation(""))
	viaSubnet := e.id(e.org, "/api/v1/nat-translations", `{"kind":"source","device_ci_id":"`+e.dev+`","inside_subnet_id":"`+e.subnet+
		`","outside_address_id":"`+e.out2+`"}`)
	got := e.natOf(e.org, e.inside)
	if len(got["inside"]) != 1 || got["inside"][0]["id"] != direct || got["inside"][0]["counterpart"] != "203.0.113.5" ||
		got["inside"][0]["device_ci_id"] != e.dev || got["inside"][0]["kind"] != "static" {
		t.Errorf("inside address, direct: %v", got["inside"])
	}
	if len(got["inside_subnet"]) != 1 || got["inside_subnet"][0]["id"] != viaSubnet || got["inside_subnet"][0]["counterpart"] != "203.0.113.6" {
		t.Errorf("inside address, through its subnet: %v", got["inside_subnet"])
	}
	out := e.natOf(e.org, e.out1)
	if len(out["outside"]) != 1 || out["outside"][0]["counterpart"] != "10.60.1.5" || len(out) != 1 {
		t.Errorf("outside address: %v", out)
	}
	if n := e.natOf(e.org, e.out2); len(n["outside"]) != 1 || n["outside"][0]["counterpart"] != "10.60.1.0/24" {
		t.Errorf("outside address of the subnet translation: %v", n)
	}
	// Filters of the list.
	for query, want := range map[string]int{"address_id=" + e.inside: 1, "subnet_id=" + e.subnet: 1, "ci_id=" + e.dev: 2,
		"kind=source": 1, "kind=pat": 0} {
		code, list := e.call(e.org, http.MethodGet, "/api/v1/nat-translations?"+query, "")
		if code != http.StatusOK || int(list["total"].(float64)) != want {
			t.Errorf("list %s: %d total %v, want %d", query, code, list["total"], want)
		}
	}
	e.refused(http.MethodGet, "/api/v1/nat-translations?kind=masquerade", "", "kind")
}

// TestNATScopeFromDevice covers AK3: client and site follow the device,
// also when its client changes.
func TestNATScopeFromDevice(t *testing.T) {
	e := newNATEnv(t)
	id := e.id(e.org, "/api/v1/nat-translations", e.translation(""))
	if _, out := e.call(e.org, http.MethodGet, "/api/v1/nat-translations/"+id, ""); out["client_id"] != e.f.Client1 {
		t.Errorf("client of the translation %v, want the device's %s", out["client_id"], e.f.Client1)
	}
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET client_id = $2 WHERE id = $1`, e.dev, e.f.Client2); err != nil {
		t.Fatal(err)
	}
	var client string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT client_id::text FROM nat_translation WHERE id = $1`, id).Scan(&client); err != nil {
		t.Fatal(err)
	}
	if client != e.f.Client2 {
		t.Errorf("client after the device moved: %s, want %s", client, e.f.Client2)
	}
}

// TestNATClientPrincipal covers AK4 and AK5 under a client scope: the
// translation of a device of client 2 is invisible (list, detail, at the
// org-wide address), a device of client 2 or an inside side of another
// client is 422.
func TestNATClientPrincipal(t *testing.T) {
	e := newNATEnv(t)
	foreign := e.f.CI(t, e.f.OrgA, e.f.Client2, "nat-foreign")
	hidden := e.id(e.org, "/api/v1/nat-translations", `{"kind":"static","device_ci_id":"`+foreign+`","inside_address_id":"`+e.out2+
		`","outside_address_id":"`+e.out1+`"}`)
	own := e.id(e.client1, "/api/v1/nat-translations", e.translation(""))
	code, list := e.call(e.client1, http.MethodGet, "/api/v1/nat-translations", "")
	if code != http.StatusOK || int(list["total"].(float64)) != 1 || !strings.Contains(listIDs(list), own) || strings.Contains(listIDs(list), hidden) {
		t.Errorf("client 1 list: %d %v", code, list)
	}
	if code, _ = e.call(e.client1, http.MethodGet, "/api/v1/nat-translations/"+hidden, ""); code != http.StatusNotFound {
		t.Errorf("client 1 GET foreign translation: %d, want 404", code)
	}
	if code, _ = e.call(e.client1, http.MethodDelete, "/api/v1/nat-translations/"+hidden, ""); code != http.StatusNotFound {
		t.Errorf("client 1 DELETE foreign translation: %d, want 404", code)
	}
	for _, n := range e.natOf(e.client1, e.out1)["outside"] {
		if n["id"] == hidden {
			t.Errorf("client 1 sees the foreign translation at %s", e.out1)
		}
	}
	code, out := e.call(e.client1, http.MethodPost, "/api/v1/nat-translations", strings.Replace(e.translation(""), e.dev, foreign, 1))
	if code != http.StatusUnprocessableEntity || out["field"] != "device_ci_id" {
		t.Errorf("client 1 with a device of client 2: %d %v", code, out)
	}
	// The inside side of another client than the device.
	sub2 := e.id(e.org, "/api/v1/subnets", `{"cidr":"10.60.2.0/24","client_id":"`+e.f.Client2+`"}`)
	e.refused(http.MethodPost, "/api/v1/nat-translations", `{"kind":"source","device_ci_id":"`+e.dev+`","inside_subnet_id":"`+sub2+
		`","outside_address_id":"`+e.out1+`"}`, "inside_subnet_id")
	// A translation of an org-wide device whose outside side belongs to
	// client 2: client 1 sees the translation, but neither the address nor
	// its id, and a filter by that id finds nothing.
	orgWide := e.f.CI(t, e.f.OrgA, "", "nat-orgwide")
	addr2 := e.id(e.org, "/api/v1/ips", `{"address":"10.60.2.5","subnet_id":"`+sub2+`"}`)
	shared := e.id(e.org, "/api/v1/nat-translations", `{"kind":"static","device_ci_id":"`+orgWide+`","inside_address_id":"`+e.out1+
		`","outside_address_id":"`+addr2+`"}`)
	code, got := e.call(e.client1, http.MethodGet, "/api/v1/nat-translations/"+shared, "")
	if code != http.StatusOK || got["outside_address_id"] != nil || got["outside"] != nil || got["inside_address_id"] != e.out1 {
		t.Errorf("client 1 GET translation with an outside side of client 2: %d %v, want the outside side empty", code, got)
	}
	code, list = e.call(e.client1, http.MethodGet, "/api/v1/nat-translations?address_id="+addr2, "")
	if code != http.StatusOK || int(list["total"].(float64)) != 0 {
		t.Errorf("client 1 filter by an address of client 2: %d %v, want nothing", code, list)
	}
	code, list = e.call(e.client1, http.MethodGet, "/api/v1/nat-translations?subnet_id="+sub2, "")
	if code != http.StatusOK || int(list["total"].(float64)) != 0 {
		t.Errorf("client 1 filter by a subnet of client 2: %d %v, want nothing", code, list)
	}
	code, list = e.call(e.client1, http.MethodGet, "/api/v1/nat-translations", "")
	if code != http.StatusOK || strings.Contains(fmt.Sprint(list), addr2) {
		t.Errorf("client 1 list carries the id of the address of client 2: %v", list)
	}
}

func listIDs(list map[string]any) string {
	var ids []string
	for _, raw := range list["data"].([]any) {
		ids = append(ids, raw.(map[string]any)["id"].(string))
	}
	return strings.Join(ids, ",")
}

// TestNATDeleteAddressCascades: deleting a referenced address removes its
// translations.
func TestNATDeleteAddressCascades(t *testing.T) {
	e := newNATEnv(t)
	id := e.id(e.org, "/api/v1/nat-translations", e.translation(""))
	if code, _ := e.call(e.org, http.MethodDelete, "/api/v1/ips/"+e.inside, ""); code != http.StatusNoContent {
		t.Fatalf("DELETE inside address: %d", code)
	}
	if code, _ := e.call(e.org, http.MethodGet, "/api/v1/nat-translations/"+id, ""); code != http.StatusNotFound {
		t.Errorf("translation after its address was deleted: %d, want 404", code)
	}
}

// TestIPAddressKeyCreatedOnce covers AK2: with the keys (id,
// organization_id) of ip_address and subnet in place the up migration
// creates no second one, and the down migration keeps a key another
// foreign key still refers to. Run inside a rolled-back transaction.
func TestIPAddressKeyCreatedOnce(t *testing.T) {
	e := newNATEnv(t)
	read := func(suffix string) string {
		t.Helper()
		files, err := filepath.Glob("../../migrations/*_nat_translation." + suffix + ".sql")
		if err != nil || len(files) != 1 {
			t.Fatalf("migration nat_translation %s: %v %v", suffix, files, err)
		}
		raw, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	bg := context.Background()
	tx, err := e.f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	keys := func(table string) int {
		t.Helper()
		var n int
		if err = tx.QueryRow(bg, `SELECT count(*) FROM pg_constraint c WHERE c.conrelid = $1::regclass AND c.contype IN ('u', 'p')
			AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
			      WHERE a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)) = ARRAY['id', 'organization_id']`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if keys("ip_address") != 1 || keys("subnet") != 1 {
		t.Fatalf("before: %d keys of ip_address, %d of subnet, want one each", keys("ip_address"), keys("subnet"))
	}
	// Another table refers to the key of ip_address: down keeps it. The key
	// of subnet is referenced by credential_binding (000292) and stays too.
	if _, err = tx.Exec(bg, `CREATE TABLE wp1071_probe (id uuid, organization_id uuid,
		FOREIGN KEY (id, organization_id) REFERENCES ip_address (id, organization_id))`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, read("down")); err != nil {
		t.Fatalf("down: %v", err)
	}
	if keys("ip_address") != 1 || keys("subnet") != 1 {
		t.Errorf("after down: %d keys of ip_address, %d of subnet, want the referenced keys kept", keys("ip_address"), keys("subnet"))
	}
	// Without a reference down drops the key it created.
	if _, err = tx.Exec(bg, `DROP TABLE wp1071_probe`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, read("up")); err != nil {
		t.Fatalf("up: %v", err)
	}
	if keys("ip_address") != 1 || keys("subnet") != 1 {
		t.Errorf("after up over existing keys: %d keys of ip_address, %d of subnet, want one each", keys("ip_address"), keys("subnet"))
	}
	// Foreign keys of other tables on the key (bgp_peering, WP-1068) are
	// dropped in the rolled-back transaction so that only nat_translation
	// refers to it.
	dropAddressKeyReferences(t, tx, "nat_translation")
	if _, err = tx.Exec(bg, read("down")); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := keys("ip_address"); n != 0 {
		t.Errorf("after down without reference: %d keys of ip_address, want 0", n)
	}
	if _, err = tx.Exec(bg, read("up")); err != nil {
		t.Fatalf("up: %v", err)
	}
	if keys("ip_address") != 1 || keys("subnet") != 1 {
		t.Errorf("after up: %d keys of ip_address, %d of subnet, want one each", keys("ip_address"), keys("subnet"))
	}
}

// TestNATWithoutDatabase covers AK5 without PostgreSQL: the CRUD routes
// answer 501, the address lists no translations.
func TestNATWithoutDatabase(t *testing.T) {
	repo := ipam.NewMemoryRepository()
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "u"})
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx))
		return w
	}
	if w := do(http.MethodGet, "/api/v1/nat-translations", ""); w.Code != http.StatusNotImplemented {
		t.Errorf("GET /nat-translations without database: %d, want 501", w.Code)
	}
	w := do(http.MethodPost, "/api/v1/ips", `{"address":"192.0.2.1"}`)
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("POST /ips: %d %s", w.Code, w.Body.String())
	}
	w = do(http.MethodGet, "/api/v1/ips/"+a.ID, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"nat_translations":[]`) {
		t.Errorf("GET /ips/{id} without database: %d %s, want an empty nat_translations", w.Code, w.Body.String())
	}
}

// dropAddressKeyReferences drops, in the transaction tx, every foreign key
// on ip_address_id_organization_key except those of the table keep, so a
// test of a migration's down step sees only the references of its own
// table.
func dropAddressKeyReferences(t *testing.T, tx pgx.Tx, keep string) {
	t.Helper()
	bg := context.Background()
	rows, err := tx.Query(bg, `SELECT f.conrelid::regclass::text, f.conname FROM pg_constraint f
		JOIN pg_constraint k ON k.conindid = f.conindid AND k.conrelid = 'ip_address'::regclass
		     AND k.conname = 'ip_address_id_organization_key'
		WHERE f.contype = 'f' AND f.conrelid <> $1::regclass`, keep)
	if err != nil {
		t.Fatal(err)
	}
	var refs [][2]string
	for rows.Next() {
		var table, name string
		if err = rows.Scan(&table, &name); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, [2]string{table, name})
	}
	rows.Close()
	for _, ref := range refs {
		if _, err = tx.Exec(bg, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %q", ref[0], ref[1])); err != nil {
			t.Fatal(err)
		}
	}
}

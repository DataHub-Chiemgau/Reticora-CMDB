package main

import (
	"context"
	"crypto/md5" //nolint:gosec // deterministic ids, not a security use
	"encoding/hex"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options sizes the seed (SIM-01, NFR-10).
type Options struct {
	// Prefix is put before the organization slugs and into every id, so
	// several seeds can live in one database (tests); empty seeds "demo"
	// and "acme".
	Prefix string
	// Objects is the number of CIs of the demo organization: 40 is the
	// SIM-01 data set, 10.000 the NFR-10 load seed, at most 50.000
	// (headroom of NFR-01).
	Objects int
	// Metrics is the number of metric samples of the demo organization.
	Metrics int
	// The volumes of NFR-01 (WP-439, F-279) apply to the demo organization;
	// acme and the small organizations keep the SIM-01 data set. Zero is
	// the SIM-01 seed: one interface and one address per CI and the LLDP,
	// power and uplink relationships.
	//
	// InterfacesPerCI is the number of interfaces of every CI: eth0, the
	// management interface of SIM-01, and eth1 and up (5 at 10.000 objects
	// are the 50.000 interfaces of NFR-01).
	InterfacesPerCI int
	// IPsPerCI is the number of addresses of every CI: the management
	// address on eth0 and the others in turn on its interfaces, from
	// ExtraAddressBase (20 at 10.000 objects are the 200.000 addresses).
	IPsPerCI int
	// RelationshipsPerCI fills the relationships of the demo organization
	// up to RelationshipsPerCI · Objects with dependencies between servers
	// (depends_on); the SIM-01 relationships stay (10 at 10.000 objects are
	// the 100.000 relationships).
	RelationshipsPerCI int
	// Orgs is the number of small organizations beside demo and acme, each
	// with the SIM-01 data set of acme (500 per SaaS instance in NFR-01).
	Orgs int
	// At anchors every timestamp, so a seed is reproducible.
	At time.Time
	// APIKey issues a test API key of the demo administrator for the load
	// test (NFR-10); an earlier load-test key is revoked. The key is the
	// only random part of a seed.
	APIKey bool
}

// MaxObjects is the headroom of NFR-01.
const MaxObjects = 50000

// The bounds of the volume options: twice the ratios of NFR-01 per object
// (5 interfaces, 20 addresses, 10 relationships) and the 5.000
// organizations of Phase 5.
const (
	MaxInterfacesPerCI    = 10
	MaxIPsPerCI           = 40
	MaxRelationshipsPerCI = 20
	MaxOrgs               = 5000
)

// ExtraAddressBase is the first address beyond the management addresses:
// address j > 1 of CI i is ExtraAddressBase + (i-1)·(IPsPerCI-1) + j-2, in
// the subnet "Server networks" 100.64.0.0/10 of the default VRF. It holds
// MaxObjects · (MaxIPsPerCI-1) addresses.
const ExtraAddressBase = "100.64.0.0"

// baseObjects are the CIs of SIM-01: they sit in the two racks.
const baseObjects = 40

// AttributeKey is the searchable attribute of the seed: a global attribute
// definition of each organization, a value on every CI. The attribute
// filter of the load test filters on it (NFR-02, F-116).
const AttributeKey = "cost_center"

// CostCenters is the number of distinct values of AttributeKey: CI i holds
// CC-<i mod CostCenters + 1>, three digits, so a value holds 200 of the
// 10.000 CIs of the NFR-10 seed.
const CostCenters = 50

// Org is one seeded organization.
type Org struct {
	ID    string
	Slug  string
	Admin string
	// APIKey is the plaintext of the load-test key (Options.APIKey).
	APIKey string
}

// Summary reports what the seed holds.
type Summary struct {
	Orgs []Org
}

// ID derives the deterministic id of a seeded row; PostgreSQL computes the
// same value as md5(key)::uuid.
func ID(parts ...string) string {
	key := ""
	for i, p := range parts {
		if i > 0 {
			key += "/"
		}
		key += p
	}
	sum := md5.Sum([]byte(key)) //nolint:gosec // deterministic ids
	h := hex.EncodeToString(sum[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// Validate checks the options.
func (o *Options) Validate() error {
	if o.Objects < baseObjects || o.Objects > MaxObjects {
		return fmt.Errorf("objects must be between %d and %d", baseObjects, MaxObjects)
	}
	if o.Metrics < 0 {
		return fmt.Errorf("metrics must not be negative")
	}
	if o.InterfacesPerCI < 0 || o.InterfacesPerCI > MaxInterfacesPerCI {
		return fmt.Errorf("interfaces per CI must be between 1 and %d", MaxInterfacesPerCI)
	}
	if o.IPsPerCI < 0 || o.IPsPerCI > MaxIPsPerCI {
		return fmt.Errorf("addresses per CI must be between 1 and %d", MaxIPsPerCI)
	}
	// Below two per CI the SIM-01 relationships (2 · (objects-4) + 1)
	// already exceed the total.
	if o.RelationshipsPerCI != 0 && (o.RelationshipsPerCI < 2 || o.RelationshipsPerCI > MaxRelationshipsPerCI) {
		return fmt.Errorf("relationships per CI must be 0 (SIM-01) or between 2 and %d", MaxRelationshipsPerCI)
	}
	if o.Orgs < 0 || o.Orgs > MaxOrgs {
		return fmt.Errorf("orgs must be between 0 and %d", MaxOrgs)
	}
	if o.At.IsZero() {
		return fmt.Errorf("an anchor time is required")
	}
	return nil
}

// orgSpec is one organization of the seed.
type orgSpec struct {
	key, name, domain string
	octet             int
	objects, metrics  int
	// The volumes of NFR-01; 0 is the SIM-01 data set.
	interfaces, ips, relationships int
}

// orgSpecs lists the organizations of the seed: the demo organization with
// the requested size, acme and the small organizations org-0001 and up with
// the SIM-01 data set. The management networks of the small organizations
// share 10.12.0.0/16: every organization has VRFs of its own.
func (o *Options) orgSpecs() []orgSpec {
	specs := []orgSpec{
		{"demo", "Demo", "demo.local", 10, o.Objects, o.Metrics, o.InterfacesPerCI, o.IPsPerCI, o.RelationshipsPerCI},
		{"acme", "acme", "acme.local", 11, baseObjects, baseObjects, 0, 0, 0},
	}
	for k := 1; k <= o.Orgs; k++ {
		key := fmt.Sprintf("org-%04d", k)
		specs = append(specs, orgSpec{key, fmt.Sprintf("Org %04d", k), key + ".local", 12, baseObjects, baseObjects, 0, 0, 0})
	}
	return specs
}

// Run seeds the demo organization (plan pro, admin@demo.local), the
// organization "acme" for isolation tests (SIM-01) and Options.Orgs small
// organizations like acme (NFR-01). Every row has a deterministic id and is
// inserted only if missing, so a second run with the same options changes
// nothing. After the seed transaction of an organization its CIs are indexed
// for the search (index) and the index of its searchable attribute is built
// (attributeIndex).
func Run(ctx context.Context, pool *pgxpool.Pool, opts Options) (Summary, error) {
	if err := opts.Validate(); err != nil {
		return Summary{}, err
	}
	var summary Summary
	for n, spec := range opts.orgSpecs() {
		slug := opts.Prefix + spec.key
		o := orgSeed{
			id: ID(opts.Prefix, "org", spec.key), slug: slug, name: spec.name, admin: "admin@" + spec.domain,
			prefix: opts.Prefix + spec.key, octet: spec.octet, objects: spec.objects, metrics: spec.metrics, at: opts.At,
			ifacesPerCI: max(spec.interfaces, 1), ipsPerCI: max(spec.ips, 1), relsPerCI: spec.relationships,
		}
		o.issueKey = opts.APIKey && n == 0
		if err := database.WithOrganizationSetup(ctx, pool, o.id, o.seed); err != nil {
			return Summary{}, fmt.Errorf("seed %s: %w", slug, err)
		}
		if err := o.index(ctx, pool); err != nil {
			return Summary{}, fmt.Errorf("index %s: %w", slug, err)
		}
		if err := o.attributeIndex(ctx, pool); err != nil {
			return Summary{}, fmt.Errorf("attribute index %s: %w", slug, err)
		}
		summary.Orgs = append(summary.Orgs, Org{ID: o.id, Slug: slug, Admin: o.admin, APIKey: o.apiKey})
	}
	return summary, nil
}

type orgSeed struct {
	id, slug, name, admin, prefix string
	// octet is the second octet of the organization's 10.x.0.0/16.
	octet            int
	objects, metrics int
	// interfaces and addresses per CI (at least 1); relationships per CI,
	// 0 for the SIM-01 relationships only (Options).
	ifacesPerCI, ipsPerCI, relsPerCI int
	at                               time.Time
	issueKey                         bool
	apiKey                           string
}

func (o *orgSeed) uid(parts ...string) string {
	return ID(append([]string{o.prefix}, parts...)...)
}

func (o *orgSeed) seed(ctx context.Context, tx pgx.Tx) error {
	steps := []struct {
		name string
		fn   func(context.Context, pgx.Tx) error
	}{
		{"organization", o.organization},
		{"locations", o.locations},
		{"attributes", o.attributes},
		{"cis", o.cis},
		{"network", o.network},
		{"relationships", o.relationships},
		{"metrics", o.metricSamples},
		{"discovery", o.discovery},
		{"webhook", o.webhook},
		{"api key", o.loadTestKey},
	}
	for _, s := range steps {
		if err := s.fn(ctx, tx); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}

// organization creates the organization (its triggers seed the standard
// roles and the default VRF), the entitlements of plan pro (E-34) and the
// administrator with org_admin.
func (o *orgSeed) organization(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `INSERT INTO organization (id, name, slug, plan) VALUES ($1, $2, $3, 'pro')
		ON CONFLICT (id) DO NOTHING`, o.id, o.name, o.slug); err != nil {
		return err
	}
	for _, feature := range entitlement.PlanFeatures(entitlement.PlanPro) {
		if _, err := tx.Exec(ctx, `INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, source)
			VALUES ($1, $2, 'pro', true, '{}', 'manual') ON CONFLICT (organization_id, feature_key) DO NOTHING`,
			o.id, feature); err != nil {
			return err
		}
	}
	userID := o.uid("user", "admin")
	if _, err := tx.Exec(ctx, `INSERT INTO app_user (id, organization_id, email, display_name) VALUES ($1, $2, $3, 'Admin')
		ON CONFLICT (id) DO NOTHING`, userID, o.id, o.admin); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, r.id FROM role r WHERE r.organization_id = $1 AND r.name = 'org_admin'
		AND NOT EXISTS (SELECT 1 FROM role_assignment a WHERE a.user_id = $2 AND a.role_id = r.id)`, o.id, userID)
	return err
}

// locations creates two clients, each with a site, a building and a room;
// the room of the first client holds the two racks. The specialist tables
// create their location row before the insert, so an existing row is
// skipped with NOT EXISTS instead of ON CONFLICT.
func (o *orgSeed) locations(ctx context.Context, tx pgx.Tx) error {
	for c, client := range []string{"hq", "branch"} {
		clientID, siteID := o.uid("client", client), o.uid("site", client)
		buildingID, roomID := o.uid("building", client), o.uid("room", client)
		if _, err := tx.Exec(ctx, `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, $3, $4)
			ON CONFLICT (id) DO NOTHING`, clientID, o.id, map[int]string{0: "Headquarters", 1: "Branch"}[c], client); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO site (id, organization_id, client_id, name)
			SELECT $1, $2, $3, $4 WHERE NOT EXISTS (SELECT 1 FROM site WHERE id = $1)`, siteID, o.id, clientID, "Site "+client); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO building (id, organization_id, site_id, name)
			SELECT $1, $2, $3, $4 WHERE NOT EXISTS (SELECT 1 FROM building WHERE id = $1)`, buildingID, o.id, siteID, "Building "+client); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO room (id, organization_id, building_id, name)
			SELECT $1, $2, $3, $4 WHERE NOT EXISTS (SELECT 1 FROM room WHERE id = $1)`, roomID, o.id, buildingID, "Server room "+client); err != nil {
			return err
		}
	}
	for r := 1; r <= 2; r++ {
		if _, err := tx.Exec(ctx, `INSERT INTO rack (id, organization_id, room_id, name, height_units)
			SELECT $1, $2, $3, $4, 42 WHERE NOT EXISTS (SELECT 1 FROM rack WHERE id = $1)`, o.uid("rack", fmt.Sprint(r)), o.id, o.uid("room", "hq"), fmt.Sprintf("R%02d", r)); err != nil {
			return err
		}
	}
	return nil
}

// attributes creates the searchable attribute definition AttributeKey, a
// text attribute of every CI type of the organization (scope global). Its
// values come with the CIs (cis); the index follows the seed transaction
// (attributeIndex). The insert skips an existing definition by its id or
// key (ON CONFLICT without a target).
func (o *orgSeed) attributes(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `INSERT INTO attribute_definition (id, organization_id, scope, key, display_name, description,
			data_type, searchable)
		VALUES ($1, $2, 'global', $3, '{"de":"Kostenstelle","en":"Cost center"}', 'Seed: filter of the load test (NFR-02)',
			'text', true)
		ON CONFLICT DO NOTHING`, o.uid("attribute", AttributeKey), o.id, AttributeKey)
	return err
}

// attributeIndex builds the index of the searchable attribute as the
// attribute.index job of the server does after the definition is created
// (DB-04, citype.DDLJob): concurrently, outside a transaction, as the owner
// role. An existing valid index is kept, so a second run changes nothing.
func (o *orgSeed) attributeIndex(ctx context.Context, pool *pgxpool.Pool) error {
	return citype.NewDDLJob(pool, jobs.NewService(jobs.NewPGRepository(pool))).Ensure(ctx, o.id, o.uid("attribute", AttributeKey))
}

// cis creates the CIs: two switches, two PDUs and servers. The first 40
// belong to the first client and are mounted in the racks (20 each); the
// rest of a load seed alternates between the clients. Every CI has a
// location: a mounted CI its rack, as a mount through the API sets it
// (RCK-03), every other CI the server room of its client. The database
// derives site_id and room_id from it (ci_derive_location). Every CI holds
// a value of the searchable attribute AttributeKey.
func (o *orgSeed) cis(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO ci (id, organization_id, client_id, location_id, ci_type_id, name, status, manufacturer, model,
			serial_number, management_ip, hostname, discovery_source, first_seen_at, last_seen_at, health, attributes)
		SELECT md5($1 || '/ci/' || i)::uuid, $2,
			CASE WHEN i <= $5 OR i % 2 = 0 THEN md5($1 || '/client/hq')::uuid ELSE md5($1 || '/client/branch')::uuid END,
			CASE WHEN i <= 20 THEN md5($1 || '/rack/1')::uuid WHEN i <= $5 THEN md5($1 || '/rack/2')::uuid
				WHEN i % 2 = 0 THEN md5($1 || '/room/hq')::uuid ELSE md5($1 || '/room/branch')::uuid END,
			t.id,
			CASE WHEN i <= 2 THEN 'sw-' WHEN i <= 4 THEN 'pdu-' ELSE 'srv-' END || lpad(i::text, 5, '0'),
			'active', 'Reticora Labs',
			CASE WHEN i <= 2 THEN 'RX-48' WHEN i <= 4 THEN 'PX-16' ELSE 'R-1U' END,
			'SN' || upper(substr(md5($1 || '/serial/' || i), 1, 12)),
			('10.' || $3::int || '.' || (i / 250) || '.' || (i % 250 + 1))::inet,
			CASE WHEN i <= 2 THEN 'sw-' WHEN i <= 4 THEN 'pdu-' ELSE 'srv-' END || lpad(i::text, 5, '0'),
			'snmp', $4::timestamptz - interval '30 days', $4::timestamptz, 'ok',
			jsonb_build_object($7::text, 'CC-' || lpad((i % $8 + 1)::text, 3, '0'))
		FROM generate_series(1, $6::int) AS i
		JOIN ci_type t ON t.organization_id IS NULL
			AND t.key = CASE WHEN i <= 2 THEN 'switch' WHEN i <= 4 THEN 'pdu' ELSE 'server' END
		ON CONFLICT (id) DO NOTHING`,
		o.prefix, o.id, o.octet, o.at, baseObjects, o.objects, AttributeKey, CostCenters); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO rack_mount (id, organization_id, rack_id, ci_id, position_u, height_u, face)
		SELECT md5($1 || '/mount/' || i)::uuid, $2,
			CASE WHEN i <= 20 THEN md5($1 || '/rack/1')::uuid ELSE md5($1 || '/rack/2')::uuid END,
			md5($1 || '/ci/' || i)::uuid, ((i - 1) % 20) * 2 + 1, 1, 'front'
		FROM generate_series(1, $3::int) AS i
		ON CONFLICT (id) DO NOTHING`, o.prefix, o.id, baseObjects)
	return err
}

// network creates the interfaces and addresses of every CI in the
// organization's default VRF, and the VRF example: a VRF with a subnet that
// overlaps the default one (TEN-10). Interface k of CI i is eth<k-1>; eth0
// holds the management address of the CI, address j > 1 sits on interface
// (j-1) mod interfaces + 1 (NFR-01 volumes). The first interface and
// address keep the ids of the SIM-01 seed (…/if/<i>, …/ip/<i>), the others
// add their number (…/if/<i>/<k>, …/ip/<i>/<j>).
func (o *orgSeed) network(ctx context.Context, tx pgx.Tx) error {
	cidr := fmt.Sprintf("10.%d.0.0/16", o.octet)
	if _, err := tx.Exec(ctx, `INSERT INTO subnet (id, organization_id, client_id, site_id, cidr, name, vrf_id, is_management)
		SELECT $1, $2, $3, $4, $5::cidr, 'Management', v.id, true FROM vrf v WHERE v.organization_id = $2 AND v.is_default
		ON CONFLICT (id) DO NOTHING`, o.uid("subnet", "management"), o.id, o.uid("client", "hq"), o.uid("site", "hq"), cidr); err != nil {
		return err
	}
	if o.ipsPerCI > 1 {
		// The further addresses of every CI, of no client or site.
		if _, err := tx.Exec(ctx, `INSERT INTO subnet (id, organization_id, cidr, name, vrf_id)
			SELECT $1, $2, $3::cidr, 'Server networks', v.id FROM vrf v WHERE v.organization_id = $2 AND v.is_default
			ON CONFLICT (id) DO NOTHING`, o.uid("subnet", "servers"), o.id, ExtraAddressBase+"/10"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO vrf (id, organization_id, name, description) VALUES ($1, $2, 'customer-a',
		'VRF example: the same addresses as the management network') ON CONFLICT (id) DO NOTHING`, o.uid("vrf", "customer-a"), o.id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO subnet (id, organization_id, client_id, site_id, cidr, name, vrf_id)
		VALUES ($1, $2, $3, $4, $5::cidr, 'Customer A', $6) ON CONFLICT (id) DO NOTHING`,
		o.uid("subnet", "customer-a"), o.id, o.uid("client", "branch"), o.uid("site", "branch"), cidr, o.uid("vrf", "customer-a")); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO network_interface (id, organization_id, ci_id, name, mac, if_type, speed_mbps, is_management, oper_status, if_index)
		SELECT md5(n.key)::uuid, $2, md5($1 || '/ci/' || n.i)::uuid, 'eth' || (n.k - 1),
			('02:' || substr(md5(n.mac), 1, 2) || ':' || substr(md5(n.mac), 3, 2) || ':' ||
			 substr(md5(n.mac), 5, 2) || ':' || substr(md5(n.mac), 7, 2) || ':' ||
			 substr(md5(n.mac), 9, 2))::macaddr,
			'ethernet', CASE WHEN n.k = 1 THEN 1000 ELSE 10000 END, n.k = 1, 'up', n.k
		FROM (
			SELECT i, k, $1 || '/if/' || i || CASE WHEN k = 1 THEN '' ELSE '/' || k END AS key,
				$1 || '/mac/' || i || CASE WHEN k = 1 THEN '' ELSE '/' || k END AS mac
			FROM generate_series(1, $3::int) AS i, generate_series(1, $4::int) AS k
		) n
		ON CONFLICT (id) DO NOTHING`, o.prefix, o.id, o.objects, o.ifacesPerCI); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO ip_address (id, organization_id, subnet_id, interface_id, address, type, vrf_id, last_seen_at)
		SELECT md5($1 || '/ip/' || a.i || CASE WHEN a.j = 1 THEN '' ELSE '/' || a.j END)::uuid, $2,
			md5($1 || CASE WHEN a.j = 1 THEN '/subnet/management' ELSE '/subnet/servers' END)::uuid,
			md5($1 || '/if/' || a.i || CASE WHEN a.k = 1 THEN '' ELSE '/' || a.k END)::uuid,
			CASE WHEN a.j = 1 THEN ('10.' || $3::int || '.' || (a.i / 250) || '.' || (a.i % 250 + 1))::inet
				ELSE $8::inet + ((a.i - 1)::bigint * ($7::int - 1) + a.j - 2) END,
			'discovered', v.id, $4::timestamptz
		FROM (
			SELECT i, j, (j - 1) % $6::int + 1 AS k
			FROM generate_series(1, $5::int) AS i, generate_series(1, $7::int) AS j
		) a
		JOIN vrf v ON v.organization_id = $2 AND v.is_default
		ON CONFLICT (id) DO NOTHING`, o.prefix, o.id, o.octet, o.at, o.objects, o.ifacesPerCI, o.ipsPerCI, ExtraAddressBase)
	return err
}

// baseRelationships is the number of SIM-01 relationships of a seed with
// objects CIs (relationships).
func baseRelationships(objects int) int { return 2*(objects-4) + 1 }

// relationships links every server to a switch over LLDP (connected_to) and
// to a PDU (powered_by), and the switches to each other. With
// relationships per CI (NFR-01 volumes) dependencies between servers fill
// the organization up to relationships · objects: dependency d (from 1)
// leads from server s = 5 + (d-1) mod S to the server j places further in
// the ring of the S servers, j = 1 + (d-1) div S. j stays below S
// (MaxRelationshipsPerCI), so no server depends on itself or twice on the
// same server. depends_on runs from the dependent CI to the one it depends
// on (source_depends_on_target): a server's failure impacts the servers
// that depend on it, never a switch or a PDU, so the impact of the core
// switch stays all CIs but itself and the PDUs (tests/load/api.js).
func (o *orgSeed) relationships(ctx context.Context, tx pgx.Tx) error {
	dependencies := 0
	if o.relsPerCI > 0 {
		dependencies = max(o.relsPerCI*o.objects-baseRelationships(o.objects), 0)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO ci_relationship (id, organization_id, source_ci_id, target_ci_id, rel_type, source, source_system,
			confidence, first_seen_at, last_seen_at, last_confirmed_at)
		SELECT md5($1 || '/rel/' || e.kind || '/' || e.i)::uuid, $2, md5($1 || '/ci/' || e.src)::uuid,
			md5($1 || '/ci/' || e.dst)::uuid, e.rel, e.source, e.sys, e.confidence, $3::timestamptz - interval '30 days',
			$3::timestamptz, $3::timestamptz
		FROM (
			SELECT 'lldp' AS kind, i, i AS src, 1 + i % 2 AS dst, 'connected_to' AS rel, 'discovery' AS source,
				'lldp' AS sys, 1.0 AS confidence
			FROM generate_series(5, $4::int) AS i
			UNION ALL
			SELECT 'power', i, i, 3 + i % 2, 'powered_by', 'manual', NULL, NULL
			FROM generate_series(5, $4::int) AS i
			UNION ALL
			SELECT 'uplink', 1, 1, 2, 'connected_to', 'discovery', 'lldp', 1.0
			UNION ALL
			SELECT 'dependency', d, 5 + (d - 1) % ($4::int - 4), 5 + ((d - 1) % ($4::int - 4) + 1 + (d - 1) / ($4::int - 4)) % ($4::int - 4),
				'depends_on', 'manual', NULL, NULL
			FROM generate_series(1, $5::int) AS d
		) e
		ON CONFLICT (id) DO NOTHING`, o.prefix, o.id, o.at, o.objects, dependencies)
	return err
}

// metricSamples writes the reachability of the sweep: one sample per CI
// every five minutes backwards from the anchor.
func (o *orgSeed) metricSamples(ctx context.Context, tx pgx.Tx) error {
	if o.metrics == 0 {
		return nil
	}
	// metric_sample has no id: the samples of a seed are written once,
	// through the tenant view metric_sample_v like the ingest.
	var present bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM metric_sample_v WHERE organization_id = $1)`, o.id).Scan(&present); err != nil || present {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO metric_sample_v (time, organization_id, ci_id, metric_name, value, labels)
		SELECT $3::timestamptz - (m / $4) * interval '5 minutes', $2, md5($1 || '/ci/' || (m % $4 + 1))::uuid,
			'sweep_reachable', CASE WHEN (m % 97) = 0 THEN 0 ELSE 1 END, '{"source":"sweep"}'
		FROM generate_series(0, $5::int - 1) AS m`, o.prefix, o.id, o.at, baseObjects, o.metrics)
	return err
}

// discovery creates a collector with two scopes and three open reviews.
func (o *orgSeed) discovery(ctx context.Context, tx pgx.Tx) error {
	collectorID := o.uid("collector", "1")
	if _, err := tx.Exec(ctx, `INSERT INTO collector (id, organization_id, client_id, site_id, name, version, status, last_heartbeat_at)
		VALUES ($1, $2, $3, $4, 'collector-hq', 'seed', 'active', $5) ON CONFLICT (id) DO NOTHING`,
		collectorID, o.id, o.uid("client", "hq"), o.uid("site", "hq"), o.at); err != nil {
		return err
	}
	for _, s := range []struct{ key, kind, target, schedule string }{
		{"management", "cidr", fmt.Sprintf("10.%d.0.0/24", o.octet), "24h"},
		{"core-switch", "host", fmt.Sprintf("10.%d.0.2", o.octet), "0 * * * *"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO discovery_scope (id, organization_id, client_id, site_id, collector_id, name, kind,
			target, schedule, credential_ids, plugins, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '{}', '{snmp,lldp}', true) ON CONFLICT (id) DO NOTHING`,
			o.uid("scope", s.key), o.id, o.uid("client", "hq"), o.uid("site", "hq"), collectorID,
			"Scope "+s.key, s.kind, s.target, s.schedule); err != nil {
			return err
		}
	}
	for _, r := range []struct {
		key, kind, payload string
		candidates         []string
	}{
		{"duplicate", "probable_duplicate", `{"name":"srv-00005-copy","reason":"same serial"}`, []string{o.uid("ci", "5")}},
		{"unclassified", "unclassified_device", fmt.Sprintf(`{"ip":"10.%d.0.240","sys_object_id":"1.3.6.1.4.1.99999"}`, o.octet), nil},
		{"scope", "scope_suggested", fmt.Sprintf(`{"cidr":"10.%d.1.0/24","collector_id":%q}`, o.octet, collectorID), nil},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO review_item (id, organization_id, kind, status, payload, candidate_ci_ids, object_key, created_at)
			VALUES ($1, $2, $3, 'open', $4::jsonb, $5::uuid[], $6, $7) ON CONFLICT (id) DO NOTHING`,
			o.uid("review", r.key), o.id, r.kind, r.payload, r.candidates, "seed:"+r.key, o.at); err != nil {
			return err
		}
	}
	return nil
}

// webhook subscribes an echo endpoint to the CI events.
func (o *orgSeed) webhook(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `INSERT INTO webhook_subscription (id, organization_id, name, url, secret, events, is_active)
		VALUES ($1, $2, 'Echo', $3, $4, '{ci.created,ci.updated,ci.deleted}', true) ON CONFLICT (id) DO NOTHING`,
		o.uid("webhook", "echo"), o.id, "https://echo."+o.slug+".example/webhook", "seed-"+o.uid("secret"))
	return err
}

// loadTestKey issues the API key of the load test with the permissions of
// org_admin; the key acts as the administrator (AUT-04).
func (o *orgSeed) loadTestKey(ctx context.Context, tx pgx.Tx) error {
	if !o.issueKey {
		return nil
	}
	key, err := identity.GenerateAPIKey(identity.APIKeyEnvironmentTest)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE api_key SET revoked_at = now()
		WHERE organization_id = $1 AND name = 'load-test' AND revoked_at IS NULL`, o.id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO api_key (organization_id, name, key_hash, key_prefix, environment, permissions, created_by)
		SELECT $1, 'load-test', $2, $3, $4, ARRAY(SELECT jsonb_array_elements_text(r.permissions)), $5
		FROM role r WHERE r.organization_id = $1 AND r.name = 'org_admin'`,
		o.id, key.KeyHash, key.KeyPrefix, key.Environment, o.uid("user", "admin")); err != nil {
		return err
	}
	o.apiKey = key.Plaintext
	return nil
}

// indexBatch is the number of search documents index writes with one
// statement.
const indexBatch = 5000

// index writes the search documents of the seeded CIs that have none yet
// through the CI indexer of the server (search.CIIndexer on the PostgreSQL
// index), so the global search finds them like CIs created through the API
// (SRC-01). It runs after the seed transaction, org-wide: the index derives
// the client and site of a document from the committed CI. A CI with a
// document keeps it, so a second run changes nothing.
func (o *orgSeed) index(ctx context.Context, pool *pgxpool.Pool) error {
	scope := database.OrgWideScope(o.id, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	indexer := search.NewCIIndexer(search.NewPGRepository(pool))
	return database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT c.id::text, c.name, COALESCE(c.hostname, ''), COALESCE(c.manufacturer, ''), COALESCE(c.model, ''),
				COALESCE(c.serial_number, ''), COALESCE(c.os_name, ''), COALESCE(c.os_version, ''),
				COALESCE(c.client_id::text, ''), COALESCE(c.site_id::text, '')
			FROM generate_series(1, $3::int) AS i
			JOIN ci c ON c.id = md5($2 || '/ci/' || i)::uuid AND c.organization_id = $1 AND c.deleted_at IS NULL
			WHERE NOT EXISTS (SELECT 1 FROM search_document d
				WHERE d.organization_id = $1 AND d.entity_type = 'ci' AND d.entity_id = c.id)
			ORDER BY i`, o.id, o.prefix, o.objects)
		if err != nil {
			return err
		}
		var docs []ci.Document
		for rows.Next() {
			item := ci.Item{OrganizationID: o.id}
			if err = rows.Scan(&item.ID, &item.Name, &item.Hostname, &item.Manufacturer, &item.Model, &item.SerialNumber,
				&item.OSName, &item.OSVersion, &item.ClientID, &item.SiteID); err != nil {
				rows.Close()
				return err
			}
			docs = append(docs, ci.IndexDocumentFor(&item))
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		// The indexer joins this transaction.
		ctx = database.ContextWithTx(ctx, tx)
		for start := 0; start < len(docs); start += indexBatch {
			if err = indexer.IndexDocuments(ctx, docs[start:min(start+indexBatch, len(docs))]); err != nil {
				return err
			}
		}
		return nil
	})
}

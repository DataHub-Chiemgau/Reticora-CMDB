// Package main provides the seed CLI (SIM-01, NFR-10): the demo organization
// (plan pro, admin@demo.local) and the organization "acme" for isolation
// tests, each with two clients, a site chain with two racks, CIs with LLDP
// and power relationships and a searchable attribute, mounts, metrics, a
// collector with scopes, three reviews, an echo webhook and a VRF example.
// -objects scales the demo organization up to 50.000 CIs for the load test;
// -interfaces-per-ci, -ips-per-ci, -relationships-per-ci and -orgs add the
// volumes of NFR-01 (WP-439).
//
// Phase 2 (WP-325): after the setup transaction of each organization the
// metamodel is seeded through the services of the metamodel WPs. The demo
// organization gets the derived types blade_chassis and blade_server of
// server, two attribute sets, two templates with two published versions
// each (using the computed system attribute warranty_days_left), a
// validation rule and a conditional rule, form layouts per template for
// the roles engineer and viewer, field grants of viewer and a sensitive
// attribute, and the users engineer@, viewer@ and client_technician@ (scoped
// to the client hq) beside the administrator. acme gets the type
// blade_server, a template with one published version and the user viewer;
// the small organizations of -orgs get no Phase-2 content. -api-key-role
// issues the load-test key of -api-key-file for the user of a standard role
// (load-test for org_admin, load-test-<role> otherwise). It is never
// deployed.
//
// Tickets and documents (WP-820): the demo organization gets a business
// calendar in Europe/Berlin (the zone of the client hq too), an SLA per
// priority bound to it, 10 tickets in the states of WP-290 whose due times
// the SLA package computes, and 3 documents with two versions each, linked
// to an asset, a contract and a ticket; acme gets 2 tickets and 1 document.
// -document-content stores the contents in the object store of the server
// (RETICORA_S3_*, bucket reticora-documents); without it only the metadata
// is seeded.
//
// Phase-2 volumes (WP-821): -assets, -consumables and -tickets scale the
// assets (60 of SIM-01 up to 50.000 minus objects; NFR-01 counts CIs and
// assets as objects, E-17), consumables (20 to 50.000) and tickets (10 to
// 50.000) of the demo organization. The objects beyond SIM-01 are written
// set-based: assets in the bins without a CI, consumables with one receipt
// each, tickets with the due times of the seed calendar; they leave the
// graph of the core switch as it is. A flag that is not set keeps the
// SIM-01 quantity and does not count against the 50.000 objects. Every [B]
// CI of the demo organization also gets its hostname, serial number and
// management address as discovered values of the source snmp, every 20th a
// protected override on hostname with the value of its column (field
// values with provenance). The Phase-2 load seed of ABN-02 (WP-327, WP-822)
// is e.g. -objects 5000 -assets 5000 -consumables 2000 -tickets 5000
// -api-key-role engineer (tests/load/README.md).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

func main() {
	dsn := flag.String("database-url", os.Getenv("RETICORA_DATABASE_URL"),
		"PostgreSQL connection string (defaults to RETICORA_DATABASE_URL)")
	objects := flag.Int("objects", baseObjects,
		fmt.Sprintf("CIs of the demo organization (%d to %d minus assets, the -assets value when set; 10000 for NFR-10)", baseObjects, MaxObjects))
	assets := flag.Int("assets", BaseAssets,
		fmt.Sprintf("assets of the demo organization (%d to %d minus objects; when not set the SIM-01 %d, outside the bound)", BaseAssets, MaxObjects, BaseAssets))
	consumables := flag.Int("consumables", BaseConsumables, fmt.Sprintf("consumables of the demo organization (%d-%d)", BaseConsumables, MaxConsumables))
	tickets := flag.Int("tickets", BaseTickets, fmt.Sprintf("tickets of the demo organization (%d-%d)", BaseTickets, MaxTickets))
	metrics := flag.Int("metrics", 2000, "metric samples of the demo organization")
	interfaces := flag.Int("interfaces-per-ci", 1, fmt.Sprintf("interfaces of every CI of the demo organization (1-%d; NFR-01: 5)", MaxInterfacesPerCI))
	ips := flag.Int("ips-per-ci", 1, fmt.Sprintf("addresses of every CI of the demo organization (1-%d; NFR-01: 20)", MaxIPsPerCI))
	relationships := flag.Int("relationships-per-ci", 0,
		fmt.Sprintf("relationships per CI of the demo organization, filled up with dependencies (0: SIM-01, about 2; 2-%d; NFR-01: 10)", MaxRelationshipsPerCI))
	orgs := flag.Int("orgs", 0, fmt.Sprintf("small organizations with the SIM-01 data set besides demo and acme (0-%d; NFR-01: 500)", MaxOrgs))
	at := flag.String("at", "", "anchor time of every timestamp (RFC 3339; default: the current hour)")
	prefix := flag.String("prefix", "", "prefix of the organization slugs and ids")
	keyFile := flag.String("api-key-file", "", "issue a load-test API key of the demo organization and write it to this file")
	keyRole := flag.String("api-key-role", DefaultAPIKeyRole,
		"standard role whose demo user owns the load-test key (org_admin: load-test, otherwise load-test-<role>)")
	documentContent := flag.Bool("document-content", false,
		"store the content of the seeded documents in the object store of RETICORA_S3_* (bucket "+document.Bucket+")")
	flag.Parse()
	// A Phase-2 volume that is not set is the SIM-01 quantity (0): an
	// unset -assets does not count against MaxObjects, so -objects 50000
	// keeps working (WP-821).
	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	for name, value := range map[string]*int{"assets": assets, "consumables": consumables, "tickets": tickets} {
		if !explicit[name] {
			*value = 0
		}
	}
	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "seed: database URL is required")
		os.Exit(2)
	}
	anchor := time.Now().UTC().Truncate(time.Hour)
	if *at != "" {
		parsed, err := time.Parse(time.RFC3339, *at)
		if err != nil {
			fmt.Fprintf(os.Stderr, "seed: -at: %v\n", err)
			os.Exit(2)
		}
		anchor = parsed
	}

	ctx := context.Background()
	var blobs blob.Store
	if *documentContent {
		store, storeErr := documentStore(ctx)
		if storeErr != nil {
			fmt.Fprintf(os.Stderr, "seed: object store: %v\n", storeErr)
			os.Exit(2)
		}
		blobs = store
	}
	pool, err := database.NewAdminPool(ctx, *dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed: connect: %v\n", err)
		os.Exit(2)
	}
	defer pool.Close()

	start := time.Now()
	summary, err := Run(ctx, pool, Options{Prefix: *prefix, Objects: *objects, Metrics: *metrics,
		InterfacesPerCI: *interfaces, IPsPerCI: *ips, RelationshipsPerCI: *relationships, Orgs: *orgs, At: anchor,
		Assets: *assets, Consumables: *consumables, Tickets: *tickets,
		APIKey: *keyFile != "", APIKeyRole: *keyRole, Blobs: blobs})
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed: %v\n", err)
		os.Exit(1)
	}
	if *keyFile != "" {
		if err = os.WriteFile(*keyFile, []byte(summary.Orgs[0].APIKey+"\n"), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "seed: write API key: %v\n", err)
			os.Exit(1)
		}
	}
	for _, o := range summary.Orgs {
		fmt.Printf("%s\t%s\t%s\n", o.Slug, o.ID, o.Admin)
	}
	if blobs == nil {
		fmt.Fprintf(os.Stderr, "seed: %d document versions as metadata only, no content stored (without -document-content)\n",
			summary.DocumentVersions)
	} else {
		fmt.Fprintf(os.Stderr, "seed: %d of %d document contents in bucket %s\n", summary.DocumentContents,
			summary.DocumentVersions, document.Bucket)
	}
	fmt.Fprintf(os.Stderr, "seed: done in %s\n", time.Since(start).Round(time.Millisecond))
}

// documentStore creates the object store like the server (config.Load,
// blob.NewS3Store with RETICORA_S3_*) and ensures the bucket of the
// documents.
func documentStore(ctx context.Context) (blob.Store, error) {
	cfg := config.Load()
	store, err := blob.NewS3Store(cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3UseSSL)
	if err != nil {
		return nil, err
	}
	if err = store.EnsureBucket(ctx, document.Bucket); err != nil {
		return nil, err
	}
	return store, nil
}

// Package main provides the seed CLI (SIM-01, NFR-10): the demo organization
// (plan pro, admin@demo.local) and the organization "acme" for isolation
// tests, each with two clients, a site chain with two racks, CIs with LLDP
// and power relationships and a searchable attribute, mounts, metrics, a
// collector with scopes, three reviews, an echo webhook and a VRF example.
// -objects scales the demo organization up to 50.000 CIs for the load test;
// -interfaces-per-ci, -ips-per-ci, -relationships-per-ci and -orgs add the
// volumes of NFR-01 (WP-439). It is never deployed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

func main() {
	dsn := flag.String("database-url", os.Getenv("RETICORA_DATABASE_URL"),
		"PostgreSQL connection string (defaults to RETICORA_DATABASE_URL)")
	objects := flag.Int("objects", baseObjects, fmt.Sprintf("CIs of the demo organization (%d-%d; 10000 for NFR-10)", baseObjects, MaxObjects))
	metrics := flag.Int("metrics", 2000, "metric samples of the demo organization")
	interfaces := flag.Int("interfaces-per-ci", 1, fmt.Sprintf("interfaces of every CI of the demo organization (1-%d; NFR-01: 5)", MaxInterfacesPerCI))
	ips := flag.Int("ips-per-ci", 1, fmt.Sprintf("addresses of every CI of the demo organization (1-%d; NFR-01: 20)", MaxIPsPerCI))
	relationships := flag.Int("relationships-per-ci", 0,
		fmt.Sprintf("relationships per CI of the demo organization, filled up with dependencies (0: SIM-01, about 2; 2-%d; NFR-01: 10)", MaxRelationshipsPerCI))
	orgs := flag.Int("orgs", 0, fmt.Sprintf("small organizations with the SIM-01 data set besides demo and acme (0-%d; NFR-01: 500)", MaxOrgs))
	at := flag.String("at", "", "anchor time of every timestamp (RFC 3339; default: the current hour)")
	prefix := flag.String("prefix", "", "prefix of the organization slugs and ids")
	keyFile := flag.String("api-key-file", "", "issue a load-test API key of the demo administrator and write it to this file")
	flag.Parse()
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
	pool, err := database.NewAdminPool(ctx, *dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed: connect: %v\n", err)
		os.Exit(2)
	}
	defer pool.Close()

	start := time.Now()
	summary, err := Run(ctx, pool, Options{Prefix: *prefix, Objects: *objects, Metrics: *metrics,
		InterfacesPerCI: *interfaces, IPsPerCI: *ips, RelationshipsPerCI: *relationships, Orgs: *orgs, At: anchor,
		APIKey: *keyFile != ""})
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
	fmt.Fprintf(os.Stderr, "seed: done in %s\n", time.Since(start).Round(time.Millisecond))
}

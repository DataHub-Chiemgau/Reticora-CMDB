package vulnfeed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CISA KEV (SEC-02, WP-1119 AK5): the catalog of known exploited
// vulnerabilities, fetched at most once per 24 h after the NVD import;
// kev is true for every advisory whose cve_ids hold a KEV CVE and false
// for the others. The state is vuln_source('kev').

// DefaultKEVURL is the default of RETICORA_VULNFEED_KEV_URL.
const DefaultKEVURL = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"

// KEVInterval is how often the KEV catalog is fetched.
const KEVInterval = 24 * time.Hour

const kevMaxBytes = 32 << 20

// KEV is the KEV sync.
type KEV struct {
	store  NVDStore
	url    string
	client *http.Client
	now    func() time.Time
}

// RunIfDue fetches the KEV catalog when the last sync is 24 h old or
// older (or there was none) and sets the flags.
func (k *KEV) RunIfDue(ctx context.Context) error {
	src, err := loadSource(ctx, k.store, KindKEV, KindKEV, k.url)
	if err != nil {
		return err
	}
	if src.SyncedAt != nil && k.now().Sub(*src.SyncedAt) < KEVInterval {
		return nil
	}
	// Fetched without If-None-Match: the flags are set again on every due
	// sync, also for advisories imported after the last change of the
	// catalog (ReplaceAdvisory sets kev on a new advisory only when another
	// one of its CVEs is flagged already). Once per 24 h this is cheap.
	f, err := fetch(ctx, k.client, k.url, "", kevMaxBytes, nil)
	if err == nil {
		err = k.apply(ctx, src, f.body)
	}
	if err != nil {
		countSyncError(KindKEV)
		src.ErrorsTotal++
		src.LastError = truncate(err.Error(), 500)
		slog.Warn("kev sync failed", "error", err)
		if uerr := k.store.UpdateSource(ctx, src); uerr != nil {
			return errors.Join(err, uerr)
		}
		return err
	}
	src.ETag = f.etag
	now := k.now().UTC()
	src.SyncedAt, src.LastError = &now, ""
	if err := k.store.UpdateSource(ctx, src); err != nil {
		return err
	}
	syncAge.synced(KindKEV, src.ID, now)
	return nil
}

// apply sets the flags from a catalog.
func (k *KEV) apply(ctx context.Context, src *Source, body []byte) error {
	var catalog struct {
		CatalogVersion  string `json:"catalogVersion"`
		Vulnerabilities []struct {
			CVEID string `json:"cveID"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		countDocument(KindKEV, ResultParseError)
		return fmt.Errorf("vulnfeed: kev catalog: %w", err)
	}
	cves := make([]string, 0, len(catalog.Vulnerabilities))
	for _, v := range catalog.Vulnerabilities {
		if id := strings.ToUpper(strings.TrimSpace(v.CVEID)); strings.HasPrefix(id, "CVE-") {
			cves = append(cves, id)
		}
	}
	if len(cves) == 0 {
		// An empty catalog would clear every flag: refused as broken.
		countDocument(KindKEV, ResultParseError)
		return errors.New("vulnfeed: kev catalog without CVEs")
	}
	changed, err := k.store.SetKEV(ctx, cves)
	if err != nil {
		return err
	}
	countDocument(KindKEV, ResultImported)
	src.DocumentsTotal++
	src.Cursor = catalog.CatalogVersion
	slog.Info("kev catalog applied", "cves", len(cves), "advisories_changed", changed)
	return nil
}

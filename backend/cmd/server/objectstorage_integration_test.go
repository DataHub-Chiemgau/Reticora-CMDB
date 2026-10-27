package main

// WP-544 (OPS-04, NFR-05; open evidence (4) of WP-100): setupObjectStorage
// against a real MinIO. Until now the bucket versioning was only shown with an
// httptest server (internal/platform/blob/versioning_test.go), which records
// the request but proves nothing about what an object store makes of it.
//
// The test runs setupObjectStorage with the S3 settings of TEST_S3_ENDPOINT
// and a bucket name of its own (RETICORA_S3_BUCKET), so that bucket is
// created and versioned by this run; the fixed buckets (exports,
// reticora-documents) are checked the same way. For every bucket it reads
// the versioning back (GetBucketVersioning = Enabled), writes one key twice
// (two versions, the first one still readable by its version id) and deletes
// it (the newest entry is a delete marker, the two versions stay). The
// checks use their own SigV4 signer, not the client under test.
//
// The DB job of .github/workflows/ci.yml starts MinIO and sets
// TEST_S3_ENDPOINT; without it the test skips only outside the CI.
//
// Retest:
//   docker run -d --name minio544 -p 127.0.0.1:59544:9000 \
//     -e MINIO_ROOT_USER=reticora -e MINIO_ROOT_PASSWORD=reticora_dev \
//     pgsty/minio:RELEASE.2026-08-04T00-00-00Z server /data
//   cd backend && TEST_S3_ENDPOINT=http://127.0.0.1:59544 \
//     go test -count=1 -v -run TestSetupObjectStorageEnablesVersioning ./cmd/server/

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
)

// s3Probe signs plain S3 requests (SigV4, path style, region us-east-1) to
// read back what setupObjectStorage left in the object store.
type s3Probe struct {
	base           *url.URL
	access, secret string
	client         *http.Client
}

func (p *s3Probe) do(t *testing.T, method, path string, query url.Values) (int, []byte) {
	t.Helper()
	u := *p.base
	u.Path = path
	u.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
	req, err := http.NewRequestWithContext(context.Background(), method, u.String(), nil)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	now := time.Now().UTC()
	amzDate, day := now.Format("20060102T150405Z"), now.Format("20060102")
	emptyHash := sha256Hex(nil)
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, url.QueryEscape(k)+"="+strings.ReplaceAll(url.QueryEscape(query.Get(k)), "+", "%20"))
	}
	canonical := strings.Join([]string{method, u.EscapedPath(), strings.Join(pairs, "&"),
		"host:" + u.Host + "\nx-amz-content-sha256:" + emptyHash + "\nx-amz-date:" + amzDate + "\n",
		"host;x-amz-content-sha256;x-amz-date", emptyHash}, "\n")
	scope := day + "/us-east-1/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	key := []byte("AWS4" + p.secret)
	for _, part := range []string{day, "us-east-1", "s3", "aws4_request"} {
		key = hmacSum(key, part)
	}
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", emptyHash)
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=%s",
		p.access, scope, hex.EncodeToString(hmacSum(key, toSign))))
	resp, err := p.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return resp.StatusCode, body
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSum(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(data))
	return mac.Sum(nil)
}

// objectVersion is a Version or DeleteMarker entry of ListObjectVersions.
type objectVersion struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId"`
	IsLatest  bool   `xml:"IsLatest"`
}

func TestSetupObjectStorageEnablesVersioning(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("TEST_S3_ENDPOINT"))
	if endpoint == "" {
		// Only the DB job of the CI starts MinIO; it is the one with a
		// database (TEST_DATABASE_URL). The unit job runs without both.
		if ci, _ := strconv.ParseBool(os.Getenv("CI")); ci && os.Getenv("TEST_DATABASE_URL") != "" {
			t.Fatal("TEST_S3_ENDPOINT not set in the CI DB job: it starts MinIO for the bucket versioning of setupObjectStorage (WP-544)")
		}
		t.Skip("TEST_S3_ENDPOINT not set; skipping the object storage integration test")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	base, err := url.Parse(endpoint)
	if err != nil || base.Host == "" {
		t.Fatalf("TEST_S3_ENDPOINT %q is not a URL: %v", endpoint, err)
	}
	access, secret := os.Getenv("TEST_S3_ACCESS_KEY"), os.Getenv("TEST_S3_SECRET_KEY")
	if access == "" {
		access = "reticora"
	}
	if secret == "" {
		secret = "reticora_dev"
	}
	probe := &s3Probe{base: &url.URL{Scheme: base.Scheme, Host: base.Host}, access: access, secret: secret,
		client: &http.Client{Timeout: 30 * time.Second}}

	var nonce [6]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	ownBucket := "wp544-" + hex.EncodeToString(nonce[:])
	// The bucket of this run does not exist before setupObjectStorage.
	if code, body := probe.do(t, http.MethodGet, "/"+ownBucket, url.Values{"versioning": {""}}); code != http.StatusNotFound {
		t.Fatalf("bucket %s exists before setupObjectStorage: HTTP %d %s", ownBucket, code, body)
	}

	cfg := &config.Config{S3Endpoint: endpoint, S3AccessKey: access, S3SecretKey: secret, S3Bucket: ownBucket}
	store, err := setupObjectStorage(cfg)
	if err != nil {
		t.Fatalf("setupObjectStorage: %v", err)
	}
	ctx := context.Background()

	for name, bucket := range map[string]string{"RETICORA_S3_BUCKET": ownBucket, "exports": export.ExportBucket, "reticora-documents": "reticora-documents"} {
		t.Run(name, func(t *testing.T) {
			code, body := probe.do(t, http.MethodGet, "/"+bucket, url.Values{"versioning": {""}})
			if code != http.StatusOK {
				t.Fatalf("GetBucketVersioning %s: HTTP %d %s", bucket, code, body)
			}
			var vc struct {
				Status string `xml:"Status"`
			}
			if err := xml.Unmarshal(body, &vc); err != nil {
				t.Fatalf("GetBucketVersioning %s: %v: %s", bucket, err, body)
			}
			if vc.Status != "Enabled" {
				t.Fatalf("GetBucketVersioning %s = %q, want Enabled: %s", bucket, vc.Status, body)
			}

			key := "wp544/" + hex.EncodeToString(nonce[:]) + "/document.txt"
			first, second := []byte("first version"), []byte("second version")
			for _, payload := range [][]byte{first, second} {
				if err := store.Put(ctx, bucket, key, bytes.NewReader(payload), int64(len(payload)), "text/plain"); err != nil {
					t.Fatalf("put %s/%s: %v", bucket, key, err)
				}
			}
			versions, markers := listVersions(t, probe, bucket, key)
			if len(versions) != 2 || len(markers) != 0 {
				t.Fatalf("after two writes of %s/%s: %d versions, %d delete markers, want 2 and 0", bucket, key, len(versions), len(markers))
			}
			// The overwritten content stays readable by its version id.
			older := versions[0]
			if older.IsLatest {
				older = versions[1]
			}
			if code, body := probe.do(t, http.MethodGet, "/"+bucket+"/"+key, url.Values{"versionId": {older.VersionID}}); code != http.StatusOK || !bytes.Equal(body, first) {
				t.Fatalf("GET %s/%s?versionId=%s = HTTP %d %q, want 200 %q", bucket, key, older.VersionID, code, body, first)
			}

			if err := store.Delete(ctx, bucket, key); err != nil {
				t.Fatalf("delete %s/%s: %v", bucket, key, err)
			}
			versions, markers = listVersions(t, probe, bucket, key)
			if len(versions) != 2 || len(markers) != 1 || !markers[0].IsLatest {
				t.Fatalf("after deleting %s/%s: %d versions, delete markers %+v, want 2 versions and one latest delete marker", bucket, key, len(versions), markers)
			}
			if rc, err := store.Get(ctx, bucket, key); err == nil {
				_ = rc.Close()
				t.Fatalf("GET %s/%s after the delete succeeded, want not found", bucket, key)
			}
		})
	}
}

// listVersions returns the versions and delete markers of key in bucket.
func listVersions(t *testing.T, probe *s3Probe, bucket, key string) (versions, markers []objectVersion) {
	t.Helper()
	code, body := probe.do(t, http.MethodGet, "/"+bucket, url.Values{"versions": {""}, "prefix": {key}})
	if code != http.StatusOK {
		t.Fatalf("ListObjectVersions %s: HTTP %d %s", bucket, code, body)
	}
	var result struct {
		Versions      []objectVersion `xml:"Version"`
		DeleteMarkers []objectVersion `xml:"DeleteMarker"`
	}
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("ListObjectVersions %s: %v: %s", bucket, err, body)
	}
	for _, v := range result.Versions {
		if v.Key == key {
			versions = append(versions, v)
		}
	}
	for _, m := range result.DeleteMarkers {
		if m.Key == key {
			markers = append(markers, m)
		}
	}
	return versions, markers
}

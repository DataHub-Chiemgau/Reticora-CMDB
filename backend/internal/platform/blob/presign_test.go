package blob

// Security tests of the presigned GET URL, phase 2 (SEC-11, WP-829). TTL
// (X-Amz-Expires=900) and host are covered by
// TestPresignedGetURLNamesThePublicOrigin (public_url_test.go, WP-647) and
// are not repeated. Documents (GET /api/v1/documents/{id}/content), import
// reports (GET /imports/{id}/report) and export jobs (GET
// /api/v1/exports/{id}) all sign over Store.PresignedGetURL, so the cases
// below hold for each of them.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

// presignObjects are objects of the three download paths: a document
// version (bucket reticora-documents, document.Bucket), an import report
// (importer.ReportKey in export.ExportBucket) and an export file
// (export.ObjectKey).
var presignObjects = []struct{ bucket, key string }{
	{"reticora-documents", "org/org-1/documents/doc-1/ver-1"},
	{"exports", "org/org-1/imports/job-1/report.csv"},
	{"exports", "org-1/export-1.csv"},
	{"reticora-documents", "org/org-2/documents/doc-1/ver-1"},
}

const (
	presignAccess = "access"
	presignSecret = "secret"
	presignHost   = "minio:9000"
)

func presignStore(t *testing.T) *S3Store {
	t.Helper()
	s, err := NewS3Store(presignHost, presignAccess, presignSecret, false)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// sigV4Valid verifies a presigned GET URL like the object store does, by
// AWS Signature Version 4 with query parameters, written out independently
// of S3Store: the canonical request covers the method, the path, every
// query parameter but X-Amz-Signature and the signed header host. It
// reports whether the signature matches the request u describes.
func sigV4Valid(t *testing.T, u *url.URL, secret, host string) bool {
	t.Helper()
	q := u.Query()
	signature := q.Get("X-Amz-Signature")
	q.Del("X-Amz-Signature")
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || q.Get("X-Amz-SignedHeaders") != "host" {
		return false
	}
	credential := strings.Split(q.Get("X-Amz-Credential"), "/")
	if len(credential) != 5 {
		return false
	}
	date, region, service := credential[1], credential[2], credential[3]

	names := make([]string, 0, len(q))
	for name := range q {
		names = append(names, name)
	}
	sort.Strings(names)
	var pairs []string
	for _, name := range names {
		for _, v := range q[name] {
			pairs = append(pairs, strings.ReplaceAll(url.QueryEscape(name)+"="+url.QueryEscape(v), "+", "%20"))
		}
	}
	canonical := strings.Join([]string{"GET", u.EscapedPath(), strings.Join(pairs, "&"),
		"host:" + host + "\n", "host", "UNSIGNED-PAYLOAD"}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", q.Get("X-Amz-Date"),
		strings.Join(credential[1:], "/"), hex.EncodeToString(sum[:])}, "\n")

	mac := func(key []byte, data string) []byte {
		h := hmac.New(sha256.New, key)
		_, _ = h.Write([]byte(data))
		return h.Sum(nil)
	}
	key := mac(mac(mac(mac([]byte("AWS4"+secret), date), region), service), "aws4_request")
	return hmac.Equal([]byte(hex.EncodeToString(mac(key, toSign))), []byte(signature))
}

func presign(t *testing.T, s *S3Store, bucket, key string) *url.URL {
	t.Helper()
	raw, err := s.PresignedGetURL(context.Background(), bucket, key)
	if err != nil {
		t.Fatalf("presign %s/%s: %v", bucket, key, err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestPresignedGetURLSignsOnlyTheRequestedObject: the signature covers the
// bucket and the key of the request and nothing else. The same query on
// the path of another object (another bucket, another key, another
// organization, a traversal from the signed key) or with a longer expiry
// fails the verification the object store applies.
func TestPresignedGetURLSignsOnlyTheRequestedObject(t *testing.T) {
	s := presignStore(t)
	for i, obj := range presignObjects {
		u := presign(t, s, obj.bucket, obj.key)
		if u.Path != "/"+obj.bucket+"/"+obj.key {
			t.Fatalf("path of %s/%s: %s", obj.bucket, obj.key, u.Path)
		}
		if !sigV4Valid(t, u, presignSecret, presignHost) {
			t.Fatalf("the URL of %s/%s does not verify: %s", obj.bucket, obj.key, u)
		}
		if sigV4Valid(t, u, "other-secret", presignHost) || sigV4Valid(t, u, presignSecret, "evil.example.org") {
			t.Fatalf("the URL of %s/%s verifies with another secret or host", obj.bucket, obj.key)
		}

		paths := []string{
			"/" + obj.bucket + "/" + obj.key + "x",
			"/" + obj.bucket + "/" + obj.key + "/../../org-2/x",
			"/" + obj.bucket + "/" + strings.Replace(obj.key, "org-1", "org-2", 1),
			"/" + obj.bucket + "/" + strings.ToUpper(obj.key),
			"/" + obj.bucket + "/" + obj.key[:strings.LastIndex(obj.key, "/")+1],
			"/other-bucket/" + obj.key,
		}
		for j, other := range presignObjects {
			if j != i {
				paths = append(paths, "/"+other.bucket+"/"+other.key)
			}
		}
		for _, path := range paths {
			if path == u.Path {
				continue
			}
			moved := *u
			moved.Path, moved.RawPath = path, ""
			if sigV4Valid(t, &moved, presignSecret, presignHost) {
				t.Errorf("the signature of %s/%s is valid for %s", obj.bucket, obj.key, path)
			}
		}

		for param, value := range map[string]string{
			"X-Amz-Expires": "604800",
			"X-Amz-Date":    time.Now().UTC().Add(time.Hour).Format("20060102T150405Z"),
			// A parameter added by the holder of the URL, e.g. to override
			// the delivered type, is outside the signature.
			"response-content-type": "text/html",
		} {
			tampered := *u
			q := tampered.Query()
			q.Set(param, value)
			tampered.RawQuery = q.Encode()
			if sigV4Valid(t, &tampered, presignSecret, presignHost) {
				t.Errorf("the signature of %s/%s is valid with %s=%s", obj.bucket, obj.key, param, value)
			}
		}
	}
}

// TestPresignedGetURLForcesDownload: a presigned URL delivers the object
// only as a download with its verified type, so a polyglot (a valid PDF
// with embedded HTML) is never rendered on the origin of the frontend
// proxy: response-content-disposition=attachment and response-content-type
// are part of the signed query.
func TestPresignedGetURLForcesDownload(t *testing.T) {
	t.Skip("Befund für WP-329 (SEC-09, SEC-11): S3Store.PresignedGetURL (blob.go:353–391) setzt nur X-Amz-*-Parameter, " +
		"weder response-content-disposition=attachment noch response-content-type, und frontend/objects-proxy.conf " +
		"setzt weder Content-Disposition noch X-Content-Type-Options: nosniff; das Folge-WP entfernt diesen Skip")
	s := presignStore(t)
	for _, obj := range presignObjects {
		u := presign(t, s, obj.bucket, obj.key)
		q := u.Query()
		if d := q.Get("response-content-disposition"); !strings.HasPrefix(d, "attachment") {
			t.Errorf("%s/%s: response-content-disposition %q, want attachment", obj.bucket, obj.key, d)
		}
		if q.Get("response-content-type") == "" {
			t.Errorf("%s/%s: no response-content-type", obj.bucket, obj.key)
		}
		if !sigV4Valid(t, u, presignSecret, presignHost) {
			t.Errorf("%s/%s: the response parameters are not signed", obj.bucket, obj.key)
		}
	}
}

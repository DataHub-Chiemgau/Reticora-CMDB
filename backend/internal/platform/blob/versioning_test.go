package blob

import (
	"context"
	"crypto/md5" //nolint:gosec // verifies the Content-MD5 header
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEnableVersioningSendsASignedPutBucketVersioning covers WP-100
// (NFR-05, OPS-04): the request is PUT /<bucket>?versioning with the
// "Enabled" configuration, Content-MD5 and a SigV4 signature that covers
// the query; an error status is returned to the caller.
func TestEnableVersioningSendsASignedPutBucketVersioning(t *testing.T) {
	status := http.StatusOK
	var gotMethod, gotPath, gotQuery, gotBody, gotMD5, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotMethod, gotPath, gotQuery, gotBody = r.Method, r.URL.Path, r.URL.RawQuery, string(body)
		gotMD5, gotAuth = r.Header.Get("Content-MD5"), r.Header.Get("Authorization")
		w.WriteHeader(status)
	}))
	defer srv.Close()
	store, err := NewS3Store(srv.URL, "access", "secret", false)
	if err != nil {
		t.Fatal(err)
	}

	if err = store.EnableVersioning(context.Background(), "reticora-documents"); err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte(gotBody)) //nolint:gosec // see import
	if gotMethod != http.MethodPut || gotPath != "/reticora-documents" || gotQuery != "versioning=" {
		t.Errorf("request: %s %s?%s", gotMethod, gotPath, gotQuery)
	}
	if !strings.Contains(gotBody, "<Status>Enabled</Status>") || gotMD5 != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Errorf("body %q, Content-MD5 %q", gotBody, gotMD5)
	}
	if !strings.HasPrefix(gotAuth, s3Algorithm+" Credential=access/") {
		t.Errorf("Authorization: %q", gotAuth)
	}

	status = http.StatusNotImplemented
	if err = store.EnableVersioning(context.Background(), "reticora-documents"); err == nil || !strings.Contains(err.Error(), "501") {
		t.Errorf("error status: %v", err)
	}
	if err = store.EnableVersioning(context.Background(), " "); err == nil {
		t.Error("empty bucket accepted")
	}
}

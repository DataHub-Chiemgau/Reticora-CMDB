package blob

import (
	"context"
	"net/url"
	"testing"
)

// presignPair signs the same object with a store on the internal endpoint
// and one with a public URL within the same second (X-Amz-Date).
func presignPair(t *testing.T, public string) (internal, published *url.URL) {
	t.Helper()
	plain, err := NewS3Store("minio:9000", "access", "secret", false)
	if err != nil {
		t.Fatal(err)
	}
	routed, err := NewS3Store("minio:9000", "access", "secret", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := routed.SetPublicURL(public); err != nil {
		t.Fatalf("SetPublicURL(%q): %v", public, err)
	}
	for range 3 {
		a, err := plain.PresignedGetURL(context.Background(), "exports", "org-1/job-1.csv")
		if err != nil {
			t.Fatal(err)
		}
		b, err := routed.PresignedGetURL(context.Background(), "exports", "org-1/job-1.csv")
		if err != nil {
			t.Fatal(err)
		}
		internal, _ = url.Parse(a)
		published, _ = url.Parse(b)
		if internal.Query().Get("X-Amz-Date") == published.Query().Get("X-Amz-Date") {
			return internal, published
		}
	}
	t.Fatal("no two signatures within the same second")
	return nil, nil
}

// TestPresignedGetURLNamesThePublicOrigin: with a public URL the signed GET
// URL names the browser-facing origin and path prefix (F-376: before, it
// named http://minio:9000, which a browser cannot resolve and which is mixed
// content on an HTTPS page). The signature stays the one over the internal
// endpoint, because the frontend proxy forwards with Host minio:9000.
func TestPresignedGetURLNamesThePublicOrigin(t *testing.T) {
	internal, published := presignPair(t, "https://cmdb.example.org/objects/")
	if internal.Scheme != "http" || internal.Host != "minio:9000" || internal.Path != "/exports/org-1/job-1.csv" {
		t.Fatalf("internal URL = %s", internal)
	}
	if published.Scheme != "https" || published.Host != "cmdb.example.org" ||
		published.Path != "/objects/exports/org-1/job-1.csv" {
		t.Errorf("public URL = %s, want https://cmdb.example.org/objects/exports/org-1/job-1.csv", published)
	}
	if published.RawQuery != internal.RawQuery {
		t.Errorf("query (signature) differs:\n public   %s\n internal %s", published.RawQuery, internal.RawQuery)
	}
	if published.Query().Get("X-Amz-Expires") != "900" || published.Query().Get("X-Amz-SignedHeaders") != "host" {
		t.Errorf("query = %s", published.RawQuery)
	}
}

func TestPresignedGetURLPublicURLWithoutPathOrWithPort(t *testing.T) {
	_, published := presignPair(t, "http://localhost:3000")
	if published.String()[:len("http://localhost:3000/exports/org-1/job-1.csv?")] != "http://localhost:3000/exports/org-1/job-1.csv?" {
		t.Errorf("public URL = %s", published)
	}
}

func TestSetPublicURLRejectsInvalidURLs(t *testing.T) {
	store, err := NewS3Store("minio:9000", "access", "secret", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"cmdb.example.org/objects", "ftp://cmdb.example.org", "https://cmdb.example.org/objects?x=1", "https://user@cmdb.example.org", "https://"} {
		if err := store.SetPublicURL(raw); err == nil {
			t.Errorf("SetPublicURL(%q) accepted", raw)
		}
	}
	if err := store.SetPublicURL(""); err != nil || store.public != nil {
		t.Errorf("empty public URL: err %v, public %v", err, store.public)
	}
}

package blob

import (
	"context"
	"crypto/md5" //nolint:gosec // verifies the Content-MD5 header
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
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

// TestDeleteAllRemovesVersionsAndMarkers covers WP-1155 (TLC-06): DeleteAll
// lists the versions below the prefix (ListObjectVersions, pages of 1,000
// with key and version markers) and deletes every version and every delete
// marker with its versionId, then aborts the unfinished multipart uploads
// below the prefix (a crashed streaming export); the objects and uploads
// of another prefix stay. A second call deletes nothing; an empty or
// non-directory prefix is refused; a listing still truncated after the
// page limit is ErrListTruncated, not a success.
func TestDeleteAllRemovesVersionsAndMarkers(t *testing.T) {
	type version struct{ key, id string }
	const prefix = "org/11111111-1111-4111-8111-111111111111/"
	var mu sync.Mutex
	stored := []version{{prefix + "documents/a/v1", "va1"}, {prefix + "documents/a/v1", "va2"}, {prefix + "exports/x.zip", "vx1"},
		{prefix + "documents/b/v1", "null"}, {"org/22222222-2222-4222-8222-222222222222/documents/c", "vc1"}}
	markers := []version{{prefix + "documents/a/v1", "ma1"}}
	uploads := []version{{prefix + "org-export/j/pii_subject-part-0001.jsonl", "up-a"},
		{"org/22222222-2222-4222-8222-222222222222/org-export/k/part-0001.jsonl", "up-b"}}
	var deletes, aborts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodGet && q.Has("uploads"):
			var body strings.Builder
			body.WriteString(`<ListMultipartUploadsResult>`)
			for _, u := range uploads {
				if strings.HasPrefix(u.key, q.Get("prefix")) {
					fmt.Fprintf(&body, `<Upload><Key>%s</Key><UploadId>%s</UploadId></Upload>`, u.key, u.id)
				}
			}
			body.WriteString(`<IsTruncated>false</IsTruncated></ListMultipartUploadsResult>`)
			_, _ = io.WriteString(w, body.String())
		case r.Method == http.MethodDelete && q.Get("uploadId") != "":
			key := strings.TrimPrefix(r.URL.Path, "/reticora-documents/")
			aborts = append(aborts, key+"@"+q.Get("uploadId"))
			uploads = slices.DeleteFunc(uploads, func(v version) bool { return v.key == key && v.id == q.Get("uploadId") })
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && q.Has("versions"):
			if q.Get("max-keys") != "1000" {
				t.Errorf("max-keys %q, want 1000", q.Get("max-keys"))
			}
			page := []version{}
			for _, v := range stored {
				if strings.HasPrefix(v.key, q.Get("prefix")) {
					page = append(page, v)
				}
			}
			// Two pages: the first holds one version, the second the rest
			// with the delete markers.
			truncated := q.Get("key-marker") == "" && len(page) > 1
			if truncated {
				page = page[:1]
			}
			var body strings.Builder
			body.WriteString(`<ListVersionsResult>`)
			for _, v := range page {
				fmt.Fprintf(&body, `<Version><Key>%s</Key><VersionId>%s</VersionId></Version>`, v.key, v.id)
			}
			if truncated {
				fmt.Fprintf(&body, `<IsTruncated>true</IsTruncated><NextKeyMarker>%s</NextKeyMarker><NextVersionIdMarker>%s</NextVersionIdMarker>`,
					page[0].key, page[0].id)
			} else {
				for _, m := range markers {
					fmt.Fprintf(&body, `<DeleteMarker><Key>%s</Key><VersionId>%s</VersionId></DeleteMarker>`, m.key, m.id)
				}
				body.WriteString(`<IsTruncated>false</IsTruncated>`)
			}
			body.WriteString(`</ListVersionsResult>`)
			_, _ = io.WriteString(w, body.String())
		case r.Method == http.MethodDelete:
			key := strings.TrimPrefix(r.URL.Path, "/reticora-documents/")
			id := q.Get("versionId")
			if id == "" {
				t.Errorf("DELETE %s without versionId (only a delete marker)", key)
			}
			deletes = append(deletes, key+"@"+id)
			stored = slices.DeleteFunc(stored, func(v version) bool { return v.key == key && v.id == id })
			markers = slices.DeleteFunc(markers, func(v version) bool { return v.key == key && v.id == id })
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	store, err := NewS3Store(srv.URL, "access", "secret", false)
	if err != nil {
		t.Fatal(err)
	}
	n, err := store.DeleteAll(context.Background(), "reticora-documents", prefix)
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 || len(deletes) != 5 || len(aborts) != 1 || aborts[0] != prefix+"org-export/j/pii_subject-part-0001.jsonl@up-a" {
		t.Errorf("deleted %d (%v, aborted %v), want the 4 versions, the marker and the upload of the prefix", n, deletes, aborts)
	}
	if len(uploads) != 1 || uploads[0].id != "up-b" {
		t.Errorf("uploads left %v, want only the one of the other organization", uploads)
	}
	if !slices.Contains(deletes, prefix+"documents/a/v1@ma1") || !slices.Contains(deletes, prefix+"documents/b/v1@null") {
		t.Errorf("deletes %v lack the marker or the null version", deletes)
	}
	if len(stored) != 1 || !strings.HasPrefix(stored[0].key, "org/2222") {
		t.Errorf("left %v, want only the object of the other organization", stored)
	}
	if n, err = store.DeleteAll(context.Background(), "reticora-documents", prefix); err != nil || n != 0 {
		t.Errorf("second DeleteAll: %d %v, want nothing", n, err)
	}
	// More pages than one call reads: an error, never a success.
	mu.Lock()
	stored = append(stored, version{prefix + "documents/d/v1", "vd1"}, version{prefix + "documents/d/v1", "vd2"})
	mu.Unlock()
	pages := maxListPages
	maxListPages = 1
	_, err = store.DeleteAll(context.Background(), "reticora-documents", prefix)
	maxListPages = pages
	if !errors.Is(err, ErrListTruncated) {
		t.Errorf("DeleteAll beyond the page limit: %v, want ErrListTruncated", err)
	}
	for _, bad := range []string{"", "org", "org/../", "/"} {
		if _, err := store.DeleteAll(context.Background(), "reticora-documents", bad); err == nil {
			t.Errorf("prefix %q accepted", bad)
		}
	}
}

// TestFileStoreDeleteAll: the file store lists and removes the files below
// a prefix and keeps the others.
func TestFileStoreDeleteAll(t *testing.T) {
	ctx := context.Background()
	s := NewFileStore(t.TempDir())
	for _, key := range []string{"org/a/documents/1", "org/a/exports/2.zip", "org/b/documents/3"} {
		if err := s.Put(ctx, "exports", key, strings.NewReader("x"), 1, ""); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := s.List(ctx, "exports", "org/a/")
	if err != nil || !slices.Equal(keys, []string{"org/a/documents/1", "org/a/exports/2.zip"}) {
		t.Fatalf("List: %v %v", keys, err)
	}
	if n, err := s.DeleteAll(ctx, "exports", "org/a/"); err != nil || n != 2 {
		t.Fatalf("DeleteAll: %d %v", n, err)
	}
	if keys, _ := s.List(ctx, "exports", "org/a/"); len(keys) != 0 {
		t.Errorf("left %v", keys)
	}
	if keys, _ := s.List(ctx, "exports", "org/b/"); len(keys) != 1 {
		t.Errorf("the other prefix lost its file: %v", keys)
	}
	if n, err := s.DeleteAll(ctx, "exports", "org/a/"); err != nil || n != 0 {
		t.Errorf("second DeleteAll: %d %v", n, err)
	}
}

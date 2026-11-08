package blob

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestS3StoreReportsAMissingObjectAsErrNotFound: an S3 answer 404 with
// NoSuchKey (GET) or without body (HEAD) is ErrNotFound and fs.ErrNotExist;
// a missing bucket and any other error status are not.
func TestS3StoreReportsAMissingObjectAsErrNotFound(t *testing.T) {
	status, body := http.StatusNotFound, "<Error><Code>NoSuchKey</Code></Error>"
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(body))
		}
	}))
	defer srv.Close()
	store, err := NewS3Store(srv.URL, "access", "secret", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err = store.Get(ctx, "bucket", "org/a/report.csv"); !errors.Is(err, ErrNotFound) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("GET 404 NoSuchKey: %v", err)
	}
	if ok, xerr := Exists(ctx, store, "bucket", "org/a/report.csv"); ok || xerr != nil {
		t.Fatalf("HEAD 404: %v %v", ok, xerr)
	}
	if methods[len(methods)-1] != http.MethodHead {
		t.Fatalf("Exists sent %v, want HEAD", methods)
	}

	body = "<Error><Code>NoSuchBucket</Code></Error>"
	if _, err = store.Get(ctx, "bucket", "org/a/report.csv"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("GET 404 NoSuchBucket: %v", err)
	}
	status, body = http.StatusInternalServerError, "boom"
	if _, err = store.Get(ctx, "bucket", "org/a/report.csv"); err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "500") {
		t.Fatalf("GET 500: %v", err)
	}
	if ok, xerr := Exists(ctx, store, "bucket", "org/a/report.csv"); ok || xerr == nil {
		t.Fatalf("HEAD 500: %v %v", ok, xerr)
	}

	status = http.StatusOK
	rc, err := store.Get(ctx, "bucket", "org/a/report.csv")
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if ok, xerr := Exists(ctx, store, "bucket", "org/a/report.csv"); !ok || xerr != nil {
		t.Fatalf("HEAD 200: %v %v", ok, xerr)
	}
}

// TestFileStoreReportsAMissingObjectAsErrNotFound: Get and Delete of a
// missing file are ErrNotFound (and fs.ErrNotExist); Exists follows the file.
func TestFileStoreReportsAMissingObjectAsErrNotFound(t *testing.T) {
	store := NewFileStore(t.TempDir())
	ctx := context.Background()
	if _, err := store.Get(ctx, "bucket", "missing"); !errors.Is(err, ErrNotFound) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Get: %v", err)
	}
	if err := store.Delete(ctx, "bucket", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete: %v", err)
	}
	if ok, err := Exists(ctx, store, "bucket", "present"); ok || err != nil {
		t.Fatalf("Exists before Put: %v %v", ok, err)
	}
	if err := store.Put(ctx, "bucket", "present", bytes.NewReader([]byte("x")), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if ok, err := Exists(ctx, store, "bucket", "present"); !ok || err != nil {
		t.Fatalf("Exists after Put: %v %v", ok, err)
	}
	// A store without a check of its own is asked with Get.
	if ok, err := Exists(ctx, onlyStore{store}, "bucket", "present"); !ok || err != nil {
		t.Fatalf("Exists over Get: %v %v", ok, err)
	}
	if ok, err := Exists(ctx, onlyStore{store}, "bucket", "missing"); ok || err != nil {
		t.Fatalf("Exists over Get, missing: %v %v", ok, err)
	}
}

// onlyStore hides the existence check of the wrapped store.
type onlyStore struct{ Store }

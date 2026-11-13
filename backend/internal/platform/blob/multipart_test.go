package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// WP-1150 (TLC-05): PutStream uploads a stream to S3 as a multipart upload
// in parts of at most 16 MiB with at most one part in memory, and aborts
// the upload (AbortMultipartUpload) when the stream or a part fails.
//
// Retest:
//   cd backend && go test ./internal/platform/blob/ -run TestPutStream

// s3Stub records the multipart calls of an S3 endpoint.
type s3Stub struct {
	mu        sync.Mutex
	parts     map[string]int // partNumber -> bytes
	completed bool
	aborted   bool
	plainPut  int
	failPart  string
	// readAt is the bytes the source had produced when each part arrived.
	readAt []int64
	source *countingReader
	// open are unfinished uploads ListMultipartUploads reports (key ->
	// upload ids), abortedIDs the ids an AbortMultipartUpload named.
	open       map[string][]string
	abortedIDs []string
}

func (s *s3Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := r.URL.Query()
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodGet && q.Has("uploads"):
		var b strings.Builder
		b.WriteString(`<ListMultipartUploadsResult>`)
		for key, ids := range s.open {
			if !strings.HasPrefix(key, q.Get("prefix")) {
				continue
			}
			for _, id := range ids {
				b.WriteString(`<Upload><Key>` + key + `</Key><UploadId>` + id + `</UploadId></Upload>`)
			}
		}
		b.WriteString(`<IsTruncated>false</IsTruncated></ListMultipartUploadsResult>`)
		_, _ = io.WriteString(w, b.String())
	case r.Method == http.MethodDelete && q.Get("uploadId") != "" && q.Get("uploadId") != "up-1":
		s.abortedIDs = append(s.abortedIDs, q.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && q.Has("uploads"):
		_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>up-1</UploadId></InitiateMultipartUploadResult>`)
	case r.Method == http.MethodPut && q.Get("uploadId") == "up-1":
		if q.Get("partNumber") == s.failPart {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if s.source != nil {
			s.readAt = append(s.readAt, s.source.n.Load())
		}
		s.parts[q.Get("partNumber")] = len(body)
		w.Header().Set("ETag", `"etag-`+q.Get("partNumber")+`"`)
	case r.Method == http.MethodPost && q.Get("uploadId") == "up-1":
		if !strings.Contains(string(body), "<PartNumber>1</PartNumber>") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.completed = true
		_, _ = io.WriteString(w, `<CompleteMultipartUploadResult></CompleteMultipartUploadResult>`)
	case r.Method == http.MethodDelete && q.Get("uploadId") == "up-1":
		s.aborted = true
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		s.plainPut++
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

// countingReader produces n bytes in small reads and counts them; an error
// (failErr, else "source broken") ends the stream after failAfter bytes.
type countingReader struct {
	n         atomic.Int64
	size      int64
	failAfter int64
	failErr   error
}

func (c *countingReader) Read(p []byte) (int, error) {
	done := c.n.Load()
	if c.failAfter > 0 && done >= c.failAfter {
		if c.failErr != nil {
			return 0, c.failErr
		}
		return 0, errors.New("source broken")
	}
	if done >= c.size {
		return 0, io.EOF
	}
	n := int64(min(len(p), 64<<10))
	n = min(n, c.size-done)
	for i := range n {
		p[i] = byte(done + i)
	}
	c.n.Add(n)
	return int(n), nil
}

func newStub(t *testing.T) (*s3Stub, *S3Store) {
	t.Helper()
	stub := &s3Stub{parts: map[string]int{}}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	store, err := NewS3Store(srv.URL, "access", "secret", false)
	if err != nil {
		t.Fatal(err)
	}
	return stub, store
}

func TestPutStreamUsesMultipart(t *testing.T) {
	stub, store := newStub(t)
	src := &countingReader{size: 2*MultipartPartSize + 5<<20}
	stub.source = src
	if err := store.PutStream(context.Background(), "exports", "org/x/a.zip", src, "application/zip"); err != nil {
		t.Fatal(err)
	}
	if !stub.completed || stub.aborted || stub.plainPut != 0 {
		t.Fatalf("completed %v aborted %v plain %d", stub.completed, stub.aborted, stub.plainPut)
	}
	want := map[string]int{"1": MultipartPartSize, "2": MultipartPartSize, "3": 5 << 20}
	for k, v := range want {
		if stub.parts[k] != v {
			t.Fatalf("parts %v, want %v", stub.parts, want)
		}
	}
	// When part k arrives the source has produced at most k parts: the
	// upload holds no more than the one part it sends.
	for i, n := range stub.readAt {
		if n > int64(i+1)*MultipartPartSize {
			t.Fatalf("part %d arrived after %d bytes were read", i+1, n)
		}
	}
	// A small stream is one plain PUT.
	if err := store.PutStream(context.Background(), "exports", "org/x/b.zip", bytes.NewReader([]byte("small")), ""); err != nil {
		t.Fatal(err)
	}
	if stub.plainPut != 1 {
		t.Fatalf("plain PUTs %d", stub.plainPut)
	}
}

func TestPutStreamAbortsOnError(t *testing.T) {
	stub, store := newStub(t)
	src := &countingReader{size: 3 * MultipartPartSize, failAfter: MultipartPartSize + 1<<20}
	if err := store.PutStream(context.Background(), "exports", "org/x/a.zip", src, ""); err == nil {
		t.Fatal("no error for a broken source")
	}
	if !stub.aborted || stub.completed {
		t.Fatalf("source error: aborted %v completed %v", stub.aborted, stub.completed)
	}

	stub, store = newStub(t)
	stub.failPart = "2"
	if err := store.PutStream(context.Background(), "exports", "org/x/a.zip", &countingReader{size: 3 * MultipartPartSize}, ""); err == nil {
		t.Fatal("no error for a refused part")
	}
	if !stub.aborted || stub.completed {
		t.Fatalf("part error: aborted %v completed %v", stub.aborted, stub.completed)
	}
}

// A source error that wraps io.ErrUnexpectedEOF (a document body cut off
// mid-transfer, handed on by the ZIP writer) is a failure, not the end of
// the stream: no plain PUT, no completed upload, the multipart is aborted.
func TestPutStreamAbortsOnWrappedEOF(t *testing.T) {
	cut := fmt.Errorf("stream document x version 1: %w", io.ErrUnexpectedEOF)
	for _, tc := range []struct {
		name      string
		failAfter int64
		multipart bool
	}{
		{"plain PUT", 1 << 20, false},
		{"multipart", MultipartPartSize + 1<<20, true},
	} {
		stub, store := newStub(t)
		src := &countingReader{size: 3 * MultipartPartSize, failAfter: tc.failAfter, failErr: cut}
		err := store.PutStream(context.Background(), "exports", "org/x/a.zip", src, "")
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("%s: error %v", tc.name, err)
		}
		if stub.plainPut != 0 || stub.completed || stub.aborted != tc.multipart {
			t.Fatalf("%s: plain %d completed %v aborted %v", tc.name, stub.plainPut, stub.completed, stub.aborted)
		}
	}
}

// AbortUploads removes the unfinished uploads of exactly one key, left by a
// process that ended before it could abort them.
func TestAbortUploadsRemovesUnfinishedUploads(t *testing.T) {
	stub, store := newStub(t)
	stub.open = map[string][]string{
		"org/x/documents-1.zip":  {"crashed-1", "crashed-2"},
		"org/x/documents-1.zip2": {"other"},
	}
	if err := AbortUploads(context.Background(), store, "exports", "org/x/documents-1.zip"); err != nil {
		t.Fatal(err)
	}
	slices.Sort(stub.abortedIDs)
	if !slices.Equal(stub.abortedIDs, []string{"crashed-1", "crashed-2"}) {
		t.Fatalf("aborted %v", stub.abortedIDs)
	}
	// Stores without multipart uploads have nothing to abort.
	if err := AbortUploads(context.Background(), NewFileStore(t.TempDir()), "exports", "org/x/a.zip"); err != nil {
		t.Fatal(err)
	}
}

func TestFileStorePutStreamLeavesNoPartialFile(t *testing.T) {
	store := NewFileStore(t.TempDir())
	ctx := context.Background()
	err := store.PutStream(ctx, "exports", "org/x/a.zip", &countingReader{size: 1 << 20, failAfter: 1000}, "")
	if err == nil {
		t.Fatal("no error")
	}
	if ok, _ := Exists(ctx, store, "exports", "org/x/a.zip"); ok {
		t.Fatal("partial object")
	}
	if err = store.PutStream(ctx, "exports", "org/x/a.zip", strings.NewReader("done"), ""); err != nil {
		t.Fatal(err)
	}
	rc, err := store.Get(ctx, "exports", "org/x/a.zip")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(b) != "done" {
		t.Fatalf("content %q", b)
	}
}

// Package blob provides MinIO/S3 blob storage operations for the Reticora platform.
package blob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	s3Algorithm        = "AWS4-HMAC-SHA256"
	s3Region           = "us-east-1"
	s3Service          = "s3"
	s3PresignTTL       = 15 * time.Minute
	emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// ErrForeignKey reports an object key outside the organization's prefix.
var ErrForeignKey = errors.New("blob: key outside the organization prefix")

// OrgPrefix is the key prefix of all objects of an organization (TEN-06).
func OrgPrefix(orgID string) string {
	return "org/" + orgID + "/"
}

// OrgKey builds a server-side object key below the organization's prefix:
// org/<orgID>/<parts joined by "/">. Keys must never come from a request.
func OrgKey(orgID string, parts ...string) string {
	return OrgPrefix(orgID) + strings.Join(parts, "/")
}

// CheckOrgKey verifies that key lies below the organization's prefix and
// contains no path traversal or empty segments.
func CheckOrgKey(orgID, key string) error {
	if orgID == "" || !strings.HasPrefix(key, OrgPrefix(orgID)) {
		return ErrForeignKey
	}
	for _, segment := range strings.Split(strings.TrimPrefix(key, OrgPrefix(orgID)), "/") {
		if segment == "" || segment == "." || segment == ".." || strings.Contains(segment, "\\") {
			return ErrForeignKey
		}
	}
	return nil
}

// Store defines the interface for blob/object storage operations.
type Store interface {
	Put(ctx context.Context, bucket, key string, reader io.Reader, size int64, contentType string) error
	Get(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, bucket, key string) error
	// PresignedGetURL returns a time-limited URL that delivers the object
	// only as a download (Download): the caller names the file name and the
	// verified type, or leaves them empty for a nameless
	// application/octet-stream attachment.
	PresignedGetURL(ctx context.Context, bucket, key string, dl Download) (string, error)
}

// FileStore stores blobs on the local filesystem.
type FileStore struct {
	baseDir string
}

// NewFileStore creates a filesystem-backed blob store rooted at baseDir.
func NewFileStore(baseDir string) *FileStore {
	if abs, err := filepath.Abs(baseDir); err == nil {
		baseDir = abs
	}
	return &FileStore{baseDir: filepath.Clean(baseDir)}
}

// Put stores an object on the local filesystem.
func (s *FileStore) Put(_ context.Context, bucket, key string, reader io.Reader, _ int64, _ string) error {
	objectPath, err := s.objectPath(bucket, key)
	if err != nil {
		return err
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(objectPath), 0o755); mkdirErr != nil {
		return fmt.Errorf("blob: create directory for %q: %w", objectPath, mkdirErr)
	}

	file, err := os.OpenFile(objectPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("blob: open file %q: %w", objectPath, err)
	}
	defer file.Close()

	if _, err := io.Copy(file, reader); err != nil {
		return fmt.Errorf("blob: write file %q: %w", objectPath, err)
	}
	return nil
}

// Get opens an object from the local filesystem.
func (s *FileStore) Get(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	objectPath, err := s.objectPath(bucket, key)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(objectPath)
	if errors.Is(err, os.ErrNotExist) {
		err = ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("blob: open file %q: %w", objectPath, err)
	}
	return file, nil
}

// Delete removes an object from the local filesystem.
func (s *FileStore) Delete(_ context.Context, bucket, key string) error {
	objectPath, err := s.objectPath(bucket, key)
	if err != nil {
		return err
	}
	err = os.Remove(objectPath)
	if errors.Is(err, os.ErrNotExist) {
		err = ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("blob: delete file %q: %w", objectPath, err)
	}
	return nil
}

// PresignedGetURL returns a development-only file URL for the object. A
// file URL carries no response headers, so dl is not applied; a web page
// cannot open file URLs anyway.
func (s *FileStore) PresignedGetURL(_ context.Context, bucket, key string, _ Download) (string, error) {
	objectPath, err := s.objectPath(bucket, key)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(objectPath)}).String(), nil
}

func (s *FileStore) objectPath(bucket, key string) (string, error) {
	if strings.TrimSpace(bucket) == "" {
		return "", fmt.Errorf("blob: bucket is required")
	}
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("blob: key is required")
	}

	base := filepath.Clean(s.baseDir)
	fullPath := filepath.Clean(filepath.Join(base, filepath.FromSlash(bucket), filepath.FromSlash(key)))
	rel, err := filepath.Rel(base, fullPath)
	if err != nil {
		return "", fmt.Errorf("blob: resolve object path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("blob: invalid path %q/%q", bucket, key)
	}
	return fullPath, nil
}

// S3Store stores blobs in an S3-compatible object store.
type S3Store struct {
	endpoint  string
	accessKey string
	secretKey string
	useSSL    bool
	client    *http.Client
	// public, when set, is the base URL under which browsers reach the
	// object store (SetPublicURL); signed GET URLs name it instead of the
	// internal endpoint.
	public *url.URL
}

// SetPublicURL sets the base URL (http(s)://host[:port][/path]) under which
// browsers reach the object store, e.g. <public base URL>/objects behind the
// frontend's reverse proxy. Signed GET URLs keep the signature over the
// internal endpoint (the proxy forwards with that Host) but name this base:
// the internal name (minio:9000) is not resolvable for browsers and, from an
// HTTPS page, would be mixed content (F-376). An empty raw keeps the
// internal endpoint.
func (s *S3Store) SetPublicURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		s.public = nil
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return fmt.Errorf("blob: public URL %q must be of the form http(s)://host[:port][/path]", raw)
	}
	trimmed := strings.Trim(parsed.Path, "/")
	parsed.Path = ""
	if trimmed != "" {
		parsed.Path = "/" + trimmed
	}
	s.public = parsed
	return nil
}

// NewS3Store creates a minimal S3-compatible blob store.
func NewS3Store(endpoint, accessKey, secretKey string, useSSL bool) (*S3Store, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("blob: endpoint is required")
	}
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("blob: parse endpoint: %w", err)
		}
		if parsed.Host == "" {
			return nil, fmt.Errorf("blob: endpoint host is required")
		}
		endpoint = parsed.Host
		switch parsed.Scheme {
		case "https":
			useSSL = true
		case "http":
			useSSL = false
		}
	}
	if strings.TrimSpace(accessKey) == "" {
		return nil, fmt.Errorf("blob: access key is required")
	}
	if strings.TrimSpace(secretKey) == "" {
		return nil, fmt.Errorf("blob: secret key is required")
	}

	return &S3Store{
		endpoint:  strings.TrimRight(endpoint, "/"),
		accessKey: accessKey,
		secretKey: secretKey,
		useSSL:    useSSL,
		client:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Put uploads an object to the S3-compatible backend.
func (s *S3Store) Put(ctx context.Context, bucket, key string, reader io.Reader, _ int64, contentType string) error {
	payload, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("blob: read upload payload: %w", err)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	resp, err := s.doRequest(ctx, http.MethodPut, bucket, key, bytes.NewReader(payload), contentType, payloadSHA256(payload), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// EnsureBucket creates the bucket when it does not exist yet. A pre-existing
// bucket (or an owning-but-foreign 409) is treated as success so startup is
// idempotent; genuine failures are returned so the caller can decide whether
// the feature depending on the bucket stays disabled.
func (s *S3Store) EnsureBucket(ctx context.Context, bucket string) error {
	if strings.TrimSpace(bucket) == "" {
		return fmt.Errorf("blob: bucket is required")
	}
	scheme := "http"
	if s.useSSL {
		scheme = "https"
	}
	bucketURL := (&url.URL{Scheme: scheme, Host: s.endpoint, Path: "/" + bucket}).String()
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, bucketURL, http.NoBody)
	if err != nil {
		return fmt.Errorf("blob: create bucket request: %w", err)
	}
	if signErr := s.signRequest(req, emptyPayloadSHA256); signErr != nil {
		return fmt.Errorf("blob: sign bucket request: %w", signErr)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("blob: ensure bucket %s: %w", bucket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// BucketAlreadyOwnedByYou / BucketAlreadyExists surface as 409; MinIO
	// returns 200 for an existing own bucket. Treat both as satisfied.
	if resp.StatusCode == http.StatusConflict {
		return nil
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return fmt.Errorf("blob: ensure bucket %s returned %s: %s", bucket, resp.Status, strings.TrimSpace(string(bodyBytes)))
}

// versioningEnabled is the PutBucketVersioning body that enables versioning.
const versioningEnabled = `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status></VersioningConfiguration>`

// EnableVersioning switches on object versioning for bucket (NFR-05,
// OPS-04): an overwritten or deleted document or export keeps its previous
// versions and stays recoverable. The call is idempotent.
func (s *S3Store) EnableVersioning(ctx context.Context, bucket string) error {
	if strings.TrimSpace(bucket) == "" {
		return fmt.Errorf("blob: bucket is required")
	}
	scheme := "http"
	if s.useSSL {
		scheme = "https"
	}
	body := []byte(versioningEnabled)
	versioningURL := (&url.URL{Scheme: scheme, Host: s.endpoint, Path: "/" + bucket, RawQuery: "versioning="}).String()
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, versioningURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("blob: create versioning request: %w", err)
	}
	sum := md5.Sum(body) //nolint:gosec // Content-MD5 is the integrity header S3 requires here, not a security hash.
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
	if err = s.signRequest(req, payloadSHA256(body)); err != nil {
		return fmt.Errorf("blob: sign versioning request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("blob: enable versioning on %s: %w", bucket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return fmt.Errorf("blob: enable versioning on %s returned %s: %s", bucket, resp.Status, strings.TrimSpace(string(bodyBytes)))
}

// Get downloads an object from the S3-compatible backend.
func (s *S3Store) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	resp, err := s.doRequest(ctx, http.MethodGet, bucket, key, nil, "", emptyPayloadSHA256, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Delete removes an object from the S3-compatible backend.
func (s *S3Store) Delete(ctx context.Context, bucket, key string) error {
	resp, err := s.doRequest(ctx, http.MethodDelete, bucket, key, nil, "", emptyPayloadSHA256, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// PresignedGetURL creates a time-limited GET URL signed with SigV4 query
// parameters. The URL carries response-content-disposition (attachment,
// with the file name of dl) and response-content-type (the type of dl or
// application/octet-stream) inside the signed query: the object store
// answers with these headers, and the holder of the URL cannot change them
// (SEC-11, finding of WP-829).
func (s *S3Store) PresignedGetURL(_ context.Context, bucket, key string, dl Download) (string, error) {
	objectURL, err := s.objectURL(bucket, key)
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	credentialScope := s.credentialScope(dateStamp)
	canonicalURI := canonicalURI(bucket, key)

	queryValues := url.Values{}
	queryValues.Set("X-Amz-Algorithm", s3Algorithm)
	queryValues.Set("X-Amz-Credential", s.accessKey+"/"+credentialScope)
	queryValues.Set("X-Amz-Date", amzDate)
	queryValues.Set("X-Amz-Expires", strconv.FormatInt(int64(s3PresignTTL/time.Second), 10))
	queryValues.Set("X-Amz-SignedHeaders", "host")
	queryValues.Set("response-content-disposition", dl.disposition())
	queryValues.Set("response-content-type", dl.contentType())
	canonicalQuery := canonicalQueryString(queryValues)
	canonicalHeaders := fmt.Sprintf("host:%s\n", objectURL.Host)

	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")

	stringToSign := strings.Join([]string{
		s3Algorithm,
		amzDate,
		credentialScope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signature := hex.EncodeToString(hmacSHA256(s.signingKey(dateStamp), stringToSign))
	queryValues.Set("X-Amz-Signature", signature)
	objectURL.RawQuery = canonicalQueryString(queryValues)
	if s.public != nil {
		objectURL.Scheme = s.public.Scheme
		objectURL.Host = s.public.Host
		objectURL.Path = s.public.Path + objectURL.Path
	}
	return objectURL.String(), nil
}

func (s *S3Store) doRequest(ctx context.Context, method, bucket, key string, body io.Reader, contentType, payloadHash string, extraHeaders http.Header) (*http.Response, error) {
	objectURL, err := s.objectURL(bucket, key)
	if err != nil {
		return nil, err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if payloadHash == "" {
		payloadHash = emptyPayloadSHA256
	}

	req, err := http.NewRequestWithContext(ctx, method, objectURL.String(), body)
	if err != nil {
		return nil, fmt.Errorf("blob: create %s request: %w", method, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for key, values := range extraHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if signErr := s.signRequest(req, payloadHash); signErr != nil {
		return nil, signErr
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("blob: %s %s: %w", method, objectURL.String(), err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}

	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return nil, statusError(method, objectURL.String(), resp, strings.TrimSpace(string(bodyBytes)))
}

func (s *S3Store) signRequest(req *http.Request, payloadHash string) error {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	credentialScope := s.credentialScope(dateStamp)
	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n", req.URL.Host, payloadHash, amzDate)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		canonicalQueryString(req.URL.Query()),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{
		s3Algorithm,
		amzDate,
		credentialScope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(s.signingKey(dateStamp), stringToSign))

	req.Host = req.URL.Host
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s3Algorithm,
		s.accessKey,
		credentialScope,
		signedHeaders,
		signature,
	))
	return nil
}

func (s *S3Store) objectURL(bucket, key string) (*url.URL, error) {
	if strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("blob: bucket is required")
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("blob: key is required")
	}

	scheme := "http"
	if s.useSSL {
		scheme = "https"
	}

	return &url.URL{
		Scheme: scheme,
		Host:   s.endpoint,
		Path:   canonicalURI(bucket, key),
	}, nil
}

func (s *S3Store) credentialScope(dateStamp string) string {
	return strings.Join([]string{dateStamp, s3Region, s3Service, "aws4_request"}, "/")
}

func (s *S3Store) signingKey(dateStamp string) []byte {
	dateKey := hmacSHA256([]byte("AWS4"+s.secretKey), dateStamp)
	regionKey := hmacSHA256(dateKey, s3Region)
	serviceKey := hmacSHA256(regionKey, s3Service)
	return hmacSHA256(serviceKey, "aws4_request")
}

func canonicalURI(bucket, key string) string {
	segments := []string{url.PathEscape(bucket)}
	for _, segment := range strings.Split(key, "/") {
		segments = append(segments, url.PathEscape(segment))
	}
	return "/" + strings.Join(segments, "/")
}

func canonicalQueryString(values url.Values) string {
	if len(values) == 0 {
		return ""
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		encodedKey := url.QueryEscape(key)
		sortedValues := append([]string(nil), values[key]...)
		sort.Strings(sortedValues)
		for _, value := range sortedValues {
			parts = append(parts, encodedKey+"="+url.QueryEscape(value))
		}
	}
	return strings.ReplaceAll(strings.Join(parts, "&"), "+", "%20")
}

func payloadSHA256(payload []byte) string {
	return hexSHA256(payload)
}

func hexSHA256(payload []byte) string {
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(data))
	return mac.Sum(nil)
}

// ─── Streaming upload (TLC-05, WP-1150) ─────────────────────────────────────

// MultipartPartSize is the size of one part of a streamed upload to S3:
// PutStream holds at most one part in memory.
const MultipartPartSize = 16 << 20

// StreamStore is a store that uploads a stream of unknown length without
// holding it in memory (S3Store: multipart upload, FileStore: file).
type StreamStore interface {
	PutStream(ctx context.Context, bucket, key string, reader io.Reader, contentType string) error
}

// PutStream uploads reader as the object key: through StreamStore when the
// store has it, else (test doubles) with one Put of the read content. A
// failed upload leaves no object behind.
func PutStream(ctx context.Context, s Store, bucket, key string, reader io.Reader, contentType string) error {
	if ss, ok := s.(StreamStore); ok {
		return ss.PutStream(ctx, bucket, key, reader, contentType)
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("blob: read upload stream: %w", err)
	}
	return s.Put(ctx, bucket, key, bytes.NewReader(payload), int64(len(payload)), contentType)
}

// PutStream implements StreamStore: the stream goes straight into a file
// next to the object, renamed to it once complete; a failed stream removes
// the file, so no partial object remains.
func (s *FileStore) PutStream(_ context.Context, bucket, key string, reader io.Reader, _ string) error {
	objectPath, err := s.objectPath(bucket, key)
	if err != nil {
		return err
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(objectPath), 0o755); mkdirErr != nil {
		return fmt.Errorf("blob: create directory for %q: %w", objectPath, mkdirErr)
	}
	tmp, err := os.CreateTemp(filepath.Dir(objectPath), "."+filepath.Base(objectPath)+".part-*")
	if err != nil {
		return fmt.Errorf("blob: create file for %q: %w", objectPath, err)
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := io.Copy(tmp, reader); err != nil {
		return fmt.Errorf("blob: write file %q: %w", objectPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("blob: close file %q: %w", objectPath, err)
	}
	if err := os.Rename(tmp.Name(), objectPath); err != nil {
		return fmt.Errorf("blob: store file %q: %w", objectPath, err)
	}
	done = true
	return nil
}

// PutStream implements StreamStore with a multipart upload in parts of
// MultipartPartSize: one buffer of one part is reused, each part is sent
// before the next is read. A stream that fits into the first part is one
// PUT. Any failure aborts the upload (AbortMultipartUpload), so S3 keeps
// neither an object nor its parts.
func (s *S3Store) PutStream(ctx context.Context, bucket, key string, reader io.Reader, contentType string) error {
	return s.putStream(ctx, bucket, key, reader, contentType, MultipartPartSize)
}

func (s *S3Store) putStream(ctx context.Context, bucket, key string, reader io.Reader, contentType string, partSize int) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	buf := make([]byte, partSize)
	n, err := io.ReadFull(reader, buf)
	if streamFailed(err) {
		return fmt.Errorf("blob: read upload stream: %w", err)
	}
	if n < partSize {
		// The whole stream fits into one part: a plain PUT.
		resp, perr := s.doRequest(ctx, http.MethodPut, bucket, key, bytes.NewReader(buf[:n]), contentType, payloadSHA256(buf[:n]), nil)
		if perr != nil {
			return perr
		}
		return resp.Body.Close()
	}
	uploadID, err := s.createMultipart(ctx, bucket, key, contentType)
	if err != nil {
		return err
	}
	var etags []string
	fail := func(cause error) error {
		if aerr := s.abortMultipart(context.WithoutCancel(ctx), bucket, key, uploadID); aerr != nil {
			return errors.Join(cause, aerr)
		}
		return cause
	}
	for part := 1; n > 0; part++ {
		etag, perr := s.uploadPart(ctx, bucket, key, uploadID, part, buf[:n])
		if perr != nil {
			return fail(perr)
		}
		etags = append(etags, etag)
		n, err = io.ReadFull(reader, buf)
		if streamFailed(err) {
			return fail(fmt.Errorf("blob: read upload stream: %w", err))
		}
	}
	if err := s.completeMultipart(ctx, bucket, key, uploadID, etags); err != nil {
		return fail(err)
	}
	return nil
}

// streamFailed reports whether io.ReadFull ended with an error of the
// source rather than at the end of the stream. ReadFull returns io.EOF and
// io.ErrUnexpectedEOF itself unwrapped; the sentinels are compared by
// identity, so a source error that wraps one of them (a document body cut
// off mid-transfer, handed on through CloseWithError) aborts the upload
// instead of storing a truncated object.
func streamFailed(err error) bool {
	//nolint:errorlint // identity on purpose, see above
	return err != nil && err != io.EOF && err != io.ErrUnexpectedEOF
}

// UploadAborter is a store that can remove the unfinished multipart uploads
// of a key, left behind when the process that started them ended (crash,
// kill) before it could abort them.
type UploadAborter interface {
	AbortUploads(ctx context.Context, bucket, key string) error
}

// AbortUploads removes the unfinished uploads of key when the store keeps
// any (S3Store); other stores have none and return nil.
func AbortUploads(ctx context.Context, s Store, bucket, key string) error {
	if a, ok := s.(UploadAborter); ok {
		return a.AbortUploads(ctx, bucket, key)
	}
	return nil
}

// AbortUploads implements UploadAborter: ListMultipartUploads with the key
// as prefix, then AbortMultipartUpload for every upload of exactly that key.
func (s *S3Store) AbortUploads(ctx context.Context, bucket, key string) error {
	if strings.TrimSpace(bucket) == "" || strings.TrimSpace(key) == "" {
		return fmt.Errorf("blob: bucket and key are required")
	}
	scheme := "http"
	if s.useSSL {
		scheme = "https"
	}
	query := url.Values{"uploads": {""}, "prefix": {key}}
	var errs []error
	// A key has few unfinished uploads; the pages are followed regardless.
	for range 100 {
		listURL := &url.URL{Scheme: scheme, Host: s.endpoint, Path: "/" + bucket, RawQuery: canonicalQueryString(query)}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL.String(), http.NoBody)
		if err != nil {
			return fmt.Errorf("blob: create list uploads request: %w", err)
		}
		if serr := s.signRequest(req, emptyPayloadSHA256); serr != nil {
			return serr
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return fmt.Errorf("blob: list uploads of %s: %w", key, err)
		}
		var out struct {
			Uploads []struct {
				Key      string `xml:"Key"`
				UploadID string `xml:"UploadId"`
			} `xml:"Upload"`
			IsTruncated        bool   `xml:"IsTruncated"`
			NextKeyMarker      string `xml:"NextKeyMarker"`
			NextUploadIDMarker string `xml:"NextUploadIdMarker"`
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
			_ = resp.Body.Close()
			return statusError(http.MethodGet, listURL.Path, resp, strings.TrimSpace(string(body)))
		}
		derr := xml.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out)
		_ = resp.Body.Close()
		if derr != nil {
			return fmt.Errorf("blob: list uploads of %s: %w", key, derr)
		}
		for _, u := range out.Uploads {
			if u.Key != key || u.UploadID == "" {
				continue
			}
			if aerr := s.abortMultipart(ctx, bucket, key, u.UploadID); aerr != nil && !errors.Is(aerr, ErrNotFound) {
				errs = append(errs, aerr)
			}
		}
		if !out.IsTruncated || out.NextKeyMarker == "" {
			return errors.Join(errs...)
		}
		query.Set("key-marker", out.NextKeyMarker)
		query.Set("upload-id-marker", out.NextUploadIDMarker)
	}
	return errors.Join(errs...)
}

// queryRequest sends a signed request to the object with the query.
func (s *S3Store) queryRequest(ctx context.Context, method, bucket, key string, query url.Values, body []byte, contentType string) (*http.Response, error) {
	objectURL, err := s.objectURL(bucket, key)
	if err != nil {
		return nil, err
	}
	objectURL.RawQuery = canonicalQueryString(query)
	if body == nil {
		body = []byte{}
	}
	req, err := http.NewRequestWithContext(ctx, method, objectURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("blob: create %s request: %w", method, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if serr := s.signRequest(req, payloadSHA256(body)); serr != nil {
		return nil, serr
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("blob: %s %s: %w", method, objectURL.Path, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return nil, statusError(method, objectURL.Path, resp, strings.TrimSpace(string(bodyBytes)))
}

// createMultipart starts a multipart upload and returns its upload id.
func (s *S3Store) createMultipart(ctx context.Context, bucket, key, contentType string) (string, error) {
	resp, err := s.queryRequest(ctx, http.MethodPost, bucket, key, url.Values{"uploads": {""}}, nil, contentType)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.UploadID == "" {
		return "", fmt.Errorf("blob: start multipart upload of %s: no upload id", key)
	}
	return out.UploadID, nil
}

// uploadPart sends one part and returns its ETag.
func (s *S3Store) uploadPart(ctx context.Context, bucket, key, uploadID string, part int, data []byte) (string, error) {
	resp, err := s.queryRequest(ctx, http.MethodPut, bucket, key,
		url.Values{"partNumber": {strconv.Itoa(part)}, "uploadId": {uploadID}}, data, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	etag := resp.Header.Get("ETag")
	if etag == "" {
		return "", fmt.Errorf("blob: part %d of %s has no ETag", part, key)
	}
	return etag, nil
}

// completeMultipart joins the parts into the object.
func (s *S3Store) completeMultipart(ctx context.Context, bucket, key, uploadID string, etags []string) error {
	type part struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	}
	body := struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []part   `xml:"Part"`
	}{}
	for i, etag := range etags {
		body.Parts = append(body.Parts, part{PartNumber: i + 1, ETag: etag})
	}
	raw, err := xml.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := s.queryRequest(ctx, http.MethodPost, bucket, key, url.Values{"uploadId": {uploadID}}, raw, "application/xml")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// S3 may answer 200 with an error document when the join fails late.
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if bytes.Contains(payload, []byte("<Error>")) {
		return fmt.Errorf("blob: complete multipart upload of %s failed", key)
	}
	return nil
}

// abortMultipart removes an unfinished upload with its parts.
func (s *S3Store) abortMultipart(ctx context.Context, bucket, key, uploadID string) error {
	resp, err := s.queryRequest(ctx, http.MethodDelete, bucket, key, url.Values{"uploadId": {uploadID}}, nil, "")
	if err != nil {
		return fmt.Errorf("blob: abort multipart upload of %s: %w", key, err)
	}
	return resp.Body.Close()
}

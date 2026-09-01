// Package blob provides MinIO/S3 blob storage operations for the Reticora platform.
package blob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

// Store defines the interface for blob/object storage operations.
type Store interface {
	Put(ctx context.Context, bucket, key string, reader io.Reader, size int64, contentType string) error
	Get(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, bucket, key string) error
	PresignedGetURL(ctx context.Context, bucket, key string) (string, error)
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
	if err := os.MkdirAll(filepath.Dir(objectPath), 0o755); err != nil {
		return fmt.Errorf("blob: create directory for %q: %w", objectPath, err)
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
	if err := os.Remove(objectPath); err != nil {
		return fmt.Errorf("blob: delete file %q: %w", objectPath, err)
	}
	return nil
}

// PresignedGetURL returns a development-only file URL for the object.
func (s *FileStore) PresignedGetURL(_ context.Context, bucket, key string) (string, error) {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, bucketURL, nil)
	if err != nil {
		return fmt.Errorf("blob: create bucket request: %w", err)
	}
	if err := s.signRequest(req, emptyPayloadSHA256); err != nil {
		return fmt.Errorf("blob: sign bucket request: %w", err)
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

// PresignedGetURL creates a time-limited GET URL signed with SigV4 query parameters.
func (s *S3Store) PresignedGetURL(_ context.Context, bucket, key string) (string, error) {
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
	if err := s.signRequest(req, payloadHash); err != nil {
		return nil, err
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
	return nil, fmt.Errorf("blob: %s %s returned %s: %s", method, objectURL.String(), resp.Status, strings.TrimSpace(string(bodyBytes)))
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
		"",
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

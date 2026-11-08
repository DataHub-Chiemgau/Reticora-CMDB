package blob

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
)

// ErrNotFound reports an object that does not exist: a missing file of the
// FileStore, an S3 answer 404 (NoSuchKey). It wraps fs.ErrNotExist, so
// errors.Is(err, fs.ErrNotExist) holds as well.
var ErrNotFound = fmt.Errorf("blob: object not found: %w", fs.ErrNotExist)

// statusError is the error of an S3 answer outside 2xx: a 404 for an object
// (not a missing bucket, NoSuchBucket, which is a configuration error)
// wraps ErrNotFound.
func statusError(method, objectURL string, resp *http.Response, body string) error {
	if resp.StatusCode == http.StatusNotFound && !strings.Contains(body, "NoSuchBucket") {
		return fmt.Errorf("blob: %s %s returned %s: %w", method, objectURL, resp.Status, ErrNotFound)
	}
	return fmt.Errorf("blob: %s %s returned %s: %s", method, objectURL, resp.Status, body)
}

// existsChecker is a store that answers whether an object exists without
// reading it.
type existsChecker interface {
	exists(ctx context.Context, bucket, key string) (bool, error)
}

// Exists reports whether the object exists; a missing object is false
// without error. A store without a check of its own is asked with Get.
func Exists(ctx context.Context, store Store, bucket, key string) (bool, error) {
	if c, ok := store.(existsChecker); ok {
		return c.exists(ctx, bucket, key)
	}
	rc, err := store.Get(ctx, bucket, key)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = rc.Close()
	return true, nil
}

// exists asks the file system.
func (s *FileStore) exists(_ context.Context, bucket, key string) (bool, error) {
	objectPath, err := s.objectPath(bucket, key)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(objectPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("blob: stat file %q: %w", objectPath, err)
	}
	return true, nil
}

// exists sends HEAD for the object; 404 is false.
func (s *S3Store) exists(ctx context.Context, bucket, key string) (bool, error) {
	resp, err := s.doRequest(ctx, http.MethodHead, bucket, key, nil, "", emptyPayloadSHA256, nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = resp.Body.Close()
	return true, nil
}

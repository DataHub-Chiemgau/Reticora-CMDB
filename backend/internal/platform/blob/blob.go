// Package blob provides MinIO/S3 blob storage operations for the Reticora platform.
package blob

import (
	"context"
	"io"
)

// Store defines the interface for blob/object storage operations.
type Store interface {
	Put(ctx context.Context, bucket, key string, reader io.Reader, size int64, contentType string) error
	Get(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, bucket, key string) error
	PresignedGetURL(ctx context.Context, bucket, key string) (string, error)
}

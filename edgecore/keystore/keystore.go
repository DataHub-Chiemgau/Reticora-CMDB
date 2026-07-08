// Package keystore provides local credential persistence for enrolled edge devices.
package keystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrNotFound is returned when no stored credentials exist yet.
var ErrNotFound = errors.New("keystore: credentials not found")

// Credentials contains the locally persisted edge identity material.
type Credentials struct {
	DeviceID             string            `json:"deviceId"`
	ClientCertificatePEM string            `json:"clientCertificatePem"`
	ClientPrivateKeyPEM  string            `json:"clientPrivateKeyPem"`
	CertificateAuthority string            `json:"certificateAuthorityPem"`
	EnrollmentToken      string            `json:"enrollmentToken,omitempty"`
	Metadata             map[string]string `json:"metadata,omitempty"`
	UpdatedAt            time.Time         `json:"updatedAt"`
}

// Store abstracts credential persistence for different runtime environments.
type Store interface {
	Load(ctx context.Context) (*Credentials, error)
	Save(ctx context.Context, creds Credentials) error
	Clear(ctx context.Context) error
}

// FileStore stores credentials as a JSON document on disk.
type FileStore struct {
	Path string
	mu   sync.RWMutex
}

// Load reads credentials from disk.
func (s *FileStore) Load(ctx context.Context) (*Credentials, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("keystore: read %s: %w", s.Path, err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("keystore: decode %s: %w", s.Path, err)
	}

	return &creds, nil
}

// Save writes credentials to disk with restrictive file permissions.
func (s *FileStore) Save(ctx context.Context, creds Credentials) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("keystore: create directory: %w", err)
	}

	if creds.UpdatedAt.IsZero() {
		creds.UpdatedAt = time.Now().UTC()
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("keystore: encode credentials: %w", err)
	}

	if err := os.WriteFile(s.Path, data, 0o600); err != nil {
		return fmt.Errorf("keystore: write %s: %w", s.Path, err)
	}

	return nil
}

// Clear removes the stored credentials from disk.
func (s *FileStore) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("keystore: remove %s: %w", s.Path, err)
	}

	return nil
}

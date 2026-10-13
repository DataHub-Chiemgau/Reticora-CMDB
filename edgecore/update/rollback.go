package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Rollback on failed start (COL-06): Apply records the installed release
// as pending. Every start of the binary calls CheckPending, which counts
// the start; the new binary calls Confirm once it works (for the collector:
// the first successful heartbeat). When MaxStartAttempts starts pass
// without confirmation, CheckPending restores the previous binary and the
// service manager starts it again. The rolled-back version is remembered
// and not installed again.

// MaxStartAttempts is the number of unconfirmed starts after which an
// update is rolled back.
const MaxStartAttempts = 3

// ErrRolledBack reports that CheckPending restored the previous binary; the
// process must exit so the service manager starts the restored binary.
var ErrRolledBack = errors.New("update: the update did not start successfully and was rolled back")

const (
	pendingFile    = ".update-pending"
	rolledBackFile = ".update-rolledback"
)

type pendingUpdate struct {
	Version  string `json:"version"`
	Attempts int    `json:"attempts"`
}

func markerPath(execPath, name string) string { return filepath.Join(filepath.Dir(execPath), name) }

func writePending(execPath string, p *pendingUpdate) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(markerPath(execPath, pendingFile), raw, 0o644)
}

func readPending(execPath string) (*pendingUpdate, error) {
	raw, err := os.ReadFile(markerPath(execPath, pendingFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var p pendingUpdate
	if err = json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("update: pending marker: %w", err)
	}
	return &p, nil
}

// CheckPending counts a start of an unconfirmed update and rolls it back
// after maxAttempts starts (≤ 0 uses MaxStartAttempts). It returns the
// pending version ("" when none) and ErrRolledBack after a rollback.
func CheckPending(execPath string, maxAttempts int) (string, error) {
	if maxAttempts <= 0 {
		maxAttempts = MaxStartAttempts
	}
	p, err := readPending(execPath)
	if err != nil || p == nil {
		return "", err
	}
	p.Attempts++
	if p.Attempts <= maxAttempts {
		return p.Version, writePending(execPath, p)
	}
	if rerr := Rollback(execPath, p.Version); rerr != nil {
		return p.Version, rerr
	}
	return p.Version, ErrRolledBack
}

// Rollback restores the previous binary (<exec>.old), keeps the failed one
// as <exec>.failed and remembers version as rolled back.
func Rollback(execPath, version string) error {
	backup := execPath + ".old"
	if _, err := os.Stat(backup); err != nil {
		return fmt.Errorf("update: no previous binary to roll back to: %w", err)
	}
	failed := execPath + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(execPath, failed); err != nil {
		return fmt.Errorf("update: set the failed binary aside: %w", err)
	}
	if err := os.Rename(backup, execPath); err != nil {
		_ = os.Rename(failed, execPath)
		return fmt.Errorf("update: restore the previous binary: %w", err)
	}
	_ = os.Remove(markerPath(execPath, pendingFile))
	_ = os.Remove(markerPath(execPath, ".update-applied"))
	return os.WriteFile(markerPath(execPath, rolledBackFile), []byte(version), 0o644)
}

// Confirm marks the pending update as started successfully.
func Confirm(execPath string) error {
	err := os.Remove(markerPath(execPath, pendingFile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RolledBackVersion returns the version last rolled back ("" when none).
func RolledBackVersion(execPath string) string {
	raw, err := os.ReadFile(markerPath(execPath, rolledBackFile))
	if err != nil {
		return ""
	}
	return string(raw)
}

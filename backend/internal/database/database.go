package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	defaultMaxOpenConns = 25
	defaultMaxIdleConns = 25
	defaultConnMaxIdle  = 5 * time.Minute
	defaultConnMaxLife  = 30 * time.Minute
	defaultPingTimeout  = 5 * time.Second
)

// Connect opens a database handle, applies connection pool settings, and
// verifies connectivity. A PostgreSQL driver must be linked separately.
func Connect(databaseURL string) (*sql.DB, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, fmt.Errorf("database URL is required")
	}

	driverName, err := driverNameFromURL(databaseURL)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driverName, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	db.SetMaxOpenConns(defaultMaxOpenConns)
	db.SetMaxIdleConns(defaultMaxIdleConns)
	db.SetConnMaxIdleTime(defaultConnMaxIdle)
	db.SetConnMaxLifetime(defaultConnMaxLife)

	ctx, cancel := context.WithTimeout(context.Background(), defaultPingTimeout)
	defer cancel()

	if err := HealthCheck(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

// HealthCheck verifies that the configured database handle is reachable.
func HealthCheck(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("database handle is nil")
	}
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}

func driverNameFromURL(databaseURL string) (string, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse database URL: %w", err)
	}
	if parsed.Scheme == "" {
		return "", fmt.Errorf("database URL scheme is required")
	}

	scheme := strings.ToLower(parsed.Scheme)
	scheme = strings.TrimSuffix(scheme, "+ssl")
	scheme = strings.TrimSuffix(scheme, "+tcp")

	switch scheme {
	case "postgres", "postgresql":
		return "postgres", nil
	default:
		return scheme, nil
	}
}

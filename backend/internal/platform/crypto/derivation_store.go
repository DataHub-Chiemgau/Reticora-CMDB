package crypto

// The derivation root of the installation (SEC-06, WP-459, F-158): Derive
// takes the secrets of the platform (the issuer and the logins of the
// collector NATS endpoint, the pagination cursor key, the MAC of the
// collector CA announcements) from it instead of from the master key, so a
// master-key rotation leaves them unchanged and a running nats-edge keeps
// accepting the authorization callout. The root is drawn once, at the first
// start, and kept in the system table platform_secret (migration 000163),
// sealed with the master key; rotation.master_key reseals it with the
// current key, after which the previous key is no longer needed. The table
// has no organization: it is read with the system flag (database.WithSystem)
// and written only here (database.WithSystemWrite).

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// derivationRootName is the row of platform_secret that holds the sealed
// derivation root.
const derivationRootName = "derivation_root"

// LoadDerivationRoot opens the derivation root of the installation for enc.
// It reads the sealed root and unseals it with the current or a previous
// master key. At the first start (no root yet) it draws a root, seals it
// with the current master key and inserts it unless another replica was
// first (INSERT … ON CONFLICT DO NOTHING); it then reads the stored root
// again, so replicas starting together all open the one root. A root that
// does not unseal (another master key, tampered ciphertext) fails with
// ErrDerivationRoot and is left as it is: a new root would change every
// derived secret. created reports whether this call stored the root.
func LoadDerivationRoot(ctx context.Context, pool *pgxpool.Pool, enc *EnvelopeEncryptor) (created bool, err error) {
	sealed, err := readDerivationRoot(ctx, pool)
	if err != nil {
		return false, err
	}
	if sealed == nil {
		fresh, serr := enc.NewDerivationRoot()
		if serr != nil {
			return false, fmt.Errorf("draw the derivation root: %w", serr)
		}
		if err = database.WithSystemWrite(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
			tag, ierr := tx.Exec(ctx, `INSERT INTO platform_secret (name, sealed) VALUES ($1, $2) ON CONFLICT (name) DO NOTHING`,
				derivationRootName, fresh)
			created = tag.RowsAffected() == 1
			return ierr
		}); err != nil {
			return false, fmt.Errorf("store the derivation root: %w", err)
		}
		// The stored root, also when another replica stored it first.
		if sealed, err = readDerivationRoot(ctx, pool); err != nil {
			return created, err
		}
		if sealed == nil {
			return created, errors.New("crypto: the stored derivation root cannot be read back")
		}
	}
	return created, enc.OpenDerivationRoot(sealed)
}

// readDerivationRoot returns the sealed root, nil without one.
func readDerivationRoot(ctx context.Context, pool *pgxpool.Pool) ([]byte, error) {
	var sealed []byte
	err := database.WithSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT sealed FROM platform_secret WHERE name = $1`, derivationRootName).Scan(&sealed)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the derivation root: %w", err)
	}
	return sealed, nil
}

// RewrapDerivationRoot seals the derivation root with the current master
// key of enc, like the rewrap of the data keys in one transaction under the
// row lock, and reports whether it changed (rotation.master_key). Without a
// root, or with one sealed with the current key, nothing changes. A root
// that does not unseal fails with ErrDerivationRoot.
func RewrapDerivationRoot(ctx context.Context, pool *pgxpool.Pool, enc *EnvelopeEncryptor) (bool, error) {
	changed := false
	err := database.WithSystemWrite(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		var sealed []byte
		err := tx.QueryRow(ctx, `SELECT sealed FROM platform_secret WHERE name = $1 FOR UPDATE`, derivationRootName).Scan(&sealed)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock the derivation root: %w", err)
		}
		if enc.IsCurrent(sealed) {
			return nil
		}
		rewrapped, err := enc.Rewrap(sealed)
		if err != nil {
			return ErrDerivationRoot
		}
		if _, err = tx.Exec(ctx, `UPDATE platform_secret SET sealed = $2, rewrapped_at = now() WHERE name = $1`,
			derivationRootName, rewrapped); err != nil {
			return fmt.Errorf("store the rewrapped derivation root: %w", err)
		}
		changed = true
		return nil
	})
	return changed, err
}

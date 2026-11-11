package credential

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// The source of a credential (CRD-02, WP-894, E-220): stored keeps the
// envelope-encrypted secret, vault_ref a reference into a vault connection
// (WP-893) and no secret. A reference of a connection with resolve_at
// collector is resolved by the collector in memory (WP-895), one with
// server by the server (WP-896); the server never decrypts it here.

// Sources of a credential.
const (
	SourceStored   = "stored"
	SourceVaultRef = "vault_ref"
)

var (
	// ErrResolvedOnCollector: the reference is resolved by the collectors
	// of its connection, never on the server (E-220).
	ErrResolvedOnCollector = errors.New("credential: the vault reference is resolved on the collector")
	// ErrVaultNotConfigured: the server cannot resolve vault references
	// yet (WP-896).
	ErrVaultNotConfigured = errors.New("credential: no vault resolver is configured on the server")
	// ErrVaultRefHasNoSecret: a vault reference has no secret to replace
	// (PUT /credentials/{id}/secret, 409).
	ErrVaultRefHasNoSecret = errors.New("credential: a vault reference has no stored secret")
	// ErrVaultConnectionInUse: credentials reference the connection (409).
	ErrVaultConnectionInUse = errors.New("credentials reference the vault connection")
)

// VaultRef is the reference of a credential with source vault_ref: the
// connection, the path below its mount, an optional version (nil: the
// latest) and the map of the kind's fields to fields of the vault entry.
type VaultRef struct {
	ConnectionID string            `json:"connection_id"`
	Path         string            `json:"path"`
	Version      *int              `json:"version,omitempty"`
	FieldMap     map[string]string `json:"field_map"`
}

// KindFields are the required fields of a credential kind: the members a
// field_map must map (AK2 of WP-894) and the members the plugins of the
// collector read.
var KindFields = map[string][]string{
	KindSNMPv2c:     {"community"},
	KindSNMPv3:      {"username"},
	KindSSHPassword: {"username", "password"},
	KindSSHKey:      {"username", "private_key"},
	KindRedfish:     {"username", "password"},
	KindIPMI:        {"username", "password"},
	KindWMI:         {"username", "password"},
	KindAPIToken:    {"token"},
	KindNUT:         {"username", "password"},
	KindNAS:         {"username", "password"},
}

func refFieldError(field, message string) error {
	return &api.FieldError{Field: field, Status: http.StatusUnprocessableEntity, Message: message}
}

// normalizeVaultPath trims the path; it must name an entry below the mount.
func normalizeVaultPath(path string) (string, error) {
	p := strings.Trim(strings.TrimSpace(path), "/")
	if p == "" || slices.Contains(strings.Split(p, "/"), "..") {
		return "", refFieldError("vault_ref.path", "path must name an entry below the mount of the connection")
	}
	return p, nil
}

// checkFieldMap requires a vault field for every required field of kind.
func checkFieldMap(kind string, fieldMap map[string]string) error {
	required := KindFields[kind]
	var missing []string
	for _, field := range required {
		if strings.TrimSpace(fieldMap[field]) == "" {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		return refFieldError("field_map", fmt.Sprintf("field_map must map %s for kind %s", strings.Join(missing, ", "), kind))
	}
	for field, source := range fieldMap {
		if !slices.Contains(required, field) || strings.TrimSpace(source) == "" {
			return refFieldError("field_map", fmt.Sprintf("field_map maps only the fields %s of kind %s to vault fields",
				strings.Join(required, ", "), kind))
		}
	}
	return nil
}

// checkVaultRef validates the reference of a new credential of kind and
// client: a visible connection of the client or org-wide (AK2).
func (s *Service) checkVaultRef(ctx context.Context, orgID, kind, clientID string, ref *VaultRef) error {
	if _, login := vaultLoginFields[kind]; login {
		return refFieldError("vault_ref", "a credential of kind "+kind+" is only stored, never a vault reference")
	}
	path, err := normalizeVaultPath(ref.Path)
	if err != nil {
		return err
	}
	ref.Path = path
	if ref.Version != nil && *ref.Version < 1 {
		return refFieldError("vault_ref.version", "version must be 1 or more; omit it for the latest")
	}
	if mapErr := checkFieldMap(kind, ref.FieldMap); mapErr != nil {
		return mapErr
	}
	const message = "connection_id must name a vault connection of the credential's client or an org-wide one"
	if !api.IsUUID(ref.ConnectionID) {
		return refFieldError("vault_ref.connection_id", message)
	}
	conn, err := s.vaults.Get(ctx, orgID, ref.ConnectionID)
	if errors.Is(err, ErrVaultConnectionNotFound) {
		return refFieldError("vault_ref.connection_id", message)
	}
	if err != nil {
		return err
	}
	if conn.ClientID != "" && conn.ClientID != clientID {
		return refFieldError("vault_ref.connection_id", message)
	}
	return nil
}

// checkSource requires exactly one of secret and vault_ref (AK2).
func checkSource(req *CreateRequest) error {
	switch {
	case req.Secret != nil && req.VaultRef != nil:
		return refFieldError("vault_ref", "send either secret or vault_ref, not both")
	case req.Secret == nil && req.VaultRef == nil:
		return refFieldError("secret", "secret or vault_ref is required")
	}
	return nil
}

// decryptVaultRef answers Decrypt for a reference: the server holds no
// plaintext of it (AK4).
func (s *Service) decryptVaultRef(ctx context.Context, orgID string, stored *StoredCredential) error {
	conn, err := s.vaults.Get(ctx, orgID, stored.VaultRef.ConnectionID)
	if err != nil {
		return fmt.Errorf("credential %s: %w", stored.ID, err)
	}
	if conn.ResolveAt == ResolveAtCollector {
		return ErrResolvedOnCollector
	}
	return ErrVaultNotConfigured
}

// VaultRefUpdate is a PATCH of the reference (AK3): path, version
// (SetVersion with nil: the latest) and field map.
type VaultRefUpdate struct {
	Path       *string
	Version    *int
	SetVersion bool
	FieldMap   map[string]string
}

// Changes reports whether the update changes the reference.
func (u *VaultRefUpdate) Changes() bool { return u.Path != nil || u.SetVersion || u.FieldMap != nil }

// checkVaultRefUpdate validates a PATCH of the reference against the stored
// credential.
func checkVaultRefUpdate(current *StoredCredential, u *VaultRefUpdate) error {
	if current.Source != SourceVaultRef {
		return refFieldError("vault_path", "only a credential with source vault_ref has a reference to change")
	}
	if u.Path != nil {
		path, err := normalizeVaultPath(*u.Path)
		if err != nil {
			return refFieldError("vault_path", "vault_path must name an entry below the mount of the connection")
		}
		u.Path = &path
	}
	if u.SetVersion && u.Version != nil && *u.Version < 1 {
		return refFieldError("vault_version", "vault_version must be 1 or more, or null for the latest")
	}
	if u.FieldMap != nil {
		return checkFieldMap(current.Kind, u.FieldMap)
	}
	return nil
}

// --- bundle (AK5) -------------------------------------------------------

// BundleVaultRef is the reference of a vault_ref credential in a bundle:
// what the collector needs to read the entry, never a value of it. The
// login of the connection is its own entry of the bundle (LoginID).
type BundleVaultRef struct {
	ConnectionID string            `json:"connection_id"`
	Kind         string            `json:"kind"`
	Address      string            `json:"address"`
	Namespace    string            `json:"namespace,omitempty"`
	Mount        string            `json:"mount"`
	Path         string            `json:"path"`
	Version      *int              `json:"version,omitempty"`
	FieldMap     map[string]string `json:"field_map"`
	CAPEM        string            `json:"ca_pem,omitempty"`
	AuthMethod   string            `json:"auth_method"`
	LoginID      string            `json:"login_id"`
}

// bundleEntries returns the bundle entries of credential c for target t:
// the stored secret, or the reference and the stored login of its
// connection. A reference of a connection the target does not resolve
// (resolve_at server, disabled, or the collector not listed) yields none.
func (s *Service) bundleEntries(ctx context.Context, t *BundleTarget, c *Credential) ([]BundleCredential, error) {
	if c.Source != SourceVaultRef || c.VaultRef == nil {
		secret, err := s.Decrypt(ctx, t.OrganizationID, c.ID)
		if err != nil {
			return nil, fmt.Errorf("credential %s: %w", c.ID, err)
		}
		return []BundleCredential{{ID: c.ID, Name: c.Name, Kind: c.Kind, Source: SourceStored, Secret: secret}}, nil
	}
	conn, err := s.vaults.Get(ctx, t.OrganizationID, c.VaultRef.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("credential %s: %w", c.ID, err)
	}
	if conn.ResolveAt != ResolveAtCollector || !conn.Enabled || !slices.Contains(conn.CollectorIDs, t.CollectorID) {
		return nil, nil
	}
	login, err := s.Get(ctx, t.OrganizationID, conn.AuthCredentialID)
	if err != nil {
		return nil, fmt.Errorf("vault connection %s: %w", conn.ID, err)
	}
	loginSecret, err := s.Decrypt(ctx, t.OrganizationID, login.ID)
	if err != nil {
		return nil, fmt.Errorf("vault connection %s: %w", conn.ID, err)
	}
	ref := &BundleVaultRef{ConnectionID: conn.ID, Kind: conn.Kind, Address: conn.Address, Namespace: conn.Namespace,
		Mount: conn.Mount, Path: c.VaultRef.Path, Version: c.VaultRef.Version, FieldMap: c.VaultRef.FieldMap,
		CAPEM: conn.CAPEM, AuthMethod: conn.AuthMethod, LoginID: login.ID}
	return []BundleCredential{
		{ID: login.ID, Name: login.Name, Kind: login.Kind, Source: SourceStored, Secret: loginSecret},
		{ID: c.ID, Name: c.Name, Kind: c.Kind, Source: SourceVaultRef, VaultRef: ref},
	}, nil
}

// --- vault connection deletion (AK3 of WP-893) ----------------------------

// connectionReferenced reports whether a credential of the organization
// references the connection; the foreign key of the database refuses as
// well, also for credentials outside the caller's scope.
func (s *Service) connectionReferenced(ctx context.Context, orgID, connectionID string) (bool, error) {
	list, err := s.repo.List(ctx, orgID)
	if err != nil {
		return false, err
	}
	for i := range list {
		if list[i].VaultRef != nil && list[i].VaultRef.ConnectionID == connectionID {
			return true, nil
		}
	}
	return false, nil
}

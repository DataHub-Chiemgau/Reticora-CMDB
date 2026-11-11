package credential

import (
	"cmp"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Vault connections (CRD-03, part of COL-03; WP-893, E-220, E-221): an
// OpenBao or HashiCorp Vault KV v2 engine the references of WP-894 read
// from. A connection holds no secret: it logs in with a stored credential
// of kind vault_approle or vault_token (auth_credential_id). The address of
// a connection with resolve_at server passes the egress policy, and POST
// /vault-connections/{id}/test runs the job vault.test (WP-896).

// Kinds of vault connection (E-221).
const (
	VaultKindOpenBao   = "openbao_kv2"
	VaultKindHashiCorp = "hashicorp_kv2"
)

// Login methods of a vault connection.
const (
	VaultAuthAppRole = "approle"
	VaultAuthToken   = "token"
)

// Places a reference of a connection is resolved at (E-220).
const (
	ResolveAtCollector = "collector"
	ResolveAtServer    = "server"
)

var (
	vaultKinds      = []string{VaultKindOpenBao, VaultKindHashiCorp}
	vaultAuthKinds  = map[string]string{VaultAuthAppRole: KindVaultAppRole, VaultAuthToken: KindVaultToken}
	vaultResolvesAt = []string{ResolveAtCollector, ResolveAtServer}
)

// maxVaultCollectors bounds collector_ids.
const maxVaultCollectors = 100

// VaultConnection is a connection to a KV v2 vault. ca_pem is a public
// certificate; the login is the credential auth_credential_id names.
type VaultConnection struct {
	ID               string    `json:"id"`
	OrganizationID   string    `json:"organization_id"`
	ClientID         string    `json:"client_id,omitempty"`
	Name             string    `json:"name"`
	Kind             string    `json:"kind"`
	Address          string    `json:"address"`
	Namespace        string    `json:"namespace,omitempty"`
	Mount            string    `json:"mount"`
	AuthMethod       string    `json:"auth_method"`
	AuthCredentialID string    `json:"auth_credential_id"`
	CAPEM            string    `json:"ca_pem,omitempty"`
	ResolveAt        string    `json:"resolve_at"`
	Enabled          bool      `json:"enabled"`
	CollectorIDs     []string  `json:"collector_ids"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Errors of the vault connection store.
var (
	ErrVaultConnectionNotFound = errors.New("vault connection not found")
	// ErrCredentialIsVaultLogin: a vault connection logs in with the
	// credential, which therefore is not deleted (409).
	ErrCredentialIsVaultLogin = errors.New("a vault connection logs in with the credential")
)

// VaultRepository stores vault connections; PGRepository and the memory
// store of this file implement it.
type VaultRepository interface {
	CreateVaultConnection(ctx context.Context, c *VaultConnection) error
	ListVaultConnections(ctx context.Context, orgID string) ([]VaultConnection, error)
	GetVaultConnection(ctx context.Context, orgID, id string) (*VaultConnection, error)
	// UpdateVaultConnection stores every member of c and replaces its
	// collectors.
	UpdateVaultConnection(ctx context.Context, c *VaultConnection) error
	DeleteVaultConnection(ctx context.Context, orgID, id string) error
	// CollectorClient returns the client of a collector visible in the
	// caller's scope ("" for an org-wide one) and whether it is visible.
	CollectorClient(ctx context.Context, orgID, collectorID string) (clientID string, visible bool, err error)
}

// VaultService validates and stores vault connections.
type VaultService struct {
	creds  *Service
	vaults VaultRepository
}

// Vaults returns the vault connection service (WP-893). A service without
// a repository of connections uses a memory store (--no-db).
func (s *Service) Vaults() *VaultService { return s.vaults }

// loginOfConnection reports whether a vault connection visible to the
// caller logs in with the credential; the foreign key
// vault_connection_auth_credential_fkey refuses as well, also for
// connections outside the caller's scope (credentialDeleteError).
func (v *VaultService) loginOfConnection(ctx context.Context, orgID, credentialID string) (bool, error) {
	list, err := v.vaults.ListVaultConnections(ctx, orgID)
	if err != nil {
		return false, err
	}
	for i := range list {
		if list[i].AuthCredentialID == credentialID {
			return true, nil
		}
	}
	return false, nil
}

// credentialDeleteError maps the refusal of the database to delete the
// login of a vault connection onto ErrCredentialIsVaultLogin.
func credentialDeleteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "vault_connection_auth_credential_fkey" {
		return ErrCredentialIsVaultLogin
	}
	return err
}

func vaultFieldError(field, message string) error {
	return &api.FieldError{Field: field, Status: http.StatusUnprocessableEntity, Message: message}
}

// checkVaultAddress checks the address syntactically (AK4): https, a host,
// no user information, query or fragment.
func checkVaultAddress(address string) error {
	u, err := url.Parse(address)
	switch {
	case err != nil || u.Scheme != "https" || u.Hostname() == "":
		return vaultFieldError("address", "address must be an https URL with a host")
	case u.User != nil:
		return vaultFieldError("address", "address must not carry user information")
	case u.RawQuery != "" || u.Fragment != "" || u.Opaque != "":
		return vaultFieldError("address", "address must not carry a query or fragment")
	}
	return nil
}

// checkCAPEM requires at least one PEM certificate and nothing else.
func checkCAPEM(raw string) error {
	rest := []byte(raw)
	found := false
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return vaultFieldError("ca_pem", "ca_pem must hold PEM certificates only")
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return vaultFieldError("ca_pem", "ca_pem holds a certificate that does not parse")
		}
		found = true
	}
	if !found || strings.TrimSpace(string(rest)) != "" {
		return vaultFieldError("ca_pem", "ca_pem must be a PEM certificate")
	}
	return nil
}

// validate checks a connection completely (create and the merged PATCH).
func (v *VaultService) validate(ctx context.Context, orgID string, c *VaultConnection) error {
	c.Name = strings.TrimSpace(c.Name)
	c.Mount = strings.Trim(strings.TrimSpace(c.Mount), "/")
	c.Namespace = strings.Trim(strings.TrimSpace(c.Namespace), "/")
	if c.ResolveAt == "" {
		c.ResolveAt = ResolveAtCollector
	}
	switch {
	case c.Name == "":
		return vaultFieldError("name", "name is required")
	case c.ClientID != "" && !api.IsUUID(c.ClientID):
		return vaultFieldError("client_id", "client_id must be a UUID")
	case !slices.Contains(vaultKinds, c.Kind):
		return vaultFieldError("kind", "kind must be openbao_kv2 or hashicorp_kv2")
	case c.Mount == "" || strings.Contains(c.Mount, ".."):
		return vaultFieldError("mount", "mount must name the KV v2 engine")
	case strings.Contains(c.Namespace, ".."):
		return vaultFieldError("namespace", "namespace must not contain ..")
	case vaultAuthKinds[c.AuthMethod] == "":
		return vaultFieldError("auth_method", "auth_method must be approle or token")
	case !slices.Contains(vaultResolvesAt, c.ResolveAt):
		return vaultFieldError("resolve_at", "resolve_at must be collector or server")
	}
	if err := checkVaultAddress(c.Address); err != nil {
		return err
	}
	// The server dials a connection with resolve_at server itself: its
	// address must pass the egress policy (SEC-08, AK3 of WP-896); without
	// a resolver the strict policy applies.
	if c.ResolveAt == ResolveAtServer {
		var policy egress.Options
		if v.creds.vault != nil {
			policy = v.creds.vault.egress
		}
		if err := policy.ValidateURL(c.Address); err != nil {
			return vaultFieldError("address", "address is not an allowed destination of the server (egress policy)")
		}
	}
	if c.CAPEM != "" {
		if err := checkCAPEM(c.CAPEM); err != nil {
			return err
		}
	}
	if err := v.checkAuthCredential(ctx, orgID, c); err != nil {
		return err
	}
	return v.checkCollectors(ctx, orgID, c)
}

// checkAuthCredential requires a visible credential of the kind of
// auth_method, of the connection's client or org-wide (AK2).
func (v *VaultService) checkAuthCredential(ctx context.Context, orgID string, c *VaultConnection) error {
	const message = "auth_credential_id must name a credential of kind %s of the connection's client or org-wide"
	want := vaultAuthKinds[c.AuthMethod]
	if !api.IsUUID(c.AuthCredentialID) {
		return vaultFieldError("auth_credential_id", fmt.Sprintf(message, want))
	}
	cred, err := v.creds.Get(ctx, orgID, c.AuthCredentialID)
	if errors.Is(err, ErrNotFound) {
		return vaultFieldError("auth_credential_id", fmt.Sprintf(message, want))
	}
	if err != nil {
		return err
	}
	if cred.Kind != want || (cred.ClientID != "" && cred.ClientID != c.ClientID) {
		return vaultFieldError("auth_credential_id", fmt.Sprintf(message, want))
	}
	return nil
}

// checkCollectors requires at least one collector for resolve_at collector
// (of the connection's client or org-wide) and none for server (AK3).
func (v *VaultService) checkCollectors(ctx context.Context, orgID string, c *VaultConnection) error {
	slices.Sort(c.CollectorIDs)
	c.CollectorIDs = slices.Compact(c.CollectorIDs)
	if c.CollectorIDs == nil {
		c.CollectorIDs = []string{}
	}
	if c.ResolveAt == ResolveAtServer {
		if len(c.CollectorIDs) > 0 {
			return vaultFieldError("collector_ids", "collector_ids must be empty for resolve_at server")
		}
		return nil
	}
	if len(c.CollectorIDs) == 0 {
		return vaultFieldError("collector_ids", "resolve_at collector needs at least one collector")
	}
	if len(c.CollectorIDs) > maxVaultCollectors {
		return vaultFieldError("collector_ids", fmt.Sprintf("at most %d collectors", maxVaultCollectors))
	}
	for _, id := range c.CollectorIDs {
		client, visible := "", false
		if api.IsUUID(id) {
			var err error
			if client, visible, err = v.vaults.CollectorClient(ctx, orgID, id); err != nil {
				return err
			}
		}
		if !visible || (c.ClientID != "" && client != "" && client != c.ClientID) {
			return vaultFieldError("collector_ids", "collector "+id+" not found or not of the connection's client")
		}
	}
	return nil
}

// Create stores a connection.
func (v *VaultService) Create(ctx context.Context, orgID string, c *VaultConnection) (*VaultConnection, error) {
	c.OrganizationID = orgID
	if err := v.validate(ctx, orgID, c); err != nil {
		return nil, err
	}
	if err := v.vaults.CreateVaultConnection(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// List returns the connections of the caller's scope.
func (v *VaultService) List(ctx context.Context, orgID string) ([]VaultConnection, error) {
	return v.vaults.ListVaultConnections(ctx, orgID)
}

// Get returns one connection.
func (v *VaultService) Get(ctx context.Context, orgID, id string) (*VaultConnection, error) {
	return v.vaults.GetVaultConnection(ctx, orgID, id)
}

// Update applies a PATCH: change merges the sent members into the stored
// connection, which is then validated as a whole.
func (v *VaultService) Update(ctx context.Context, orgID, id string, change func(c *VaultConnection) error) (*VaultConnection, error) {
	current, err := v.vaults.GetVaultConnection(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	next := *current
	next.CollectorIDs = slices.Clone(current.CollectorIDs)
	if changeErr := change(&next); changeErr != nil {
		return nil, changeErr
	}
	if validErr := v.validate(ctx, orgID, &next); validErr != nil {
		return nil, validErr
	}
	if updateErr := v.vaults.UpdateVaultConnection(ctx, &next); updateErr != nil {
		return nil, updateErr
	}
	// Values read and the client logged in with the old address, mount or
	// login are not used again (WP-896).
	v.creds.forgetVault(orgID, id)
	return &next, nil
}

// Delete removes a connection; one referenced by credentials is refused
// (ErrVaultConnectionInUse, 409; WP-894).
func (v *VaultService) Delete(ctx context.Context, orgID, id string) error {
	referenced, err := v.creds.connectionReferenced(ctx, orgID, id)
	if err != nil {
		return err
	}
	if referenced {
		return ErrVaultConnectionInUse
	}
	if derr := v.vaults.DeleteVaultConnection(ctx, orgID, id); derr != nil {
		return derr
	}
	v.creds.forgetVault(orgID, id)
	return nil
}

// --- PostgreSQL -----------------------------------------------------------

const vaultConnectionColumns = `v.id::text, v.organization_id::text, COALESCE(v.client_id::text, ''), v.name, v.kind,
	v.address, COALESCE(v.namespace, ''), v.mount, v.auth_method, v.auth_credential_id::text, COALESCE(v.ca_pem, ''),
	v.resolve_at, v.enabled,
	COALESCE((SELECT array_agg(vc.collector_id::text ORDER BY vc.collector_id::text) FROM vault_connection_collector vc
	          WHERE vc.vault_connection_id = v.id), '{}'),
	v.created_at, v.updated_at`

func scanVaultConnection(row pgx.Row) (*VaultConnection, error) {
	var c VaultConnection
	err := row.Scan(&c.ID, &c.OrganizationID, &c.ClientID, &c.Name, &c.Kind, &c.Address, &c.Namespace, &c.Mount,
		&c.AuthMethod, &c.AuthCredentialID, &c.CAPEM, &c.ResolveAt, &c.Enabled, &c.CollectorIDs, &c.CreatedAt, &c.UpdatedAt)
	return &c, err
}

// vaultConnectionError maps the constraints of vault_connection.
func vaultConnectionError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "vault_connection_name_key":
		return &api.ConflictError{Field: "name", Message: "a vault connection of this name exists"}
	case "vault_connection_auth_credential_fkey", "vault_connection_auth_credential_rule":
		return vaultFieldError("auth_credential_id", "auth_credential_id must name a credential of the auth method of the connection's client or org-wide")
	case "vault_connection_collector_collector_fkey", "vault_connection_collector_client_rule":
		return vaultFieldError("collector_ids", "a collector is not found or not of the connection's client")
	case "vault_connection_client_fkey":
		return vaultFieldError("client_id", "client not found")
	case "credential_vault_connection_fkey":
		return ErrVaultConnectionInUse
	}
	return err
}

func writeVaultCollectors(ctx context.Context, tx pgx.Tx, c *VaultConnection) error {
	if _, err := tx.Exec(ctx, `DELETE FROM vault_connection_collector WHERE vault_connection_id = $1`, c.ID); err != nil {
		return fmt.Errorf("replace vault connection collectors: %w", err)
	}
	if len(c.CollectorIDs) == 0 {
		return nil
	}
	// organization_id and client_id come from the connection (trigger).
	if _, err := tx.Exec(ctx, `INSERT INTO vault_connection_collector (organization_id, vault_connection_id, collector_id)
		SELECT current_setting('app.org_id')::uuid, $1, unnest($2::uuid[])`, c.ID, c.CollectorIDs); err != nil {
		return vaultConnectionError(err)
	}
	return nil
}

func readVaultConnection(ctx context.Context, tx pgx.Tx, id string) (*VaultConnection, error) {
	c, err := scanVaultConnection(tx.QueryRow(ctx, `SELECT `+vaultConnectionColumns+` FROM vault_connection v WHERE v.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrVaultConnectionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read vault connection: %w", err)
	}
	return c, nil
}

// CreateVaultConnection implements VaultRepository.
func (r *PGRepository) CreateVaultConnection(ctx context.Context, c *VaultConnection) error {
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO vault_connection (organization_id, client_id, name, kind, address, namespace,
			mount, auth_method, auth_credential_id, ca_pem, resolve_at, enabled)
			VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id::text`,
			nilIfEmpty(c.ClientID), c.Name, c.Kind, c.Address, nilIfEmpty(c.Namespace), c.Mount, c.AuthMethod,
			c.AuthCredentialID, nilIfEmpty(c.CAPEM), c.ResolveAt, c.Enabled).Scan(&c.ID); err != nil {
			return vaultConnectionError(err)
		}
		if err := writeVaultCollectors(ctx, tx, c); err != nil {
			return err
		}
		stored, err := readVaultConnection(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		*c = *stored
		return nil
	})
}

// ListVaultConnections implements VaultRepository.
func (r *PGRepository) ListVaultConnections(ctx context.Context, orgID string) ([]VaultConnection, error) {
	out := []VaultConnection{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+vaultConnectionColumns+` FROM vault_connection v ORDER BY v.name, v.id`)
		if err != nil {
			return fmt.Errorf("list vault connections: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c, scanErr := scanVaultConnection(rows)
			if scanErr != nil {
				return fmt.Errorf("scan vault connection: %w", scanErr)
			}
			out = append(out, *c)
		}
		return rows.Err()
	})
	return out, err
}

// GetVaultConnection implements VaultRepository.
func (r *PGRepository) GetVaultConnection(ctx context.Context, orgID, id string) (*VaultConnection, error) {
	if !api.IsUUID(id) {
		return nil, ErrVaultConnectionNotFound
	}
	var out *VaultConnection
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = readVaultConnection(ctx, tx, id)
		return err
	})
	return out, err
}

// UpdateVaultConnection implements VaultRepository.
func (r *PGRepository) UpdateVaultConnection(ctx context.Context, c *VaultConnection) error {
	if !api.IsUUID(c.ID) {
		return ErrVaultConnectionNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE vault_connection SET name = $2, kind = $3, address = $4, namespace = $5, mount = $6,
			auth_method = $7, auth_credential_id = $8, ca_pem = $9, resolve_at = $10, enabled = $11 WHERE id = $1`,
			c.ID, c.Name, c.Kind, c.Address, nilIfEmpty(c.Namespace), c.Mount, c.AuthMethod, c.AuthCredentialID,
			nilIfEmpty(c.CAPEM), c.ResolveAt, c.Enabled)
		if err != nil {
			return vaultConnectionError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrVaultConnectionNotFound
		}
		if writeErr := writeVaultCollectors(ctx, tx, c); writeErr != nil {
			return writeErr
		}
		stored, err := readVaultConnection(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		*c = *stored
		return nil
	})
}

// DeleteVaultConnection implements VaultRepository.
func (r *PGRepository) DeleteVaultConnection(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrVaultConnectionNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM vault_connection WHERE id = $1`, id)
		if err != nil {
			return vaultConnectionError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrVaultConnectionNotFound
		}
		return nil
	})
}

// CollectorClient implements VaultRepository.
func (r *PGRepository) CollectorClient(ctx context.Context, orgID, collectorID string) (clientID string, visible bool, err error) {
	var client *string
	found := false
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		scanErr := tx.QueryRow(ctx, `SELECT client_id::text FROM collector WHERE id = $1`, collectorID).Scan(&client)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil
		}
		found = scanErr == nil
		return scanErr
	})
	if err != nil || !found {
		return "", false, err
	}
	if client == nil {
		return "", true, nil
	}
	return *client, true, nil
}

var _ VaultRepository = (*PGRepository)(nil)

// --- memory (--no-db) -----------------------------------------------------

// memoryVaults keeps connections in memory; it knows no collectors, so
// every collector counts as present and org-wide.
type memoryVaults struct {
	mu   sync.Mutex
	rows map[string]*VaultConnection
}

func newMemoryVaults() *memoryVaults { return &memoryVaults{rows: map[string]*VaultConnection{}} }

func (m *memoryVaults) nameTaken(c *VaultConnection) bool {
	for _, other := range m.rows {
		if other.ID != c.ID && other.OrganizationID == c.OrganizationID && other.Name == c.Name {
			return true
		}
	}
	return false
}

func (m *memoryVaults) CreateVaultConnection(_ context.Context, c *VaultConnection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nameTaken(c) {
		return &api.ConflictError{Field: "name", Message: "a vault connection of this name exists"}
	}
	id, err := generateID()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	c.ID, c.CreatedAt, c.UpdatedAt = id, now, now
	stored := *c
	stored.CollectorIDs = slices.Clone(c.CollectorIDs)
	m.rows[id] = &stored
	return nil
}

func (m *memoryVaults) ListVaultConnections(_ context.Context, orgID string) ([]VaultConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []VaultConnection{}
	for _, c := range m.rows {
		if c.OrganizationID == orgID {
			row := *c
			row.CollectorIDs = slices.Clone(c.CollectorIDs)
			out = append(out, row)
		}
	}
	slices.SortFunc(out, func(a, b VaultConnection) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
	return out, nil
}

func (m *memoryVaults) GetVaultConnection(_ context.Context, orgID, id string) (*VaultConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.rows[id]
	if !ok || c.OrganizationID != orgID {
		return nil, ErrVaultConnectionNotFound
	}
	out := *c
	out.CollectorIDs = slices.Clone(c.CollectorIDs)
	return &out, nil
}

func (m *memoryVaults) UpdateVaultConnection(_ context.Context, c *VaultConnection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.rows[c.ID]
	if !ok || current.OrganizationID != c.OrganizationID {
		return ErrVaultConnectionNotFound
	}
	if m.nameTaken(c) {
		return &api.ConflictError{Field: "name", Message: "a vault connection of this name exists"}
	}
	c.UpdatedAt = time.Now().UTC()
	stored := *c
	stored.CollectorIDs = slices.Clone(c.CollectorIDs)
	m.rows[c.ID] = &stored
	return nil
}

func (m *memoryVaults) DeleteVaultConnection(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.rows[id]
	if !ok || c.OrganizationID != orgID {
		return ErrVaultConnectionNotFound
	}
	delete(m.rows, id)
	return nil
}

func (m *memoryVaults) CollectorClient(context.Context, string, string) (clientID string, visible bool, err error) {
	return "", true, nil
}

// --- HTTP -------------------------------------------------------------------

// vaultConnectionRequest is the body of POST /vault-connections.
type vaultConnectionRequest struct {
	ClientID         string   `json:"client_id"`
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	Address          string   `json:"address"`
	Namespace        string   `json:"namespace"`
	Mount            string   `json:"mount"`
	AuthMethod       string   `json:"auth_method"`
	AuthCredentialID string   `json:"auth_credential_id"`
	CAPEM            string   `json:"ca_pem"`
	ResolveAt        string   `json:"resolve_at"`
	Enabled          *bool    `json:"enabled"`
	CollectorIDs     []string `json:"collector_ids"`
}

// registerVaultRoutes mounts the vault connection routes (CRD-03, WP-893).
func (h *Handler) registerVaultRoutes(r chi.Router) {
	r.Route("/api/v1/vault-connections", func(r chi.Router) {
		r.Post("/", h.createVaultConnection)
		r.Get("/", h.listVaultConnections)
		r.Get("/{id}", h.getVaultConnection)
		r.Patch("/{id}", h.updateVaultConnection)
		r.Delete("/{id}", h.deleteVaultConnection)
		r.Post("/{id}/test", h.testVaultConnection)
	})
}

// VaultTestJobs runs a connection test as a job in this process;
// jobs.Service implements it.
type VaultTestJobs interface {
	RunDetached(ctx context.Context, orgID, kind string, params any, createdBy string,
		run func(ctx context.Context) (any, error)) (*jobs.Job, error)
}

// VaultTestQueue submits a connection test to the job queue on NATS,
// whose workers run it with RunVaultTestJob; jobs.Queue implements it.
type VaultTestQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// WithJobs runs the connection tests as jobs of svc in this process.
func (h *Handler) WithJobs(svc VaultTestJobs) *Handler {
	h.vaultJobs = svc
	return h
}

// WithQueue runs the connection tests on the job queue.
func (h *Handler) WithQueue(q VaultTestQueue) *Handler {
	h.vaultQueue = q
	return h
}

// errVaultTestFailed is the error of a test that could not run; the cause
// goes to the log (API-03).
var errVaultTestFailed = errors.New("the vault connection test could not run")

// runVaultTest runs a test for a job; the result carries ok or the error
// class only.
func (h *Handler) runVaultTest(ctx context.Context, orgID string, p VaultTestParams) (any, error) {
	res, err := h.svc.TestVaultConnection(ctx, orgID, p)
	if err != nil {
		slog.ErrorContext(ctx, "vault connection test", "connection_id", p.ConnectionID, "error", err)
		return nil, errVaultTestFailed
	}
	return res, nil
}

// RunVaultTestJob runs a job vault.test of the job queue in the tenant and
// scope of its creator.
func (h *Handler) RunVaultTestJob(ctx context.Context, job *jobs.Job) (any, error) {
	var p VaultTestParams
	if err := json.Unmarshal(job.Params, &p); err != nil {
		return nil, errVaultTestFailed
	}
	return h.runVaultTest(jobs.CreatorContext(ctx, job), job.OrganizationID, p)
}

// vaultTestResponse is the answer of POST /vault-connections/{id}/test.
type vaultTestResponse struct {
	JobID  string      `json:"job_id"`
	Status jobs.Status `json:"status"`
	Kind   string      `json:"kind"`
}

// testVaultConnection handles POST /vault-connections/{id}/test (AK4 of
// WP-896): the job vault.test, answered with 202 and the job.
func (h *Handler) testVaultConnection(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	// The body is optional: an empty one (also chunked, without
	// Content-Length) tests the default path.
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Path != "" {
		path, err := normalizeVaultPath(body.Path)
		if err != nil {
			api.WriteFieldError(w, vaultFieldError("path", "path must name an entry below the mount of the connection"))
			return
		}
		body.Path = path
	}
	conn, err := h.svc.Vaults().Get(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeVaultError(w, err)
		return
	}
	if h.svc.vault == nil || (h.vaultJobs == nil && h.vaultQueue == nil) {
		writeError(w, http.StatusServiceUnavailable, "vault connection tests are not available")
		return
	}
	params := VaultTestParams{ConnectionID: conn.ID, Path: body.Path}
	userID := tenant.FromContext(r.Context()).UserID
	var job *jobs.Job
	if h.vaultQueue != nil {
		job, _, err = h.vaultQueue.Submit(r.Context(), orgID, VaultTestJobKind, params, jobs.EnqueueOptions{CreatedBy: userID})
	} else {
		job, err = h.vaultJobs.RunDetached(r.Context(), orgID, VaultTestJobKind, params, userID,
			func(ctx context.Context) (any, error) { return h.runVaultTest(ctx, orgID, params) })
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "vault connection test: submit the job", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	api.WriteJSON(w, http.StatusAccepted, vaultTestResponse{JobID: job.ID, Status: job.Status, Kind: job.Kind})
}

// writeVaultError maps the errors of the vault connection service.
func writeVaultError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrVaultConnectionNotFound):
		writeError(w, http.StatusNotFound, "vault connection not found")
	case errors.Is(err, ErrVaultConnectionInUse):
		api.WriteFieldError(w, &api.ConflictError{Field: "id", Message: "credentials reference the vault connection; change or delete them first"})
	default:
		// Field and conflict errors carry their field (422, 409); a
		// client outside the scope 403; anything else 500 (API-03).
		api.WriteRepoError(w, err)
	}
}

func (h *Handler) createVaultConnection(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	var req vaultConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	c := VaultConnection{ClientID: req.ClientID, Name: req.Name, Kind: req.Kind, Address: req.Address, Namespace: req.Namespace,
		Mount: req.Mount, AuthMethod: req.AuthMethod, AuthCredentialID: req.AuthCredentialID, CAPEM: req.CAPEM,
		ResolveAt: req.ResolveAt, Enabled: req.Enabled == nil || *req.Enabled, CollectorIDs: req.CollectorIDs}
	created, err := h.svc.Vaults().Create(r.Context(), orgID, &c)
	if err != nil {
		writeVaultError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, created)
}

func (h *Handler) listVaultConnections(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	list, err := h.svc.Vaults().List(r.Context(), orgID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.Page(list, page))
}

func (h *Handler) getVaultConnection(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	c, err := h.svc.Vaults().Get(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeVaultError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, c)
}

// vaultPatch decodes one member of a PATCH into the connection.
func vaultPatch(c *VaultConnection, field string, raw json.RawMessage) error {
	text := func(dst *string, nullable bool) error {
		if nullable && string(raw) == "null" {
			*dst = ""
			return nil
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			return vaultFieldError(field, field+" must be a string")
		}
		return nil
	}
	switch field {
	case "name":
		return text(&c.Name, false)
	case "kind":
		return text(&c.Kind, false)
	case "address":
		return text(&c.Address, false)
	case "namespace":
		return text(&c.Namespace, true)
	case "mount":
		return text(&c.Mount, false)
	case "auth_method":
		return text(&c.AuthMethod, false)
	case "auth_credential_id":
		return text(&c.AuthCredentialID, false)
	case "ca_pem":
		return text(&c.CAPEM, true)
	case "resolve_at":
		return text(&c.ResolveAt, false)
	case "enabled":
		if err := json.Unmarshal(raw, &c.Enabled); err != nil {
			return vaultFieldError(field, "enabled must be a boolean")
		}
		return nil
	case "collector_ids":
		c.CollectorIDs = nil
		if err := json.Unmarshal(raw, &c.CollectorIDs); err != nil {
			return vaultFieldError(field, "collector_ids must be a list of collector IDs")
		}
		return nil
	case "client_id":
		return vaultFieldError(field, "client_id cannot be changed; create a connection for the other client")
	}
	return vaultFieldError(field, "unknown field")
}

func (h *Handler) updateVaultConnection(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	c, err := h.svc.Vaults().Update(r.Context(), orgID, chi.URLParam(r, "id"), func(c *VaultConnection) error {
		for field, raw := range body {
			if patchErr := vaultPatch(c, field, raw); patchErr != nil {
				return patchErr
			}
		}
		// A connection resolved by the server has no collectors; without
		// collector_ids the switch drops them.
		if _, sent := body["collector_ids"]; !sent && c.ResolveAt == ResolveAtServer {
			c.CollectorIDs = nil
		}
		return nil
	})
	if err != nil {
		writeVaultError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, c)
}

func (h *Handler) deleteVaultConnection(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	if err := h.svc.Vaults().Delete(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeVaultError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

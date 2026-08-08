package iga

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

const maxSCIMResponseBytes int64 = 1 << 20

type Account struct {
	ID          string  `json:"id"`
	UserName    string  `json:"userName"`
	DisplayName string  `json:"displayName,omitempty"`
	Active      bool    `json:"active"`
	Raw         JSONMap `json:"raw,omitempty"`
}

type Connector interface {
	Capabilities() ConnectorCapabilities
	CreateAccount(ctx context.Context, account Account) (Account, error)
	UpdateAccount(ctx context.Context, id string, account Account) (Account, error)
	DisableAccount(ctx context.Context, id string) error
	DeleteAccount(ctx context.Context, id string) error
	ReadAccounts(ctx context.Context, startIndex, count int) ([]Account, int, error)
	AddGroupMember(ctx context.Context, groupID, accountID string) error
	RemoveGroupMember(ctx context.Context, groupID, accountID string) error
}

type Registry struct {
	discovery discovery.Repository
	client    *http.Client
}

func NewRegistry(discoveryRepo discovery.Repository, client *http.Client) *Registry {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Registry{discovery: discoveryRepo, client: client}
}

func (r *Registry) Build(cfg ConnectorConfig, secret JSONMap) (Connector, error) {
	switch cfg.Type {
	case ConnectorTypeSCIM:
		token, _ := secret["bearer_token"].(string)
		if token == "" {
			token, _ = secret["token"].(string)
		}
		return NewSCIMConnector(cfg.BaseURL, token, r.client)
	case ConnectorTypeRelay:
		return NewRelayConnector(cfg, r.discovery), nil
	default:
		return nil, fmt.Errorf("unsupported connector type %q", cfg.Type)
	}
}

func DefaultCapabilities(kind string) ConnectorCapabilities {
	switch kind {
	case ConnectorTypeRelay:
		return ConnectorCapabilities{CreateAccount: true, UpdateAccount: true, DisableAccount: true, DeleteAccount: true, ReadAccounts: true, GroupMembership: true}
	case ConnectorTypeSCIM:
		return ConnectorCapabilities{CreateAccount: true, UpdateAccount: true, DisableAccount: true, DeleteAccount: true, ReadAccounts: true, GroupMembership: true}
	default:
		return ConnectorCapabilities{}
	}
}

type SCIMConnector struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewSCIMConnector(baseURL, token string, client *http.Client) (*SCIMConnector, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("SCIM connector requires an https base_url")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &SCIMConnector{baseURL: parsed.String(), token: token, client: client}, nil
}

func (c *SCIMConnector) Capabilities() ConnectorCapabilities {
	return DefaultCapabilities(ConnectorTypeSCIM)
}

func (c *SCIMConnector) CreateAccount(ctx context.Context, account Account) (Account, error) {
	payload := JSONMap{"userName": account.UserName, "displayName": account.DisplayName, "active": account.Active}
	var out JSONMap
	if err := c.do(ctx, http.MethodPost, "/Users", payload, &out); err != nil {
		return Account{}, err
	}
	return scimAccount(out), nil
}

func (c *SCIMConnector) UpdateAccount(ctx context.Context, id string, account Account) (Account, error) {
	payload := JSONMap{"id": id, "userName": account.UserName, "displayName": account.DisplayName, "active": account.Active}
	var out JSONMap
	if err := c.do(ctx, http.MethodPut, "/Users/"+url.PathEscape(id), payload, &out); err != nil {
		return Account{}, err
	}
	return scimAccount(out), nil
}

func (c *SCIMConnector) DisableAccount(ctx context.Context, id string) error {
	payload := JSONMap{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": []JSONMap{{"op": "Replace", "path": "active", "value": false}}}
	return c.do(ctx, http.MethodPatch, "/Users/"+url.PathEscape(id), payload, nil)
}
func (c *SCIMConnector) DeleteAccount(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/Users/"+url.PathEscape(id), nil, nil)
}
func (c *SCIMConnector) ReadAccounts(ctx context.Context, startIndex, count int) ([]Account, int, error) {
	if startIndex < 1 {
		startIndex = 1
	}
	if count < 1 || count > 100 {
		count = 100
	}
	var out struct {
		TotalResults int       `json:"totalResults"`
		Resources    []JSONMap `json:"Resources"`
	}
	path := fmt.Sprintf("/Users?startIndex=%d&count=%d", startIndex, count)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, 0, err
	}
	accounts := make([]Account, 0, len(out.Resources))
	for _, raw := range out.Resources {
		accounts = append(accounts, scimAccount(raw))
	}
	return accounts, out.TotalResults, nil
}
func (c *SCIMConnector) AddGroupMember(ctx context.Context, groupID, accountID string) error {
	payload := JSONMap{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": []JSONMap{{"op": "Add", "path": "members", "value": []JSONMap{{"value": accountID}}}}}
	return c.do(ctx, http.MethodPatch, "/Groups/"+url.PathEscape(groupID), payload, nil)
}
func (c *SCIMConnector) RemoveGroupMember(ctx context.Context, groupID, accountID string) error {
	payload := JSONMap{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": []JSONMap{{"op": "Remove", "path": fmt.Sprintf(`members[value eq "%s"]`, accountID)}}}
	return c.do(ctx, http.MethodPatch, "/Groups/"+url.PathEscape(groupID), payload, nil)
}

func (c *SCIMConnector) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/scim+json, application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/scim+json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxSCIMResponseBytes)
	if resp.StatusCode >= 400 {
		b, rerr := io.ReadAll(limited)
		if rerr != nil {
			return fmt.Errorf("SCIM %s %s failed: %s (error body unreadable: %v)", method, path, resp.Status, rerr)
		}
		return fmt.Errorf("SCIM %s %s failed: %s", method, path, strings.TrimSpace(string(b)))
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(limited).Decode(out)
	}
	_, _ = io.Copy(io.Discard, limited)
	return nil
}

func scimAccount(raw JSONMap) Account {
	a := Account{Raw: raw, Active: true}
	if v, ok := raw["id"].(string); ok {
		a.ID = v
	}
	if v, ok := raw["userName"].(string); ok {
		a.UserName = v
	}
	if v, ok := raw["displayName"].(string); ok {
		a.DisplayName = v
	}
	if v, ok := raw["active"].(bool); ok {
		a.Active = v
	}
	return a
}

type RelayConnector struct {
	cfg       ConnectorConfig
	discovery discovery.Repository
}

func NewRelayConnector(cfg ConnectorConfig, discoveryRepo discovery.Repository) *RelayConnector {
	return &RelayConnector{cfg: cfg, discovery: discoveryRepo}
}
func (c *RelayConnector) Capabilities() ConnectorCapabilities {
	return DefaultCapabilities(ConnectorTypeRelay)
}
func (c *RelayConnector) enqueue(ctx context.Context, action string, payload JSONMap) error {
	if c.discovery == nil {
		return fmt.Errorf("discovery repository unavailable")
	}
	return c.discovery.CreateJob(ctx, &discovery.Job{OrganizationID: c.cfg.OrganizationID, CollectorID: c.cfg.CollectorID, JobType: discovery.JobTypePoll, Status: discovery.JobStatusPending, Config: map[string]any{"kind": "iga_relay", "connector_id": c.cfg.ID, "action": action, "payload": payload}})
}
func (c *RelayConnector) CreateAccount(ctx context.Context, a Account) (Account, error) {
	return a, c.enqueue(ctx, TaskActionCreateAccount, JSONMap{"account": a})
}
func (c *RelayConnector) UpdateAccount(ctx context.Context, id string, a Account) (Account, error) {
	a.ID = id
	return a, c.enqueue(ctx, TaskActionUpdateAccount, JSONMap{"id": id, "account": a})
}
func (c *RelayConnector) DisableAccount(ctx context.Context, id string) error {
	return c.enqueue(ctx, TaskActionDisableAccount, JSONMap{"id": id})
}
func (c *RelayConnector) DeleteAccount(ctx context.Context, id string) error {
	return c.enqueue(ctx, TaskActionDeleteAccount, JSONMap{"id": id})
}
func (c *RelayConnector) ReadAccounts(ctx context.Context, _, _ int) ([]Account, int, error) {
	return nil, 0, c.enqueue(ctx, "read_accounts", JSONMap{})
}
func (c *RelayConnector) AddGroupMember(ctx context.Context, groupID, accountID string) error {
	return c.enqueue(ctx, TaskActionAddGroupMember, JSONMap{"group_id": groupID, "account_id": accountID})
}
func (c *RelayConnector) RemoveGroupMember(ctx context.Context, groupID, accountID string) error {
	return c.enqueue(ctx, TaskActionRemoveGroupMember, JSONMap{"group_id": groupID, "account_id": accountID})
}

package iga

import "context"

type DriftDetector struct {
	repo     Repository
	registry *Registry
	decrypt  func(context.Context, string, string) (JSONMap, error)
}

func NewDriftDetector(repo Repository, registry *Registry, decrypt func(context.Context, string, string) (JSONMap, error)) *DriftDetector {
	return &DriftDetector{repo: repo, registry: registry, decrypt: decrypt}
}
func (d *DriftDetector) Reconcile(ctx context.Context, orgID, connectorID string, expected []Account) ([]DriftFinding, error) {
	cfg, err := d.repo.GetConnector(orgID, connectorID)
	if err != nil {
		return nil, err
	}
	var secret JSONMap
	if d.decrypt != nil && cfg.CredentialID != "" {
		secret, err = d.decrypt(ctx, orgID, cfg.CredentialID)
		if err != nil {
			return nil, err
		}
	}
	conn, err := d.registry.Build(*cfg, secret)
	if err != nil {
		return nil, err
	}
	observed, _, err := conn.ReadAccounts(ctx, 1, 100)
	if err != nil {
		return nil, err
	}
	return DetectDrift(orgID, connectorID, expected, observed), nil
}
func DetectDrift(orgID, connectorID string, expected, observed []Account) []DriftFinding {
	exp := map[string]Account{}
	obs := map[string]Account{}
	for _, a := range expected {
		key := a.ID
		if key == "" {
			key = a.UserName
		}
		exp[key] = a
	}
	for _, a := range observed {
		key := a.ID
		if key == "" {
			key = a.UserName
		}
		obs[key] = a
	}
	out := []DriftFinding{}
	for key, a := range obs {
		if _, ok := exp[key]; !ok {
			out = append(out, DriftFinding{OrganizationID: orgID, ConnectorID: connectorID, ExternalID: key, DriftType: "orphan_account", Severity: "high", Observed: JSONMap{"userName": a.UserName, "active": a.Active}, Status: "open"})
		}
	}
	for key, a := range exp {
		if _, ok := obs[key]; !ok {
			out = append(out, DriftFinding{OrganizationID: orgID, ConnectorID: connectorID, ExternalID: key, DriftType: "missing_account", Severity: "medium", Expected: JSONMap{"userName": a.UserName, "active": a.Active}, Status: "open"})
		}
	}
	return out
}

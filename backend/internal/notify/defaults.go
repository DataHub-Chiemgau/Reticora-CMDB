package notify

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Names of the default channels (F-157). Migration 000171 gives every
// existing organization without an in-app channel one under
// DefaultInAppName.
const (
	DefaultInAppName = "In-App"
	DefaultEmailName = "E-Mail"
)

// DefaultChannels are the channels an organization starts with (NTF-01,
// UI-16): the in-app channel of the notification center, and e-mail over the
// installation's SMTP when it has one (centralSMTP). Neither needs a
// configuration, so nothing is sealed.
func DefaultChannels(orgID string, centralSMTP bool) []Channel {
	out := []Channel{{OrganizationID: orgID, Name: DefaultInAppName, Kind: KindInApp, Provider: ProviderInApp, Enabled: true}}
	if centralSMTP {
		out = append(out, Channel{OrganizationID: orgID, Name: DefaultEmailName, Kind: KindEmail,
			Provider: ProviderCentralSMTP, Enabled: true})
	}
	return out
}

// ProvisionDefaultChannels creates the default channels of orgID in tx, the
// transaction that creates the organization (database.WithOrganizationSetup):
// without them every notification of a new organization is dropped until
// someone creates a channel (F-157).
func ProvisionDefaultChannels(ctx context.Context, tx pgx.Tx, orgID string, centralSMTP bool) error {
	channels := DefaultChannels(orgID, centralSMTP)
	for i := range channels {
		ch := &channels[i]
		if err := ch.Validate(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notification_channel (organization_id, name, kind, provider, enabled)
			VALUES ($1, $2, $3, $4, $5)`, ch.OrganizationID, ch.Name, ch.Kind, ch.Provider, ch.Enabled); err != nil {
			return fmt.Errorf("create default notification channel %s: %w", ch.Provider, err)
		}
	}
	return nil
}

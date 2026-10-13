package notify_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// TestChannelsAgainstPostgres covers NTF-01 (WP-137) against PostgreSQL:
// channels are stored per organization with the provider configuration
// encrypted (no secret in the row, a data key per row), read back
// decrypted, isolated between organizations, unique by name; the database
// enforces kind and provider.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestChannelsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "99")
	bg := context.Background()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	repo := notify.NewPGRepository(f.App, notify.NewSealer(enc))

	ch := &notify.Channel{OrganizationID: f.OrgA, Name: "own-smtp", Provider: notify.ProviderSMTP, Enabled: true,
		Config: map[string]any{"host": "mail.acme.example", "port": float64(587), "username": "cmdb", "password": "top-secret",
			"from": "cmdb@acme.example"}}
	if err = repo.Create(bg, ch); err != nil {
		t.Fatal(err)
	}
	if ch.ID == "" || ch.Kind != notify.KindEmail {
		t.Fatalf("created: %+v", ch)
	}
	var ciphertext, dek []byte
	if err = f.Admin.QueryRow(bg, `SELECT config_ciphertext, config_dek FROM notification_channel WHERE id = $1`, ch.ID).Scan(&ciphertext, &dek); err != nil {
		t.Fatal(err)
	}
	if len(ciphertext) == 0 || len(dek) == 0 || strings.Contains(string(ciphertext), "top-secret") || strings.Contains(string(ciphertext), "mail.acme") {
		t.Error("the configuration is not encrypted")
	}
	got, err := repo.Get(bg, f.OrgA, ch.ID)
	if err != nil || got.Config["password"] != "top-secret" || got.Config["host"] != "mail.acme.example" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err = repo.Get(bg, f.OrgB, ch.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Errorf("organization B reads A's channel: %v", err)
	}
	if err = repo.Create(bg, &notify.Channel{OrganizationID: f.OrgA, Name: "own-smtp", Provider: notify.ProviderInApp}); !errors.Is(err, notify.ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	if err = repo.Create(bg, &notify.Channel{OrganizationID: f.OrgB, Name: "own-smtp", Provider: notify.ProviderInApp, Enabled: true}); err != nil {
		t.Errorf("the same name in another organization: %v", err)
	}
	inApp := &notify.Channel{OrganizationID: f.OrgA, Name: "center", Provider: notify.ProviderInApp, Enabled: true}
	if err = repo.Create(bg, inApp); err != nil {
		t.Fatal(err)
	}
	var nullConfig bool
	if err = f.Admin.QueryRow(bg, `SELECT config_ciphertext IS NULL AND config_dek IS NULL FROM notification_channel WHERE id = $1`, inApp.ID).Scan(&nullConfig); err != nil || !nullConfig {
		t.Errorf("empty configuration stored: %v", err)
	}

	got.Config["password"] = "rotated"
	got.Enabled = false
	if err = repo.Update(bg, got); err != nil {
		t.Fatal(err)
	}
	var dek2 []byte
	if err = f.Admin.QueryRow(bg, `SELECT config_dek FROM notification_channel WHERE id = $1`, ch.ID).Scan(&dek2); err != nil || string(dek2) == string(dek) {
		t.Errorf("update kept the data key: %v", err)
	}
	list, err := repo.List(bg, f.OrgA)
	if err != nil || len(list) != 2 || list[0].Name != "center" || list[1].Config["password"] != "rotated" || list[1].Enabled {
		t.Errorf("list: %+v %v", list, err)
	}
	if err = repo.Update(bg, &notify.Channel{ID: ch.ID, OrganizationID: f.OrgB, Name: "x", Provider: notify.ProviderInApp}); !errors.Is(err, notify.ErrNotFound) {
		t.Errorf("update across organizations: %v", err)
	}
	if err = repo.Delete(bg, f.OrgB, ch.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Errorf("delete across organizations: %v", err)
	}
	if err = repo.Delete(bg, f.OrgA, ch.ID); err != nil {
		t.Fatal(err)
	}

	for name, sql := range map[string]string{
		"e-mail kind with the in-app provider": `INSERT INTO notification_channel (organization_id, name, kind, provider) VALUES ($1, 'x1', 'email', 'inapp')`,
		"sms without a provider yet":           `INSERT INTO notification_channel (organization_id, name, kind, provider) VALUES ($1, 'x2', 'sms', 'twilio')`,
		"ciphertext without a key":             `INSERT INTO notification_channel (organization_id, name, kind, provider, config_ciphertext) VALUES ($1, 'x3', 'inapp', 'inapp', '\x00')`,
	} {
		if _, err := f.Admin.Exec(bg, sql, f.OrgA); err == nil {
			t.Errorf("the database accepted %s", name)
		}
	}
}

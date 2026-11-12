package admp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp/readonly"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The holders of an ADMP connection (ADM-06, SEC-05, E-243, E-254;
// WP-1076). ADMP delivers them as the relation inhaber[] of GET
// /V1/anschluss/{id}; the cache keeps all holders of a connection as one
// pii_subject of the kind person (admp_connection.holder_subject_id), never
// in plain text. Only GET /api/v1/admp/connections/{id}/holder resolves
// it, with admp:holder_read and an audit entry in the same transaction.
// Deleting the subject (pii.DeleteTx, the erasure workflows of WP-1158)
// takes the private address anchored at it along (crypto-shredding).

// ErrHolderNotFound: the connection has no holder (none delivered, shredded
// or no vault).
var ErrHolderNotFound = errors.New("admp holder not found")

// Audit of a holder read (ADM-06).
const (
	HolderAuditAction   = "admp.holder.read"
	HolderResourceType  = "admp_holder"
	holderCountProperty = "holder_count"
)

// Holder is a holder of a connection (AnschlussInhaber without
// geburtsdatum and ids, E-254).
type Holder struct {
	Type              string `json:"type"`
	Title             string `json:"title,omitempty"`
	FirstName         string `json:"first_name,omitempty"`
	LastName          string `json:"last_name,omitempty"`
	RegisterCourt     string `json:"register_court,omitempty"`
	RegisterNumber    string `json:"register_number,omitempty"`
	Street            string `json:"-"`
	HouseNumber       string `json:"-"`
	HouseNumberSuffix string `json:"-"`
	PostalCode        string `json:"-"`
	City              string `json:"-"`
}

// holderFields are the keys of one holder in the subject: <n>.<key>.
var holderFields = []string{"typ", "titel", "vorname", "name", "registerinstanz", "registernummer", "strasse", "hausnummer",
	"hausnummerzusatz", "plz", "ort"}

// values returns the fields of h in the order of holderFields.
func (h *Holder) values() []*string {
	return []*string{&h.Type, &h.Title, &h.FirstName, &h.LastName, &h.RegisterCourt, &h.RegisterNumber, &h.Street,
		&h.HouseNumber, &h.HouseNumberSuffix, &h.PostalCode, &h.City}
}

// holdersOf maps the holders of the read-only client.
func holdersOf(in []readonly.Holder) []Holder {
	if in == nil {
		return nil
	}
	out := make([]Holder, 0, len(in))
	for i := range in {
		h := &in[i]
		out = append(out, Holder{Type: h.Type, Title: h.Title, FirstName: h.FirstName, LastName: h.LastName,
			RegisterCourt: h.RegisterCourt, RegisterNumber: h.RegisterNumber, Street: h.Street, HouseNumber: h.HouseNumber,
			HouseNumberSuffix: h.HouseNumberSuffix, PostalCode: h.PostalCode, City: h.City})
	}
	return out
}

// holderData is the subject of the holders: count and <n>.<field> per
// holder (n from 1 in the order of ADMP); empty fields are left out.
func holderData(holders []Holder) pii.Data {
	data := pii.Data{"count": strconv.Itoa(len(holders))}
	for i := range holders {
		h := holders[i]
		for j, v := range h.values() {
			if *v != "" {
				data[strconv.Itoa(i+1)+"."+holderFields[j]] = *v
			}
		}
	}
	return data
}

// holdersFrom reads the holders of a subject.
func holdersFrom(data pii.Data) []Holder {
	n, err := strconv.Atoi(data["count"])
	if err != nil || n < 0 {
		return []Holder{}
	}
	out := make([]Holder, 0, n)
	for i := 1; i <= n; i++ {
		var h Holder
		for j, v := range h.values() {
			*v = data[strconv.Itoa(i)+"."+holderFields[j]]
		}
		out = append(out, h)
	}
	return out
}

// storeHolders keeps the holders of c in the vault in tx (WP-1076, E-254):
// one subject of the kind person, created when the connection has none,
// updated in place (same surrogate) when its data changed. A delivered
// empty list shreds the subject (pii.DeleteTx); holders not delivered
// (HoldersKnown false) leave subject and count as they are. Without a
// vault no holder is stored. properties.holder_count follows the subject.
func storeHolders(ctx context.Context, tx pgx.Tx, c *Connection, previous *connectionState) error {
	holders, known := c.Holders, c.HoldersKnown
	c.Holders, c.HoldersKnown = nil, false
	vault := pii.Default()
	if !known || vault == nil {
		if c.HolderSubjectID != "" && c.HolderSubjectID == previous.holderSubjectID {
			if count, ok := previous.holderCount(); ok {
				c.Properties[holderCountProperty] = count
			}
		}
		return nil
	}
	if len(holders) == 0 {
		if previous.holderSubjectID != "" {
			if err := pii.DeleteTx(ctx, tx, c.OrganizationID, previous.holderSubjectID); err != nil && !errors.Is(err, pii.ErrNotFound) {
				return err
			}
		}
		c.HolderSubjectID = ""
		delete(c.Properties, holderCountProperty)
		return nil
	}
	data := holderData(holders)
	c.Properties[holderCountProperty] = float64(len(holders))
	if previous.holderSubjectID != "" {
		subjects, err := vault.ResolveTx(ctx, tx, c.OrganizationID, []string{previous.holderSubjectID})
		if err != nil {
			return err
		}
		if s := subjects[previous.holderSubjectID]; s != nil {
			c.HolderSubjectID = previous.holderSubjectID
			if maps.Equal(s.Data, data) {
				return nil
			}
			return vault.UpdateTx(ctx, tx, c.OrganizationID, previous.holderSubjectID, data)
		}
	}
	id, err := vault.CreateTx(ctx, tx, c.OrganizationID, pii.KindPerson, data)
	if err != nil {
		return err
	}
	c.HolderSubjectID = id
	return nil
}

// holderCount reads holder_count of the stored properties.
func (s *connectionState) holderCount() (float64, bool) {
	if len(s.properties) == 0 {
		return 0, false
	}
	var props map[string]any
	if err := json.Unmarshal(s.properties, &props); err != nil {
		return 0, false
	}
	n, ok := props[holderCountProperty].(float64)
	return n, ok
}

// holderCountOf returns properties.holder_count of a connection with a
// holder, else 0.
func holderCountOf(c *Connection) int {
	if c.HolderSubjectID == "" {
		return 0
	}
	switch n := c.Properties[holderCountProperty].(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// HolderView is a holder as GET /api/v1/admp/connections/{id}/holder
// answers it: the address as one line.
type HolderView struct {
	Holder
	Address string `json:"address,omitempty"`
}

func holderViews(holders []Holder) []HolderView {
	out := make([]HolderView, 0, len(holders))
	for i := range holders {
		h := holders[i]
		out = append(out, HolderView{Holder: h,
			Address: AddressLine(h.Street, h.HouseNumber, h.HouseNumberSuffix, h.PostalCode, h.City)})
	}
	return out
}

// resolveHolders resolves the holder subject of a visible connection in tx
// and records the read (action admp.holder.read, resource admp_holder, the
// connection's id, no personal value in changes) in the same transaction.
func resolveHolders(ctx context.Context, tx pgx.Tx, orgID, connectionID string) ([]Holder, error) {
	var subject string
	err := tx.QueryRow(ctx, `SELECT COALESCE(holder_subject_id::text, '') FROM admp_connection WHERE id = $1`, connectionID).
		Scan(&subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCacheNotFound
	}
	if err != nil {
		return nil, err
	}
	vault := pii.Default()
	if subject == "" || vault == nil {
		return nil, ErrHolderNotFound
	}
	subjects, err := vault.ResolveTx(ctx, tx, orgID, []string{subject})
	if err != nil {
		return nil, err
	}
	s := subjects[subject]
	if s == nil {
		return nil, ErrHolderNotFound
	}
	holders := holdersFrom(s.Data)
	actor := tenant.FromContext(ctx).UserID
	actorType := "user"
	if actor == "" {
		actorType = "system"
	}
	if _, err = audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: HolderAuditAction, ResourceType: HolderResourceType, ResourceID: connectionID,
		Changes: map[string]any{"holder_subject_id": subject, "holder_count": len(holders)}}); err != nil {
		return nil, fmt.Errorf("audit %s: %w", HolderAuditAction, err)
	}
	return holders, nil
}

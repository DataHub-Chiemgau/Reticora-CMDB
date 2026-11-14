package readonly

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// The connections (Anschluss) of ADMP with their holders and their
// carriers (ADM-06, ADM-07, E-252, E-254; WP-1075, WP-1076). No other path of the documentation is read for a connection:
// the services come over the ports (WP-1073), the holder paginator stays
// outside the allowlist (E-254).

// connectionPath is getV1Anschluss:id, organizationsPath
// getV1OrganizationByIds with exactly one id in the query.
const (
	connectionPath    = "/V1/anschluss/{id}"
	organizationsPath = "/V1/organization/byIds"
)

var _ = allowGets(getRoute{path: connectionPath}, getRoute{path: organizationsPath, query: []string{"id"}})

// ErrNotFound: ADMP answered 404 (NamedError) for a connection, or the
// organization of a carrier is not in the answer of byIds.
var ErrNotFound = errors.New("admp: object not found")

// Connection is an Anschluss of ADMP: the ten required members of the
// documentation, the carrier when the answer embeds it (netzbetreiber)
// and the holders (inhaber). Times are numbers without unit as ADMP sends
// them (E-253). Holders is nil when the answer has no inhaber (the
// relation was not delivered), else the delivered list, possibly empty.
// dienste, actions and every relation of the carrier are dropped.
type Connection struct {
	ID                int64
	CarrierID         int64
	ContractID        string
	Created           *float64
	Removed           *float64
	Street            string
	HouseNumber       string
	HouseNumberSuffix string
	PostalCode        string
	City              string
	Carrier           *Organization
	Holders           []Holder
}

// Organization is a carrier (Organization of the documentation): id,
// name, short name and carrier number (netzbetreiberNummer, nil when
// null). groupName and the relations (anschlüsse, authUsers, authGroups,
// accessNodes, dienstTemplates) are dropped.
type Organization struct {
	ID        int64
	Name      string
	ShortName string
	Number    *float64
}

// Holder is an AnschlussInhaber (ADM-06, E-254, WP-1076): its typ
// (Privat, Gewerblich, Genossenschaft), title, first and last name,
// register court and number and its address; null members are empty.
// geburtsdatum, id, anschlussId and anschluss are dropped (data
// minimisation): the holders come ordered by their ADMP id, the id itself
// is not kept.
type Holder struct {
	Type              string
	Title             string
	FirstName         string
	LastName          string
	RegisterCourt     string
	RegisterNumber    string
	Street            string
	HouseNumber       string
	HouseNumberSuffix string
	PostalCode        string
	City              string
}

// rawOrganization is the part of Organization a read decodes.
type rawOrganization struct {
	ID                  *docInt  `json:"id"`
	Name                *string  `json:"name"`
	ShortName           *string  `json:"shortName"`
	NetzbetreiberNummer *float64 `json:"netzbetreiberNummer"`
}

func (o *rawOrganization) organization() (*Organization, bool) {
	if o.ID == nil || o.Name == nil {
		return nil, false
	}
	out := &Organization{ID: int64(*o.ID), Name: *o.Name, Number: o.NetzbetreiberNummer}
	if o.ShortName != nil {
		out.ShortName = *o.ShortName
	}
	return out, true
}

// rawHolder is the part of AnschlussInhaber a read decodes; the id only
// orders the holders.
type rawHolder struct {
	ID               *docInt `json:"id"`
	Typ              *string `json:"typ"`
	Titel            *string `json:"titel"`
	Vorname          *string `json:"vorname"`
	Name             *string `json:"name"`
	Registerinstanz  *string `json:"registerinstanz"`
	Registernummer   *string `json:"registernummer"`
	Strasse          *string `json:"strasse"`
	Hausnummer       *string `json:"hausnummer"`
	Hausnummerzusatz *string `json:"hausnummerzusatz"`
	PLZ              *string `json:"plz"`
	Ort              *string `json:"ort"`
}

func (h *rawHolder) holder() Holder {
	return Holder{Type: *h.Typ, Title: text(h.Titel), FirstName: text(h.Vorname), LastName: text(h.Name),
		RegisterCourt: text(h.Registerinstanz), RegisterNumber: text(h.Registernummer), Street: text(h.Strasse),
		HouseNumber: text(h.Hausnummer), HouseNumberSuffix: text(h.Hausnummerzusatz), PostalCode: text(h.PLZ), City: text(h.Ort)}
}

// text returns the value of a string-or-null member.
func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// GetConnection reads a connection: GET /V1/anschluss/{id}. Only a 404
// whose body is a NamedError of ADMP is ErrNotFound (class client, HTTP
// 404); a 404 without it (proxy, gateway, wrong base path) stays a plain
// client failure, so the cache entry turns stale instead of being deleted.
// An answer for another id than the requested one is a decode failure.
func (c *Client) GetConnection(ctx context.Context, id int64) (*Connection, error) {
	p, err := docPath(connectionPath, id)
	if err != nil {
		return nil, err
	}
	var raw struct {
		ID               *docInt          `json:"id"`
		NetzbetreiberID  *docInt          `json:"netzbetreiberId"`
		VertragID        *string          `json:"vertragId"`
		Created          *float64         `json:"created"`
		Removed          *float64         `json:"removed"`
		Strasse          *string          `json:"strasse"`
		Hausnummer       *string          `json:"hausnummer"`
		Hausnummerzusatz *string          `json:"hausnummerzusatz"`
		PLZ              *string          `json:"plz"`
		Ort              *string          `json:"ort"`
		Netzbetreiber    *rawOrganization `json:"netzbetreiber"`
		Inhaber          *[]rawHolder     `json:"inhaber"`
	}
	if gerr := c.getJSON(ctx, p, nil, &raw); gerr != nil {
		var e *Error
		if errors.As(gerr, &e) && e.Class == ClassClient && e.Status == http.StatusNotFound && e.named {
			return nil, classErr(ClassClient, http.StatusNotFound, ErrNotFound)
		}
		return nil, gerr
	}
	if raw.ID == nil || raw.NetzbetreiberID == nil || int64(*raw.ID) != id {
		// Another connection than the asked one must never be stored under
		// the requested id (its address and holders belong elsewhere).
		return nil, classErr(ClassDecode, 0, nil)
	}
	out := &Connection{ID: int64(*raw.ID), CarrierID: int64(*raw.NetzbetreiberID), ContractID: text(raw.VertragID),
		Created: raw.Created, Removed: raw.Removed, Street: text(raw.Strasse), HouseNumber: text(raw.Hausnummer),
		HouseNumberSuffix: text(raw.Hausnummerzusatz), PostalCode: text(raw.PLZ), City: text(raw.Ort)}
	if raw.Netzbetreiber != nil {
		org, ok := raw.Netzbetreiber.organization()
		if !ok {
			return nil, classErr(ClassDecode, 0, nil)
		}
		out.Carrier = org
	}
	if raw.Inhaber != nil {
		holders := *raw.Inhaber
		for i := range holders {
			if holders[i].ID == nil || holders[i].Typ == nil {
				return nil, classErr(ClassDecode, 0, nil)
			}
		}
		// Ordered by the ADMP id (stable insertion sort: a connection has
		// few holders).
		for i := 1; i < len(holders); i++ {
			for j := i; j > 0 && *holders[j].ID < *holders[j-1].ID; j-- {
				holders[j], holders[j-1] = holders[j-1], holders[j]
			}
		}
		out.Holders = make([]Holder, 0, len(holders))
		for i := range holders {
			out.Holders = append(out.Holders, holders[i].holder())
		}
	}
	return out, nil
}

// GetOrganization reads one carrier: GET /V1/organization/byIds?id=<id>
// with exactly one id; the element with that id is decoded. An answer
// without it is ErrNotFound.
func (c *Client) GetOrganization(ctx context.Context, id int64) (*Organization, error) {
	if id < 1 || id > maxDocInt {
		return nil, classErr(ClassClient, 0, ErrInvalidPath)
	}
	var raw []rawOrganization
	if err := c.getJSON(ctx, organizationsPath, url.Values{"id": {strconv.FormatInt(id, 10)}}, &raw); err != nil {
		return nil, err
	}
	for i := range raw {
		if raw[i].ID == nil || int64(*raw[i].ID) != id {
			continue
		}
		org, ok := raw[i].organization()
		if !ok {
			return nil, classErr(ClassDecode, 0, nil)
		}
		return org, nil
	}
	return nil, classErr(ClassClient, http.StatusNotFound, ErrNotFound)
}

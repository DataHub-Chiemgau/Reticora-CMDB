package readonly

import (
	"context"
	"encoding/json"
)

// The services (Dienst) of ADMP (ADM-04, ADM-05, E-253; WP-1073, WP-1074).

// servicePath is getV1Dienst:id, servicePropertiesPath
// getV1Dienst:idPropertiesPublic. GET /V1/dienst/{id}/properties (all
// properties) is not used (E-253).
const (
	servicePath           = "/V1/dienst/{id}"
	servicePropertiesPath = "/V1/dienst/{id}/properties/public"
)

var _ = allowGets(getRoute{path: servicePath}, getRoute{path: servicePropertiesPath})

// Service is a Dienst of ADMP: its id, its connection (anschlussId), its
// template (templateId and, when the answer embeds it, the template name)
// and the six times of its life cycle as ADMP sends them (number without
// unit, E-253; nil when null). The connection, the properties and the
// actions of the answer are dropped.
type Service struct {
	ID                  int64
	ConnectionID        int64
	TemplateID          *int64
	TemplateName        string
	Booked              *float64
	ProvisioningPlanned *float64
	Provisioned         *float64
	Cancelled           *float64
	ShutdownPlanned     *float64
	ShutDown            *float64
}

// GetService reads a service: GET /V1/dienst/{id}.
func (c *Client) GetService(ctx context.Context, id int64) (*Service, error) {
	p, err := docPath(servicePath, id)
	if err != nil {
		return nil, err
	}
	var raw struct {
		ID          *docInt `json:"id"`
		AnschlussID *docInt `json:"anschlussId"`
		TemplateID  *docInt `json:"templateId"`
		Template    *struct {
			Name string `json:"name"`
		} `json:"template"`
		Gebucht        *float64 `json:"gebucht"`
		Bereitstellung *float64 `json:"bereitstellung"`
		Bereitgestellt *float64 `json:"bereitgestellt"`
		Gekuendigt     *float64 `json:"gekuendigt"`
		Abschaltung    *float64 `json:"abschaltung"`
		Abgeschaltet   *float64 `json:"abgeschaltet"`
	}
	if gerr := c.getJSON(ctx, p, nil, &raw); gerr != nil {
		return nil, gerr
	}
	if raw.ID == nil || raw.AnschlussID == nil {
		return nil, classErr(ClassDecode, 0, nil)
	}
	s := &Service{ID: int64(*raw.ID), ConnectionID: int64(*raw.AnschlussID), Booked: raw.Gebucht,
		ProvisioningPlanned: raw.Bereitstellung, Provisioned: raw.Bereitgestellt, Cancelled: raw.Gekuendigt,
		ShutdownPlanned: raw.Abschaltung, ShutDown: raw.Abgeschaltet}
	if raw.TemplateID != nil {
		v := int64(*raw.TemplateID)
		s.TemplateID = &v
	}
	if raw.Template != nil {
		s.TemplateName = raw.Template.Name
	}
	return s, nil
}

// ServiceProperty is a public property of a Dienst (DienstProperty of
// /properties/public): key, value (SerializeableData: null, boolean,
// string, number, object or array), label (label, else title), order,
// public flag and timestamp as sent. description, input and dienst are
// dropped.
type ServiceProperty struct {
	Key       string
	Value     any
	Label     string
	Order     *int64
	Public    bool
	Timestamp *int64
}

// ListServiceProperties reads the public properties of a service: GET
// /V1/dienst/{id}/properties/public.
func (c *Client) ListServiceProperties(ctx context.Context, serviceID int64) ([]ServiceProperty, error) {
	p, err := docPath(servicePropertiesPath, serviceID)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Key        *string          `json:"key"`
		Value      *json.RawMessage `json:"value"`
		Label      *string          `json:"label"`
		Title      *string          `json:"title"`
		OrderIndex *docInt          `json:"orderIndex"`
		IsPublic   *bool            `json:"isPublic"`
		Timestamp  *docInt          `json:"timestamp"`
	}
	if gerr := c.getJSON(ctx, p, nil, &raw); gerr != nil {
		return nil, gerr
	}
	out := make([]ServiceProperty, 0, len(raw))
	for i := range raw {
		r := &raw[i]
		if r.Key == nil || *r.Key == "" {
			return nil, classErr(ClassDecode, 0, nil)
		}
		prop := ServiceProperty{Key: *r.Key, Public: r.IsPublic == nil || *r.IsPublic}
		if r.Value != nil {
			if derr := json.Unmarshal(*r.Value, &prop.Value); derr != nil {
				return nil, classErr(ClassDecode, 0, nil)
			}
		}
		switch {
		case r.Label != nil && *r.Label != "":
			prop.Label = *r.Label
		case r.Title != nil:
			prop.Label = *r.Title
		}
		if r.OrderIndex != nil {
			v := int64(*r.OrderIndex)
			prop.Order = &v
		}
		if r.Timestamp != nil {
			v := int64(*r.Timestamp)
			prop.Timestamp = &v
		}
		out = append(out, prop)
	}
	return out, nil
}

package readonly

import (
	"context"
)

// The services (Dienst) of ADMP (ADM-04, ADM-05, E-253; WP-1073).

// servicePath is getV1Dienst:id.
const servicePath = "/V1/dienst/{id}"

var _ = allowGets(getRoute{path: servicePath})

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

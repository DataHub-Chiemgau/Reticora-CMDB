// Package enrollment implements the shared device enrollment protocol used by edge components.
package enrollment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/transport"
)

// EnrollmentRequest describes the device identity sent to the control plane.
type EnrollmentRequest struct {
	DeviceID        string            `json:"deviceId"`
	EnrollmentToken string            `json:"enrollmentToken,omitempty"`
	Hostname        string            `json:"hostname,omitempty"`
	Site            string            `json:"site,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Capabilities    []string          `json:"capabilities,omitempty"`
	CSRPEM          string            `json:"csrPem,omitempty"`
}

// EnrollmentResponse carries the issued identity material for an edge device.
type EnrollmentResponse struct {
	DeviceID             string            `json:"deviceId"`
	ClientCertificatePEM string            `json:"clientCertificatePem,omitempty"`
	ClientPrivateKeyPEM  string            `json:"clientPrivateKeyPem,omitempty"`
	CertificateAuthority string            `json:"certificateAuthorityPem,omitempty"`
	AssignedLabels       map[string]string `json:"assignedLabels,omitempty"`
	ExpiresAt            time.Time         `json:"expiresAt,omitempty"`
}

// Client enrolls an edge component with the Reticora control plane.
type Client interface {
	Enroll(ctx context.Context, req EnrollmentRequest) (*EnrollmentResponse, error)
}

// HTTPClient implements the enrollment protocol over HTTPS.
type HTTPClient struct {
	BaseURL   string
	Endpoint  string
	Transport transport.Transport
}

// Enroll posts an enrollment request to the configured server endpoint.
func (c *HTTPClient) Enroll(ctx context.Context, req EnrollmentRequest) (*EnrollmentResponse, error) {
	if c.Transport == nil {
		return nil, fmt.Errorf("enrollment: transport is required")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("enrollment: marshal request: %w", err)
	}

	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = "/api/v1/enrollment"
	}

	url := strings.TrimRight(c.BaseURL, "/") + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("enrollment: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.Transport.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("enrollment: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("enrollment: unexpected status %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}

	var out EnrollmentResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("enrollment: decode response: %w", err)
	}

	return &out, nil
}

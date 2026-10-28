package operator

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/prometheus/client_golang/prometheus"
)

// SessionLifetime of an operator session; there is no refresh, an operator
// logs in again (with MFA).
const SessionLifetime = 15 * time.Minute

// BreakGlassHeader carries RETICORA_OPERATOR_TOKEN.
const BreakGlassHeader = "X-Operator-Token"

// Kinds of operator identity.
const (
	KindOIDC       = "oidc"
	KindBreakGlass = "break_glass"
)

// Config configures the operator path.
type Config struct {
	// BreakGlassToken is RETICORA_OPERATOR_TOKEN; empty disables break-glass.
	BreakGlassToken string
	// Group is the IdP group of operators (default "operators").
	Group string
	// MFAACRValues are acr values that prove MFA in addition to amr methods
	// (default "2", the Keycloak level of a second factor).
	MFAACRValues []string
}

func (c *Config) group() string {
	if strings.TrimSpace(c.Group) == "" {
		return "operators"
	}
	return c.Group
}

// mfaMethods are amr values (RFC 8176) that mean more than one factor.
var mfaMethods = []string{"mfa", "otp", "totp", "hwk", "swk", "webauthn", "sms"}

// MFASatisfied reports whether the login used a second factor.
func (c *Config) MFASatisfied(amr []string, acr string) bool {
	for _, m := range amr {
		if slices.Contains(mfaMethods, strings.ToLower(m)) {
			return true
		}
	}
	acrValues := c.MFAACRValues
	if len(acrValues) == 0 {
		acrValues = []string{"2"}
	}
	return acr != "" && slices.Contains(acrValues, acr)
}

// Identity is the authenticated operator of a request.
type Identity struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Kind  string `json:"kind"`
}

type identityKey struct{}

// FromContext returns the operator of the request.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

var breakGlassUses atomic.Int64

// BreakGlassUses is the number of break-glass logins since start.
func BreakGlassUses() int64 { return breakGlassUses.Load() }

// RegisterMetrics registers reticora_operator_break_glass_total, the alarm
// signal of SEC-07.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Namespace: "reticora",
		Name:      "operator_break_glass_total",
		Help:      "Requests authenticated with the operator break-glass token (every use is an alarm).",
	}, func() float64 { return float64(BreakGlassUses()) }))
}

// authenticate resolves the operator of a request: the break-glass header or
// an operator session token. Tenant sessions and API keys are refused.
func (h *Handler) authenticate(r *http.Request) (Identity, bool) {
	if token := r.Header.Get(BreakGlassHeader); token != "" {
		if h.cfg.BreakGlassToken == "" ||
			subtle.ConstantTimeCompare([]byte(token), []byte(h.cfg.BreakGlassToken)) != 1 {
			return Identity{}, false
		}
		breakGlassUses.Add(1)
		slog.Error("SECURITY ALERT: operator break-glass token used",
			"alert", "operator_break_glass", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		return Identity{ID: "break-glass", Kind: KindBreakGlass}, true
	}
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, token, found := strings.Cut(authz, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || h.sessions == nil {
		return Identity{}, false
	}
	claims, err := h.sessions.Validate(strings.TrimSpace(token))
	if err != nil || !claims.Operator || claims.ExpiresAt.IsZero() || !time.Now().Before(claims.ExpiresAt) {
		return Identity{}, false
	}
	return Identity{ID: claims.Subject, Name: claims.Name, Email: claims.Email, Kind: KindOIDC}, true
}

// heldResponse holds a response back until its audit entry is written.
type heldResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (h *heldResponse) Header() http.Header { return h.header }

func (h *heldResponse) WriteHeader(code int) {
	if h.status == 0 {
		h.status = code
	}
}

func (h *heldResponse) Write(b []byte) (int, error) {
	if h.status == 0 {
		h.status = http.StatusOK
	}
	return h.body.Write(b)
}

// send writes the held response.
func (h *heldResponse) send(w http.ResponseWriter) {
	maps.Copy(w.Header(), h.header)
	if h.status == 0 {
		h.status = http.StatusOK
	}
	w.WriteHeader(h.status)
	_, _ = w.Write(h.body.Bytes())
}

// auditedKey marks a request whose handler wrote its change together with
// its operator_audit entry (markAudited).
type auditedKey struct{}

// markAudited tells Middleware that the change of the request and its
// entry were written atomically (RecordChange, OrgCreator): the request
// needs no entry of its own, and the response is not held back.
func markAudited(ctx context.Context) {
	if done, ok := ctx.Value(auditedKey{}).(*bool); ok {
		*done = true
	}
}

// Middleware admits operators only and records every request in
// operator_audit, refused ones included. A change is recorded atomically
// by its handler (markAudited); every other response is held back until
// its entry is written and becomes a 500 when the entry fails (F-208).
func (h *Handler) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op, ok := h.authenticate(r)
		if !ok {
			// A failed attempt counts against the pre-auth budget of the
			// IP (AUT-10) even when its entry fails and it is answered 500.
			middleware.SpendPreAuthSlot(r.Context())
			if h.record(w, r, &AuditEntry{OperatorID: "anonymous", OperatorKind: KindOIDC,
				Action: actionFor(r.Method, r.URL.Path), Status: http.StatusUnauthorized,
				Details: map[string]any{"remote": r.RemoteAddr}}) {
				api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "operator authentication required")
			}
			return
		}
		held := &heldResponse{header: w.Header().Clone()}
		audited := false
		ctx := context.WithValue(context.WithValue(r.Context(), identityKey{}, op), auditedKey{}, &audited)
		next.ServeHTTP(held, r.WithContext(ctx))
		if !audited && !h.record(w, r, &AuditEntry{OperatorID: op.ID, OperatorKind: op.Kind,
			Action: actionFor(r.Method, r.URL.Path), TargetOrg: targetOrg(r), Status: held.status,
			Details: map[string]any{"remote": r.RemoteAddr}}) {
			return
		}
		held.send(w)
	})
}

// record writes an audit entry and reports whether it was written. A
// failure is logged and answered 500 (the response of the request is not
// sent): no operator action without its entry (F-208).
func (h *Handler) record(w http.ResponseWriter, r *http.Request, e *AuditEntry) bool {
	if e.Status == 0 {
		e.Status = http.StatusOK
	}
	err := h.audit.Record(context.WithoutCancel(r.Context()), e)
	if err == nil {
		return true
	}
	writeAuditFailure(w, e, err)
	return false
}

// writeAuditFailure logs a failed operator_audit write and answers 500
// with a fixed text.
func writeAuditFailure(w http.ResponseWriter, e *AuditEntry, err error) {
	slog.Error("operator audit failed", "error", err, "action", e.Action, "operator", e.OperatorID)
	api.WriteError(w, http.StatusInternalServerError, "Internal Server Error",
		"the operator audit entry could not be written; the request was not carried out")
}

// targetOrg extracts the organization of /admin/orgs/{id}/... requests.
func targetOrg(r *http.Request) string {
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/admin/orgs/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	if len(id) != 36 {
		return ""
	}
	return id
}

// newTokenID returns a random token id (jti).
func newTokenID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

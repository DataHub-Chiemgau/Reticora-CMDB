package operator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The Alertmanager bridge of the operator path (OPS-05, E-75): Alertmanager
// posts its webhook notifications to POST /api/v1/admin/alerts/alertmanager
// with the bearer token RETICORA_OPS_ALERT_TOKEN. Each notification of an
// alert group becomes one notification of the operator organization
// (RETICORA_OPS_ALERT_ORG_ID) on its notification channels. The payload is
// input of whoever holds the token: it is bounded, its values reach SQL only
// as parameters and the logs not at all.

// AlertPath is the route of the bridge.
const AlertPath = "/api/v1/admin/alerts/alertmanager"

// Limits of one request. Alertmanager sends at most 50 alerts per
// notification (max_alerts in deploy/monitoring/alertmanager.yaml).
const (
	MaxAlertBody = 1 << 20
	MaxAlerts    = 100
	// listedAlerts is how many alerts the text of a notification lists.
	listedAlerts = 20
	// maxLabels is how many labels of a group the data of a notification
	// carries.
	maxLabels = 32
)

// AlertCategory is the notification category of the operations alerts.
const AlertCategory = "ops_alert"

// AlertAction is the audit action of a queued operations alert.
const AlertAction = "notification.ops_alert_queued"

// Alert statuses of Alertmanager.
const (
	alertFiring   = "firing"
	alertResolved = "resolved"
)

// alertAudience are the recipients in the operator organization: its users
// who may write monitoring organization-wide (org_admin, engineer).
var alertAudience = notify.Audience{Permission: identity.PermMonitoringWrite, AllWithPermission: true}

// AlertConfig configures the bridge.
type AlertConfig struct {
	// Token is RETICORA_OPS_ALERT_TOKEN, the bearer token of Alertmanager.
	Token string
	// OrgID is RETICORA_OPS_ALERT_ORG_ID, the operator organization; empty
	// switches the bridge off (404, E-75).
	OrgID string
}

func (c AlertConfig) enabled() bool { return c.OrgID != "" && c.Token != "" }

// authorized reports whether r carries the token as its bearer token. Both
// values are hashed before the constant-time comparison, so neither the
// content nor the length of the token shows in the timing.
func (c AlertConfig) authorized(r *http.Request) bool {
	scheme, token, found := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	token = strings.TrimSpace(token)
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || c.Token == "" {
		return false
	}
	got, want := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(c.Token))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// AlertNotifier queues a notification on the channels of an organization
// (notify.Notifier).
type AlertNotifier interface {
	Notify(ctx context.Context, orgID string, req *notify.Request) (int, error)
}

// AlertBridge serves POST /api/v1/admin/alerts/alertmanager.
type AlertBridge struct {
	cfg AlertConfig
	// audit records the refused requests in operator_audit like every
	// other request of the operator path.
	audit *Auditor
	// pool, notifier, channels and resolver deliver; without them a
	// configured bridge answers 503.
	pool     *pgxpool.Pool
	notifier AlertNotifier
	channels notify.Repository
	resolver notify.Resolver
}

// NewAlertBridge creates the bridge.
func NewAlertBridge(cfg AlertConfig, auditor *Auditor) *AlertBridge {
	return &AlertBridge{cfg: cfg, audit: auditor}
}

// WithDelivery sets what the bridge delivers with: the notifier queues on
// the channels of the operator organization, the resolver finds its
// recipients; pool holds the audit log of the organization.
func (b *AlertBridge) WithDelivery(pool *pgxpool.Pool, notifier AlertNotifier, channels notify.Repository, resolver notify.Resolver) *AlertBridge {
	b.pool, b.notifier, b.channels, b.resolver = pool, notifier, channels, resolver
	return b
}

// RegisterRoutes registers the bridge behind its own authentication.
func (b *AlertBridge) RegisterRoutes(r chi.Router) {
	r.With(b.authenticate).Post(AlertPath, b.Receive)
}

// authenticate admits Alertmanager only. Without an operator organization
// the route answers 404 like an unknown one, also with a valid token. The
// token is the only credential: tenant sessions, API keys and operator
// credentials are refused (401, recorded in operator_audit). The body is
// read here, bounded to MaxAlertBody (413), before the validation of the
// route or the handler reads it.
func (b *AlertBridge) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !b.cfg.enabled() {
			api.WriteError(w, http.StatusNotFound, "Not Found", "the requested resource does not exist")
			return
		}
		if !b.cfg.authorized(r) {
			if err := b.audit.Record(context.WithoutCancel(r.Context()), &AuditEntry{OperatorID: "anonymous", OperatorKind: KindOIDC,
				Action: actionFor(r.Method, r.URL.Path), Status: http.StatusUnauthorized,
				Details: map[string]any{"remote": r.RemoteAddr}}); err != nil {
				slog.Error("operator audit failed", "error", err, "action", actionFor(r.Method, r.URL.Path))
			}
			api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "the bearer token of the alert bridge is required")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, MaxAlertBody+1))
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "unable to read request body")
			return
		}
		if len(body) > MaxAlertBody {
			api.WriteError(w, http.StatusRequestEntityTooLarge, "Payload Too Large",
				fmt.Sprintf("the body is larger than %d bytes", MaxAlertBody))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

// Receive queues the notification of an Alertmanager payload on the
// channels of the operator organization (202). The deliveries and their
// audit entry are written in one transaction of the organization.
func (b *AlertBridge) Receive(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxAlertBody+1))
	if err != nil || len(raw) > MaxAlertBody {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "unable to read request body")
		return
	}
	p, err := decodeAlerts(raw)
	if errors.Is(err, errInvalidAlerts) {
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "the body is no valid Alertmanager JSON payload")
		return
	}
	if b.pool == nil || b.notifier == nil || b.channels == nil || b.resolver == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the alert bridge needs the database")
		return
	}
	ctx, orgID := r.Context(), b.cfg.OrgID
	channels, err := b.channels.List(ctx, orgID)
	if err != nil {
		slog.Error("alert bridge: list the channels of the operator organization", "organization_id", orgID, "error", err)
		api.WriteRepoError(w, err)
		return
	}
	enabled := false
	for i := range channels {
		enabled = enabled || channels[i].Enabled
	}
	if !enabled {
		slog.Warn("alert bridge: the operator organization has no enabled notification channel", "organization_id", orgID)
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the operator organization has no enabled notification channel")
		return
	}
	audience := alertAudience
	recipients, err := b.resolver.Resolve(ctx, orgID, &audience)
	if err != nil {
		slog.Error("alert bridge: resolve the recipients of the operator organization", "organization_id", orgID, "error", err)
		api.WriteRepoError(w, err)
		return
	}
	if len(recipients) == 0 {
		slog.Warn("alert bridge: the operator organization has no user with monitoring:write", "organization_id", orgID)
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the operator organization has no recipient for operations alerts")
		return
	}
	req := p.notification()
	req.Recipients = recipients

	queued := 0
	scope := database.OrgWideScope(orgID, "")
	err = database.WithTenant(ctx, b.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var qErr error
		// The notifier's statements join this transaction (same scope).
		if queued, qErr = b.notifier.Notify(database.ContextWithTx(ctx, tx), orgID, req); qErr != nil || queued == 0 {
			return qErr
		}
		_, qErr = audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: orgID, ActorType: "system",
			Action: AlertAction, ResourceType: "notification",
			Changes: map[string]interface{}{"status": p.Status, "alertname": req.Data["alertname"], "critical": req.Critical,
				"alerts": len(p.Alerts), "deliveries": queued, "dedup_key": req.DedupKey}})
		return qErr
	})
	if err != nil {
		slog.Error("alert bridge: queue the notification", "organization_id", orgID, "error", err)
		api.WriteRepoError(w, err)
		return
	}
	receipt := "queued"
	if queued == 0 {
		receipt = "duplicate"
	}
	api.WriteJSON(w, http.StatusAccepted, map[string]any{"notification": receipt, "deliveries": queued})
}

// alertPayload is the webhook payload of Alertmanager, version 4.
type alertPayload struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            alertList         `json:"alerts"`
}

// alertItem is one alert of a payload.
type alertItem struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// alertList decodes at most MaxAlerts alerts: a body of many empty
// objects is refused before it is allocated.
type alertList []alertItem

var errInvalidAlerts = errors.New("invalid Alertmanager payload")

// UnmarshalJSON implements json.Unmarshaler.
func (l *alertList) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		*l = nil
		return nil
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return fmt.Errorf("%w: alerts must be an array", errInvalidAlerts)
	}
	out := alertList{}
	for dec.More() {
		if len(out) == MaxAlerts {
			return fmt.Errorf("%w: at most %d alerts per request", errInvalidAlerts, MaxAlerts)
		}
		var a alertItem
		if decErr := dec.Decode(&a); decErr != nil {
			return decErr
		}
		out = append(out, a)
	}
	*l = out
	return nil
}

// decodeAlerts parses and checks a payload; errInvalidAlerts marks a JSON
// document that is no Alertmanager payload of version 4. The messages name
// fields, never values; Receive answers other errors with a fixed text.
// validation: fixed texts naming the field (and the alert index).
func decodeAlerts(raw []byte) (*alertPayload, error) {
	var p alertPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	switch {
	case p.Version != "4":
		return nil, fmt.Errorf("%w: version must be 4", errInvalidAlerts)
	case p.Status != alertFiring && p.Status != alertResolved:
		return nil, fmt.Errorf("%w: status must be firing or resolved", errInvalidAlerts)
	case strings.TrimSpace(p.GroupKey) == "":
		return nil, fmt.Errorf("%w: groupKey is required", errInvalidAlerts)
	case len(p.Alerts) == 0:
		return nil, fmt.Errorf("%w: alerts must not be empty", errInvalidAlerts)
	case p.TruncatedAlerts < 0:
		return nil, fmt.Errorf("%w: truncatedAlerts must not be negative", errInvalidAlerts)
	}
	for i := range p.Alerts {
		a := &p.Alerts[i]
		if a.Status != alertFiring && a.Status != alertResolved {
			return nil, fmt.Errorf("%w: alerts[%d].status must be firing or resolved", errInvalidAlerts, i)
		}
		if a.StartsAt.IsZero() {
			return nil, fmt.Errorf("%w: alerts[%d].startsAt is required", errInvalidAlerts, i)
		}
	}
	return &p, nil
}

// latestStart is the latest startsAt of the group's alerts: a new alert
// in the group moves it, a repetition does not.
func (p *alertPayload) latestStart() time.Time {
	var latest time.Time
	for i := range p.Alerts {
		if p.Alerts[i].StartsAt.After(latest) {
			latest = p.Alerts[i].StartsAt
		}
	}
	return latest.UTC()
}

// dedupKey identifies the notification: group key, status and the latest
// startsAt, hashed with length prefixes (fixed length, no label value in
// the stored key).
func (p *alertPayload) dedupKey() string {
	h := sha256.New()
	for _, part := range []string{p.GroupKey, p.Status, p.latestStart().Format(time.RFC3339Nano)} {
		h.Write([]byte(strconv.Itoa(len(part))))
		h.Write([]byte{':'})
		h.Write([]byte(part))
	}
	return "alertmanager:" + hex.EncodeToString(h.Sum(nil))
}

// alertName names the group: alertname of the group, else of the first
// alert.
func (p *alertPayload) alertName() string {
	for _, labels := range []map[string]string{p.CommonLabels, p.GroupLabels, p.Alerts[0].Labels} {
		if name := clean(labels["alertname"], 120); name != "" {
			return name
		}
	}
	return "Alertmanager"
}

// notification is the notification of the payload (without recipients):
// subject in German, the text in German and English, the data for webhook
// channels. A firing group with a critical alert is critical.
func (p *alertPayload) notification() *notify.Request {
	name := p.alertName()
	firing, critical := 0, false
	for i := range p.Alerts {
		if p.Alerts[i].Status == alertFiring {
			firing++
			critical = critical || p.Alerts[i].Labels["severity"] == "critical"
		}
	}
	critical = critical && p.Status == alertFiring
	total := len(p.Alerts) + p.TruncatedAlerts

	subject := "Betriebsalarm: " + name
	stateDE, stateEN := "ausgelöst", "firing"
	switch {
	case p.Status == alertResolved:
		subject = "Betriebsalarm behoben: " + name
		stateDE, stateEN = "behoben", "resolved"
	case critical:
		subject = "Betriebsalarm (kritisch): " + name
	}
	if total > 1 {
		subject += fmt.Sprintf(" (%d Alarme)", total)
	}

	var body strings.Builder
	group := labelText(p.GroupLabels, nil)
	if group != "" {
		group = " – " + group
	}
	fmt.Fprintf(&body, "Alertmanager: %d Alarm(e) %s%s\n", total, stateDE, group)
	fmt.Fprintf(&body, "Alertmanager: %d alert(s) %s%s\n", total, stateEN, group)
	for i := range p.Alerts {
		if i == listedAlerts {
			fmt.Fprintf(&body, "\n… %d weitere / %d more", len(p.Alerts)-listedAlerts, len(p.Alerts)-listedAlerts)
			break
		}
		a := &p.Alerts[i]
		line := "\n- " + a.Status + " " + clean(a.Labels["alertname"], 120)
		if own := labelText(a.Labels, p.CommonLabels); own != "" {
			line += " " + own
		}
		body.WriteString(line + ", seit/since " + a.StartsAt.UTC().Format(time.RFC3339))
		for _, key := range []string{"summary", "description"} {
			if text := clean(a.Annotations[key], 500); text != "" {
				body.WriteString("\n  " + text)
				break
			}
		}
	}
	if p.TruncatedAlerts > 0 {
		fmt.Fprintf(&body, "\n… Alertmanager hat %d weitere Alarme ausgelassen / left out %d more alerts", p.TruncatedAlerts, p.TruncatedAlerts)
	}
	if u := clean(p.ExternalURL, 300); u != "" {
		body.WriteString("\n\nAlertmanager: " + u)
	}

	key := p.dedupKey()
	data := map[string]any{
		"source": "alertmanager", "status": p.Status, "alertname": name, "critical": critical,
		"group_key": clean(p.GroupKey, 512), "group_labels": cleanLabels(p.GroupLabels), "common_labels": cleanLabels(p.CommonLabels),
		"alerts": len(p.Alerts), "firing": firing, "resolved": len(p.Alerts) - firing,
		"starts_at": p.latestStart().Format(time.RFC3339), "dedup_key": key,
	}
	if p.TruncatedAlerts > 0 {
		data["truncated_alerts"] = p.TruncatedAlerts
	}
	if u := clean(p.ExternalURL, 512); u != "" {
		data["external_url"] = u
	}
	return &notify.Request{Category: AlertCategory, Critical: critical, Subject: subject, Body: body.String(),
		Data: data, DedupKey: key}
}

// labelText renders labels as "k=v, …", sorted, without alertname and
// without those of except, at most 10.
func labelText(labels, except map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k, v := range labels {
		if k == "alertname" {
			continue
		}
		if common, ok := except[k]; ok && common == v {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, min(len(keys), 10)+1)
	for i, k := range keys {
		if i == 10 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, clean(k, 64)+"="+clean(labels[k], 120))
	}
	return strings.Join(parts, ", ")
}

// cleanLabels bounds labels for the data of a notification: at most
// maxLabels, sorted by key, keys and values cleaned.
func cleanLabels(labels map[string]string) map[string]string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, min(len(keys), maxLabels))
	for _, k := range keys[:min(len(keys), maxLabels)] {
		out[clean(k, 64)] = clean(labels[k], 256)
	}
	return out
}

// clean makes a label or annotation value safe for subject, text and data:
// control and format characters and line breaks (which a mail subject
// refuses) become spaces, runs of white space collapse, and the value is
// cut to max runes.
func clean(s string, maxRunes int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp)
	}), " ")
	if utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes-1]) + "…"
	}
	return s
}

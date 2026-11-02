package operator

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// WP-448 (OPS-05, E-75): the Alertmanager bridge without a database —
// token check, the 404 without operator organization, the bounds of the
// payload, its parsing into the notification and the deduplication key,
// and the receivers of deploy/monitoring/alertmanager.yaml. The delivery
// itself is covered by alerts_integration_test.go.

const (
	testAlertToken = "ops-alert-token-with-at-least-32-chars"
	testAlertOrg   = "6f1c2d3e-4b5a-4c6d-8e7f-0a1b2c3d4e5f"
)

// testAlert is one alert of a test payload.
type testAlert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations,omitempty"`
	StartsAt    string            `json:"startsAt,omitempty"`
	EndsAt      string            `json:"endsAt,omitempty"`
}

// testPayload builds an Alertmanager payload of version 4 for the group
// alertname=ReticoraWorkerStopped, worker=notifier.
func testPayload(status string, alerts ...testAlert) map[string]any {
	return map[string]any{
		"version": "4", "groupKey": `{}/{severity="critical"}:{alertname="ReticoraWorkerStopped", worker="notifier"}`,
		"truncatedAlerts": 0, "status": status, "receiver": "operators-critical",
		"groupLabels":       map[string]string{"alertname": "ReticoraWorkerStopped", "worker": "notifier"},
		"commonLabels":      map[string]string{"alertname": "ReticoraWorkerStopped", "worker": "notifier", "severity": "critical"},
		"commonAnnotations": map[string]string{},
		"externalURL":       "https://alertmanager.example.invalid",
		"alerts":            alerts,
	}
}

func stoppedAlert(status, instance, startsAt string) testAlert {
	return testAlert{Status: status, StartsAt: startsAt,
		Labels:      map[string]string{"alertname": "ReticoraWorkerStopped", "worker": "notifier", "severity": "critical", "instance": instance},
		Annotations: map[string]string{"summary": "Worker notifier stopped on " + instance}}
}

func encode(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func parsePayload(t *testing.T, v any) *alertPayload {
	t.Helper()
	p, err := decodeAlerts([]byte(encode(t, v)))
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return p
}

// alertMux serves the bridge behind the tenant authentication of the
// server, which leaves the operator path alone.
func alertMux(t *testing.T, b *AlertBridge) (http.Handler, *identity.SessionIssuer) {
	t.Helper()
	issuer := testIssuer(t)
	mux := chi.NewRouter()
	b.RegisterRoutes(mux)
	return middleware.AuthMiddlewareWithVerifier(issuer)(mux), issuer
}

func postAlerts(h http.Handler, body io.Reader, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, AlertPath, body)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// TestAlertBridgeAdmitsTheAlertTokenOnly: only RETICORA_OPS_ALERT_TOKEN
// as bearer token passes; a wrong, shortened or extended token, another
// scheme, a tenant session with every permission, an operator session, an
// API key and the break-glass token get 401. Without the operator
// organization the route answers 404 like an unknown one, also with the
// valid token.
func TestAlertBridgeAdmitsTheAlertTokenOnly(t *testing.T) {
	h, issuer := alertMux(t, NewAlertBridge(AlertConfig{Token: testAlertToken, OrgID: testAlertOrg}, NewAuditor(nil)))
	body := encode(t, testPayload(alertFiring, stoppedAlert(alertFiring, "api-1", "2026-10-25T10:00:00Z")))
	now := time.Now().UTC()
	sign := func(c identity.SessionClaims) string {
		t.Helper()
		token, err := issuer.Issue(c)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	tenant := sign(identity.SessionClaims{Subject: "user-1", OrganizationID: testAlertOrg, Permissions: identity.AllPermissions(),
		IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
	operatorSession := sign(identity.SessionClaims{Subject: "op-1", Operator: true, IssuedAt: now, ExpiresAt: now.Add(time.Minute)})

	for name, header := range map[string]map[string]string{
		"no credentials":           nil,
		"wrong token":              bearer(strings.Repeat("x", len(testAlertToken))),
		"token with a suffix":      bearer(testAlertToken + "x"),
		"token without last char":  bearer(testAlertToken[:len(testAlertToken)-1]),
		"basic scheme":             {"Authorization": "Basic " + testAlertToken},
		"token without scheme":     {"Authorization": testAlertToken},
		"empty bearer":             {"Authorization": "Bearer "},
		"tenant session (all)":     bearer(tenant),
		"operator session":         bearer(operatorSession),
		"API key":                  {"X-API-Key": "rk_live_" + strings.Repeat("a", 52)},
		"break-glass header":       {BreakGlassHeader: testBreakGlass},
		"break-glass token bearer": bearer(testBreakGlass),
		"token in another header":  {"X-Alert-Token": testAlertToken},
	} {
		if w := postAlerts(h, strings.NewReader(body), header); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s, want 401", name, w.Code, w.Body.String())
		}
	}
	// The valid token passes; without database the bridge cannot deliver.
	for name, header := range map[string]map[string]string{
		"bearer":                   bearer(testAlertToken),
		"lower-case scheme, blank": {"Authorization": "  bearer   " + testAlertToken + " "},
	} {
		if w := postAlerts(h, strings.NewReader(body), header); w.Code != http.StatusServiceUnavailable ||
			!strings.Contains(w.Body.String(), "needs the database") {
			t.Errorf("%s: %d %s, want 503 (authenticated, no database)", name, w.Code, w.Body.String())
		}
	}

	// Without operator organization (or token) the bridge is off.
	for name, cfg := range map[string]AlertConfig{
		"token without organization": {Token: testAlertToken},
		"organization without token": {OrgID: testAlertOrg},
		"nothing":                    {},
	} {
		off, _ := alertMux(t, NewAlertBridge(cfg, NewAuditor(nil)))
		for _, header := range []map[string]string{bearer(testAlertToken), nil} {
			w := postAlerts(off, strings.NewReader(body), header)
			if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "the requested resource does not exist") {
				t.Errorf("%s (%v): %d %s, want 404", name, header != nil, w.Code, w.Body.String())
			}
		}
	}
	if (AlertConfig{}).authorized(httptest.NewRequest(http.MethodPost, AlertPath, nil)) {
		t.Error("an empty configuration authorizes a request without token")
	}
}

// countingReader counts the bytes read from it.
type countingReader struct {
	r    io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

// TestAlertBridgeBoundsThePayload: the body is read only after the token
// and only up to 1 MiB (413); more than 100 alerts are refused before they
// are decoded (422), 100 pass; a payload that is no Alertmanager webhook of
// version 4 is 422, a document that is no JSON 400.
func TestAlertBridgeBoundsThePayload(t *testing.T) {
	h, _ := alertMux(t, NewAlertBridge(AlertConfig{Token: testAlertToken, OrgID: testAlertOrg}, NewAuditor(nil)))
	auth := bearer(testAlertToken)

	big := &countingReader{r: strings.NewReader(strings.Repeat(" ", 2*MaxAlertBody))}
	if w := postAlerts(h, big, nil); w.Code != http.StatusUnauthorized || big.read != 0 {
		t.Errorf("2 MiB without token: %d, %d bytes read; want 401 before reading", w.Code, big.read)
	}
	big = &countingReader{r: strings.NewReader(strings.Repeat(" ", 2*MaxAlertBody))}
	if w := postAlerts(h, big, auth); w.Code != http.StatusRequestEntityTooLarge || big.read > MaxAlertBody+1 {
		t.Errorf("2 MiB with token: %d, %d bytes read; want 413 after at most %d", w.Code, big.read, MaxAlertBody+1)
	}

	alerts := func(n int) []testAlert {
		out := make([]testAlert, n)
		for i := range out {
			out[i] = stoppedAlert(alertFiring, fmt.Sprintf("api-%d", i), "2026-10-25T10:00:00Z")
		}
		return out
	}
	if w := postAlerts(h, strings.NewReader(encode(t, testPayload(alertFiring, alerts(MaxAlerts)...))), auth); w.Code != http.StatusServiceUnavailable {
		t.Errorf("%d alerts: %d %s, want them parsed (503 without database)", MaxAlerts, w.Code, w.Body.String())
	}
	// Many empty alerts below 1 MiB: refused once the 101st is reached.
	many := `{"version":"4","groupKey":"g","status":"firing","alerts":[` + strings.TrimSuffix(strings.Repeat("{},", 300000), ",") + `]}`
	if len(many) > MaxAlertBody {
		t.Fatalf("fixture of %d bytes is over the body limit", len(many))
	}
	for name, body := range map[string]string{
		"101 alerts":             encode(t, testPayload(alertFiring, alerts(MaxAlerts+1)...)),
		"300000 empty alerts":    many,
		"version 3":              strings.Replace(encode(t, testPayload(alertFiring, alerts(1)...)), `"version":"4"`, `"version":"3"`, 1),
		"no version":             strings.Replace(encode(t, testPayload(alertFiring, alerts(1)...)), `"version":"4"`, `"v":"4"`, 1),
		"status pending":         encode(t, testPayload("pending", alerts(1)...)),
		"empty group key":        strings.Replace(encode(t, testPayload(alertFiring, alerts(1)...)), `"groupKey":"{`, `"groupKey":" ","x":"{`, 1),
		"no alerts":              encode(t, testPayload(alertFiring)),
		"alerts is an object":    `{"version":"4","groupKey":"g","status":"firing","alerts":{}}`,
		"alert status unknown":   encode(t, testPayload(alertFiring, stoppedAlert("unknown", "api-1", "2026-10-25T10:00:00Z"))),
		"alert without startsAt": encode(t, testPayload(alertFiring, stoppedAlert(alertFiring, "api-1", ""))),
		"negative truncation":    strings.Replace(encode(t, testPayload(alertFiring, alerts(1)...)), `"truncatedAlerts":0`, `"truncatedAlerts":-1`, 1),
	} {
		if w := postAlerts(h, strings.NewReader(body), auth); w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s, want 422", name, w.Code, w.Body.String())
		}
	}
	for name, body := range map[string]string{
		"no JSON":            "firing",
		"startsAt not time":  encode(t, testPayload(alertFiring, stoppedAlert(alertFiring, "api-1", "yesterday"))),
		"labels not strings": `{"version":"4","groupKey":"g","status":"firing","alerts":[{"status":"firing","labels":{"a":1},"startsAt":"2026-10-25T10:00:00Z"}]}`,
		"empty body":         "",
	} {
		if w := postAlerts(h, strings.NewReader(body), auth); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
}

var dedupKeyPattern = regexp.MustCompile(`^alertmanager:[0-9a-f]{64}$`)

// TestAlertNotificationOfAGroup: one notification per group and status.
// The deduplication key comes from group key, status and the latest
// startsAt: a repetition (also with the alerts in another order) has the
// same key, the resolution, a new alert of the group and another group a
// new one; no label value is part of it. A firing group with a critical
// alert is critical, a resolved one is not.
func TestAlertNotificationOfAGroup(t *testing.T) {
	a1 := stoppedAlert(alertFiring, "api-1", "2026-10-25T10:00:00Z")
	a2 := stoppedAlert(alertFiring, "api-2", "2026-10-25T10:02:00Z")
	firing := parsePayload(t, testPayload(alertFiring, a1, a2)).notification()
	if firing.Category != AlertCategory || !firing.Critical || firing.Personal {
		t.Errorf("category %q, critical %v, personal %v; want ops_alert, critical, not personal", firing.Category, firing.Critical, firing.Personal)
	}
	if want := "Betriebsalarm (kritisch): ReticoraWorkerStopped (2 Alarme)"; firing.Subject != want {
		t.Errorf("subject %q, want %q", firing.Subject, want)
	}
	for _, want := range []string{
		"Alertmanager: 2 Alarm(e) ausgelöst – worker=notifier", "Alertmanager: 2 alert(s) firing – worker=notifier",
		"- firing ReticoraWorkerStopped instance=api-1, seit/since 2026-10-25T10:00:00Z", "  Worker notifier stopped on api-2",
		"Alertmanager: https://alertmanager.example.invalid",
	} {
		if !strings.Contains(firing.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, firing.Body)
		}
	}
	if !dedupKeyPattern.MatchString(firing.DedupKey) || strings.Contains(firing.DedupKey, "notifier") {
		t.Errorf("dedup key %q", firing.DedupKey)
	}
	if firing.Data["status"] != alertFiring || firing.Data["alerts"] != 2 || firing.Data["firing"] != 2 ||
		firing.Data["starts_at"] != "2026-10-25T10:02:00Z" || firing.Data["dedup_key"] != firing.DedupKey {
		t.Errorf("data %v", firing.Data)
	}

	key := func(status string, alerts ...testAlert) string {
		return parsePayload(t, testPayload(status, alerts...)).notification().DedupKey
	}
	if key(alertFiring, a2, a1) != firing.DedupKey || key(alertFiring, a1, a2) != firing.DedupKey {
		t.Error("a repetition of the group has another dedup key")
	}
	resolvedAlerts := []testAlert{stoppedAlert(alertResolved, "api-1", "2026-10-25T10:00:00Z"), stoppedAlert(alertResolved, "api-2", "2026-10-25T10:02:00Z")}
	resolvedKey := key(alertResolved, resolvedAlerts...)
	newAlert := key(alertFiring, a1, a2, stoppedAlert(alertFiring, "api-3", "2026-10-25T10:07:00Z"))
	other := testPayload(alertFiring, a1, a2)
	other["groupKey"] = `{}/{severity="critical"}:{alertname="ReticoraWorkerStopped", worker="export"}`
	otherGroup := parsePayload(t, other).notification().DedupKey
	seen := map[string]string{firing.DedupKey: "firing"}
	for name, k := range map[string]string{"resolved": resolvedKey, "new alert": newAlert, "other group": otherGroup} {
		if prev, dup := seen[k]; dup {
			t.Errorf("%s has the dedup key of %s", name, prev)
		}
		seen[k] = name
	}

	resolved := parsePayload(t, testPayload(alertResolved, resolvedAlerts...)).notification()
	if resolved.Critical || resolved.Subject != "Betriebsalarm behoben: ReticoraWorkerStopped (2 Alarme)" ||
		!strings.Contains(resolved.Body, "2 alert(s) resolved") {
		t.Errorf("resolved: critical %v, subject %q, body %q", resolved.Critical, resolved.Subject, resolved.Body)
	}
	warning := testPayload(alertFiring, testAlert{Status: alertFiring, StartsAt: "2026-10-25T10:00:00Z",
		Labels: map[string]string{"alertname": "ReticoraWorkerErrors", "severity": "warning"}})
	warning["commonLabels"] = map[string]string{"alertname": "ReticoraWorkerErrors", "severity": "warning"}
	if n := parsePayload(t, warning).notification(); n.Critical || n.Subject != "Betriebsalarm: ReticoraWorkerErrors" {
		t.Errorf("warning: critical %v, subject %q", n.Critical, n.Subject)
	}
}

// TestAlertNotificationCleansValues: label and annotation values reach
// subject, text and data without line breaks or control and format
// characters (a mail subject with a line break would be refused, a header
// could be injected) and cut to a fixed length; the text lists at most 20
// alerts and names the alerts Alertmanager left out; the data carries at
// most 32 labels of a group.
func TestAlertNotificationCleansValues(t *testing.T) {
	evil := testAlert{Status: alertFiring, StartsAt: "2026-10-25T10:00:00Z",
		Labels:      map[string]string{"alertname": "Evil\r\nBcc: attacker@example.invalid\u202e", "instance": "a\x00b\tc"},
		Annotations: map[string]string{"summary": strings.Repeat("s", 1000) + "\nX-Injected: 1"}}
	p := testPayload(alertFiring, evil)
	p["commonLabels"] = map[string]string{"alertname": evil.Labels["alertname"]}
	labels := map[string]string{}
	for i := range 40 {
		labels[fmt.Sprintf("label_%02d", i)] = "v\n" + strings.Repeat("x", 300)
	}
	p["groupLabels"] = labels
	n := parsePayload(t, p).notification()
	if strings.ContainsAny(n.Subject, "\r\n\u202e") || n.Subject != "Betriebsalarm: Evil Bcc: attacker@example.invalid" {
		t.Errorf("subject %q", n.Subject)
	}
	if strings.ContainsAny(n.Body, "\r\x00\u202e") || strings.Contains(n.Body, "\nX-Injected") || strings.Contains(n.Body, "\nBcc") {
		t.Errorf("body keeps a control character or line break of a value:\n%q", n.Body)
	}
	if !strings.Contains(n.Body, "instance=a b c") || !strings.Contains(n.Body, strings.Repeat("s", 499)+"…") ||
		strings.Contains(n.Body, strings.Repeat("s", 500)) {
		t.Errorf("values not cleaned or cut:\n%s", n.Body)
	}
	group, _ := n.Data["group_labels"].(map[string]string)
	if len(group) != maxLabels {
		t.Errorf("data carries %d group labels, want %d", len(group), maxLabels)
	}
	for k, v := range group {
		if strings.ContainsAny(v, "\n") || utf8.RuneCountInString(v) > 256 {
			t.Errorf("group label %s=%q not cleaned", k, v)
		}
	}

	many := make([]testAlert, 30)
	for i := range many {
		many[i] = stoppedAlert(alertFiring, fmt.Sprintf("api-%02d", i), "2026-10-25T10:00:00Z")
	}
	q := testPayload(alertFiring, many...)
	q["truncatedAlerts"] = 5
	n = parsePayload(t, q).notification()
	if got := strings.Count(n.Body, "\n- firing "); got != listedAlerts {
		t.Errorf("text lists %d alerts, want %d", got, listedAlerts)
	}
	for _, want := range []string{"… 10 weitere / 10 more", "Alertmanager hat 5 weitere Alarme ausgelassen / left out 5 more alerts", "35 Alarm(e)"} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	if !strings.HasSuffix(n.Subject, "(35 Alarme)") || n.Data["truncated_alerts"] != 5 {
		t.Errorf("subject %q, truncated %v", n.Subject, n.Data["truncated_alerts"])
	}
}

// TestAlertmanagerConfigDeliversToTheBridge: every receiver of the route
// tree of deploy/monitoring/alertmanager.yaml keeps its e-mail (E-75: the
// delivery while the bridge answers 404) and posts to the bridge with
// resolved notifications, at most MaxAlerts alerts and the token from a
// credentials file; no receiver writes a token or password into the file.
func TestAlertmanagerConfigDeliversToTheBridge(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/monitoring/alertmanager.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type route struct {
		Receiver string  `yaml:"receiver"`
		Routes   []route `yaml:"routes"`
	}
	var cfg struct {
		Route     route `yaml:"route"`
		Receivers []struct {
			Name     string           `yaml:"name"`
			Email    []map[string]any `yaml:"email_configs"`
			Webhooks []struct {
				URL          string `yaml:"url"`
				SendResolved bool   `yaml:"send_resolved"`
				MaxAlerts    int    `yaml:"max_alerts"`
				HTTPConfig   struct {
					Authorization map[string]string `yaml:"authorization"`
					BasicAuth     map[string]string `yaml:"basic_auth"`
					BearerToken   string            `yaml:"bearer_token"`
				} `yaml:"http_config"`
			} `yaml:"webhook_configs"`
		} `yaml:"receivers"`
	}
	if err = yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	var walk func(r route)
	walk = func(r route) {
		if r.Receiver != "" {
			used[r.Receiver] = true
		}
		for _, child := range r.Routes {
			walk(child)
		}
	}
	walk(cfg.Route)
	if len(used) < 2 {
		t.Fatalf("route tree names receivers %v", used)
	}
	for _, rcv := range cfg.Receivers {
		for _, wh := range rcv.Webhooks {
			auth := wh.HTTPConfig.Authorization
			if auth["credentials"] != "" || wh.HTTPConfig.BearerToken != "" || wh.HTTPConfig.BasicAuth["password"] != "" {
				t.Errorf("receiver %s: a webhook writes its credentials into the file", rcv.Name)
			}
		}
		if !used[rcv.Name] {
			continue
		}
		delete(used, rcv.Name)
		if len(rcv.Email) == 0 {
			t.Errorf("receiver %s: no e-mail, nothing delivers while the bridge answers 404 (E-75)", rcv.Name)
		}
		bridges := 0
		for _, wh := range rcv.Webhooks {
			if !strings.HasSuffix(wh.URL, AlertPath) {
				continue
			}
			bridges++
			auth := wh.HTTPConfig.Authorization
			if !wh.SendResolved || wh.MaxAlerts < 1 || wh.MaxAlerts > MaxAlerts ||
				!strings.EqualFold(auth["type"], "Bearer") || auth["credentials_file"] == "" {
				t.Errorf("receiver %s: bridge webhook send_resolved %v, max_alerts %d, authorization %v; want resolved, 1..%d alerts, "+
					"Bearer from a credentials_file", rcv.Name, wh.SendResolved, wh.MaxAlerts, auth, MaxAlerts)
			}
		}
		if bridges != 1 {
			t.Errorf("receiver %s posts to the bridge %d times, want once", rcv.Name, bridges)
		}
	}
	if len(used) > 0 {
		t.Errorf("route tree names undefined receivers %v", used)
	}
}

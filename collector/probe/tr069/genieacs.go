package tr069

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GenieACS-NBI reference adapter (WP-1018 AK3): GET /devices/ with a query
// built from the structured selector, a projection of the needed fields
// and skip/limit paging.

// MaxPageBytes bounds the body of one page (max_payload_bytes of the
// type); a larger page is discarded as a whole.
const MaxPageBytes = 1 << 20

// ErrPageTooLarge is returned for a page above MaxPageBytes.
var ErrPageTooLarge = errors.New("tr069: ACS page exceeds 1 MiB")

// ErrAuth is returned when the ACS refuses the credential (401, 403).
var ErrAuth = errors.New("tr069: ACS refused the credential")

// StatusError is an ACS answer with a status other than 200, 401 and 403
// (a redirect included: it is not followed).
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("tr069: ACS answered HTTP %d", e.Code) }

// genieACS reads the GenieACS northbound interface.
type genieACS struct {
	base   *url.URL
	client *http.Client
	auth   string
}

var _ Reader = (*genieACS)(nil)

// newGenieACS returns the adapter for the NBI at base (scheme, host and an
// optional path prefix); auth is the Authorization header value or empty.
func newGenieACS(base *url.URL, client *http.Client, auth string) *genieACS {
	return &genieACS{base: base, client: client, auth: auth}
}

// selectorQuery builds the MongoDB-style query of GenieACS from the
// selector: exact matches on the device id fields and the tag list. The
// values go through json.Marshal, so quotes, backslashes and control
// characters are escaped; no operator can be injected because every value
// is a JSON string.
func selectorQuery(s Selector) (string, error) {
	q := map[string]string{}
	if s.Serial != "" {
		q["_deviceId._SerialNumber"] = s.Serial
	}
	if s.OUI != "" {
		q["_deviceId._OUI"] = s.OUI
	}
	if s.ProductClass != "" {
		q["_deviceId._ProductClass"] = s.ProductClass
	}
	if s.Tag != "" {
		q["_tags"] = s.Tag
	}
	raw, err := json.Marshal(q) // map keys are sorted
	return string(raw), err
}

// pageURL returns the URL of one page.
func (g *genieACS) pageURL(selector Selector, parameters []string, page Page) (string, error) {
	query, err := selectorQuery(selector)
	if err != nil {
		return "", err
	}
	projection := append([]string{"_id", "_lastInform", "_deviceId"}, parameters...)
	u := *g.base
	u.Path = strings.TrimSuffix(u.Path, "/") + "/devices/"
	u.RawQuery = url.Values{
		"query":      {query},
		"projection": {strings.Join(projection, ",")},
		"skip":       {strconv.Itoa(page.Skip)},
		"limit":      {strconv.Itoa(page.Limit)},
	}.Encode()
	return u.String(), nil
}

// ListDevices implements Reader.
func (g *genieACS) ListDevices(ctx context.Context, selector Selector, parameters []string, page Page) ([]Device, error) {
	target, err := g.pageURL(selector, parameters, page)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if g.auth != "" {
		req.Header.Set("Authorization", g.auth)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrAuth
	default:
		return nil, &StatusError{Code: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxPageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxPageBytes {
		return nil, ErrPageTooLarge
	}
	var docs []map[string]any
	if err := json.Unmarshal(body, &docs); err != nil {
		return nil, fmt.Errorf("tr069: ACS answer is no device list: %w", err)
	}
	out := make([]Device, 0, len(docs))
	for _, doc := range docs {
		out = append(out, deviceOf(doc, parameters))
	}
	return out, nil
}

// deviceOf reads a GenieACS device document.
func deviceOf(doc map[string]any, parameters []string) Device {
	d := Device{Values: map[string]any{}}
	d.ID, _ = doc["_id"].(string)
	if id, ok := doc["_deviceId"].(map[string]any); ok {
		d.Serial, _ = id["_SerialNumber"].(string)
	}
	if d.Serial == "" {
		d.Serial = d.ID
	}
	if s, ok := doc["_lastInform"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			d.LastInform = t
		}
	}
	for _, path := range parameters {
		if v, ok := lookup(doc, path); ok {
			d.Values[path] = v
		}
	}
	return d
}

// lookup returns <path>._value of a device document.
func lookup(doc map[string]any, path string) (any, bool) {
	var node any = doc
	for _, part := range strings.Split(path, ".") {
		m, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		node = m[part]
	}
	m, ok := node.(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := m["_value"]
	return v, ok && v != nil
}

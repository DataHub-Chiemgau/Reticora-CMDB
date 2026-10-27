package tenantapi

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // IANA zone names validate without the host's zoneinfo

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"golang.org/x/text/language"
)

// FieldError and ConflictError are the shared field errors of the API: 400
// and 409 naming the field (LOC-01–LOC-06).
type (
	FieldError    = api.FieldError
	ConflictError = api.ConflictError
)

// validTimezone checks an IANA time zone name (LOC-01–LOC-03). "UTC" is a
// name of the tz database; "Local" and abbreviations are not.
func validTimezone(field, tz string) error {
	if tz == "" || tz == "Local" || !strings.Contains(tz, "/") && tz != "UTC" {
		return &FieldError{Field: field, Message: fmt.Sprintf("%q is not an IANA time zone (e.g. Europe/Berlin)", tz)}
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return &FieldError{Field: field, Message: fmt.Sprintf("%q is not an IANA time zone (e.g. Europe/Berlin)", tz)}
	}
	return nil
}

// validOptionalTimezone accepts an empty value (no own time zone).
func validOptionalTimezone(field string, tz *string) error {
	if tz == nil || *tz == "" {
		return nil
	}
	return validTimezone(field, *tz)
}

// validCurrency checks an active ISO 4217 alphabetic code (LOC-01).
func validCurrency(code string) error {
	if !iso4217[code] {
		return &FieldError{Field: "currency", Message: fmt.Sprintf("%q is not an ISO 4217 currency code (e.g. EUR)", code)}
	}
	return nil
}

// canonicalLocale checks a BCP 47 language tag and returns its canonical
// form ("de-de" → "de-DE"; LOC-01).
func canonicalLocale(tag string) (string, error) {
	t, err := language.Parse(tag)
	if err != nil || tag == "" || t == language.Und {
		return "", &FieldError{Field: "locale", Message: fmt.Sprintf("%q is not a BCP 47 language tag (e.g. de-DE)", tag)}
	}
	return t.String(), nil
}

// layoutKeys returns the object ids of a room layout in a fixed order, so
// the first refused entry is the same on every request.
func layoutKeys(layout map[string]LayoutPosition) []string {
	keys := make([]string, 0, len(layout))
	for k := range layout {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// validateLayoutRange checks the normalized coordinates of a room layout:
// 0 ≤ x, y ≤ 1, otherwise 422 naming layout.<ci>.x|y (F-341).
func validateLayoutRange(layout map[string]LayoutPosition) error {
	for _, k := range layoutKeys(layout) {
		p := layout[k]
		for _, c := range []struct {
			axis string
			v    float64
		}{{"x", p.X}, {"y", p.Y}} {
			if math.IsNaN(c.v) || c.v < 0 || c.v > 1 {
				return &FieldError{Field: "layout." + k + "." + c.axis, Status: http.StatusUnprocessableEntity,
					Message: "the coordinate must lie between 0 and 1"}
			}
		}
	}
	return nil
}

// unknownLayoutCI refuses a layout key that is no CI of the organization
// visible to the caller (422 naming layout.<ci>, F-341).
func unknownLayoutCI(key string) error {
	return &FieldError{Field: "layout." + key, Status: http.StatusUnprocessableEntity,
		Message: "the key is not a configuration item of the organization"}
}

// iso4217 lists the active alphabetic codes of ISO 4217 (list one,
// including the special codes for gold, SDR and testing purposes).
var iso4217 = func() map[string]bool {
	codes := strings.Fields(`
AED AFN ALL AMD ANG AOA ARS AUD AWG AZN BAM BBD BDT BGN BHD BIF BMD BND BOB BOV BRL BSD BTN BWP BYN BZD
CAD CDF CHE CHF CHW CLF CLP CNY COP COU CRC CUC CUP CVE CZK DJF DKK DOP DZD EGP ERN ETB EUR FJD FKP GBP
GEL GHS GIP GMD GNF GTQ GYD HKD HNL HTG HUF IDR ILS INR IQD IRR ISK JMD JOD JPY KES KGS KHR KMF KPW KRW
KWD KYD KZT LAK LBP LKR LRD LSL LYD MAD MDL MGA MKD MMK MNT MOP MRU MUR MVR MWK MXN MXV MYR MZN NAD NGN
NIO NOK NPR NZD OMR PAB PEN PGK PHP PKR PLN PYG QAR RON RSD RUB RWF SAR SBD SCR SDG SEK SGD SHP SLE SLL
SOS SRD SSP STN SVC SYP SZL THB TJS TMT TND TOP TRY TTD TWD TZS UAH UGX USD USN UYI UYU UYW UZS VED VES
VND VUV WST XAF XAG XAU XBA XBB XBC XBD XCD XCG XDR XOF XPD XPF XPT XSU XTS XUA XXX YER ZAR ZMW ZWG ZWL`)
	m := make(map[string]bool, len(codes))
	for _, c := range codes {
		m[c] = true
	}
	return m
}()

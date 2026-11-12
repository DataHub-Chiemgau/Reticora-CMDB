package identity

import (
	"slices"
	"strings"
)

// MFAMethods are the amr values (RFC 8176) that mean more than one factor.
var MFAMethods = []string{"mfa", "otp", "totp", "hwk", "swk", "webauthn", "sms"}

// DefaultMFAACRValues are the acr values that prove a second factor when
// no others are configured: "2", the Keycloak level of a second factor.
var DefaultMFAACRValues = []string{"2"}

// MFASatisfied reports whether a login used a second factor: amr contains
// one of MFAMethods, or acr is one of acrValues (DefaultMFAACRValues when
// none are given). The operator login (SEC-07) and the session claim mfa of
// the tenant login (WP-1152) share the rule.
func MFASatisfied(amr []string, acr string, acrValues ...string) bool {
	for _, m := range amr {
		if slices.Contains(MFAMethods, strings.ToLower(m)) {
			return true
		}
	}
	if len(acrValues) == 0 {
		acrValues = DefaultMFAACRValues
	}
	return acr != "" && slices.Contains(acrValues, acr)
}

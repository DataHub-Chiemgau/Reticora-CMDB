package middleware

import "net/http"

// SecurityHeaders adds the baseline HTTP response headers that protect the
// API and the hosted SPA against content sniffing, clickjacking, referrer
// leakage and downgrade attacks. HSTS is only sent on HTTPS requests (or
// when the platform terminates TLS in front, signalled via
// X-Forwarded-Proto), because advertising it over plain HTTP would be
// meaningless and would break local development.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'self'")
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

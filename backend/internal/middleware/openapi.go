package middleware

import (
	"net/http"
)

// OpenAPIValidation is a placeholder middleware for OpenAPI request validation.
// In production, this should use kin-openapi or oapi-codegen generated validators
// to validate requests against api/openapi.yaml.
//
// The spec requires: "oapi-codegen für Code und Request-Validierung"
// This validates request bodies, path/query parameters, and content types
// against the OpenAPI 3.1 specification.
func OpenAPIValidation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TODO: Load and cache OpenAPI spec from api/openapi.yaml
		// TODO: Validate request against spec using kin-openapi/openapi3filter
		// TODO: On validation failure, return 400 with problem type "validation-error"
		//
		// Implementation sketch:
		// 1. Parse openapi.yaml at startup
		// 2. Create a router from the spec
		// 3. For each request, find the matching route
		// 4. Validate path params, query params, headers, request body
		// 5. On failure: httpx.ValidationError(w, r, detail)
		next.ServeHTTP(w, r)
	})
}

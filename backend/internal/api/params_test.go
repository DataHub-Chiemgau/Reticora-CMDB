package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestIsUUID(t *testing.T) {
	for value, want := range map[string]bool{
		"0b0d4e7c-1f2a-4c3d-8e9f-a0b1c2d3e4f5":    true,
		"0B0D4E7C-1F2A-4C3D-8E9F-A0B1C2D3E4F5":    true,
		"":                                        false,
		"not-a-uuid":                              false,
		"0b0d4e7c1f2a4c3d8e9fa0b1c2d3e4f5":        false,
		"0b0d4e7c-1f2a-4c3d-8e9f-a0b1c2d3e4fg":    false,
		"0b0d4e7c-1f2a-4c3d-8e9f-a0b1c2d3e4f\x00": false,
		"{0b0d4e7c-1f2a-4c3d-8e9f-a0b1c2d3e4f5}":  false,
	} {
		if got := IsUUID(value); got != want {
			t.Errorf("IsUUID(%q) = %v, want %v", value, got, want)
		}
	}
}

// TestParseUUIDParam: a malformed path id is answered before any SQL, 400
// or 404 with the caller's detail (API-03, F-047).
func TestParseUUIDParam(t *testing.T) {
	const valid = "0b0d4e7c-1f2a-4c3d-8e9f-a0b1c2d3e4f5"
	for _, c := range []struct {
		name, id, notFound string
		wantOK             bool
		wantStatus         int
		wantDetail         string
	}{
		{"valid id", valid, "", true, http.StatusOK, ""},
		{"malformed id, 400", "not-a-uuid", "", false, http.StatusBadRequest, "the path parameter id must be a UUID"},
		{"malformed id, 404", "not-a-uuid", "team not found", false, http.StatusNotFound, "team not found"},
		{"NUL byte", "a%00b", "", false, http.StatusBadRequest, "the path parameter id must be a UUID"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got string
			var ok bool
			mux := chi.NewRouter()
			mux.Get("/things/{id}", func(w http.ResponseWriter, r *http.Request) {
				got, ok = ParseUUIDParam(w, r, "id", c.notFound)
				if ok {
					w.WriteHeader(http.StatusOK)
				}
			})
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/things/"+c.id, nil))
			if ok != c.wantOK || w.Code != c.wantStatus {
				t.Fatalf("ParseUUIDParam: ok %v status %d (%s), want %v %d", ok, w.Code, w.Body.String(), c.wantOK, c.wantStatus)
			}
			if ok {
				if got != valid {
					t.Fatalf("ParseUUIDParam returned %q, want %q", got, valid)
				}
				return
			}
			var problem ProblemDetail
			if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || problem.Detail != c.wantDetail {
				t.Fatalf("problem %+v (%v), want detail %q", problem, err, c.wantDetail)
			}
		})
	}
}

// TestRequireUUID: a body field that is no UUID is a 422 field error.
func TestRequireUUID(t *testing.T) {
	if err := RequireUUID("ci_id", "0b0d4e7c-1f2a-4c3d-8e9f-a0b1c2d3e4f5"); err != nil {
		t.Fatalf("valid UUID rejected: %v", err)
	}
	for _, value := range []string{"", "not-a-uuid", "x\x00y"} {
		err := RequireUUID("ci_id", value)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != "ci_id" || fe.Status != http.StatusUnprocessableEntity {
			t.Fatalf("RequireUUID(%q) = %#v, want a 422 field error for ci_id", value, err)
		}
		w := httptest.NewRecorder()
		if !WriteFieldError(w, err) || w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"field":"ci_id"`) {
			t.Fatalf("WriteFieldError: %d %s", w.Code, w.Body.String())
		}
	}
}

// TestRejectNUL: PostgreSQL cannot store NUL in text; the request is a 400
// field error instead of SQLSTATE 22021.
func TestRejectNUL(t *testing.T) {
	if err := RejectNUL("name", "plain", "", "ümlaut"); err != nil {
		t.Fatalf("values without NUL rejected: %v", err)
	}
	if err := RejectNUL("name"); err != nil {
		t.Fatalf("no values rejected: %v", err)
	}
	err := RejectNUL("priorities", "manual", "x\x00y")
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != "priorities" || fe.Status != 0 {
		t.Fatalf("RejectNUL = %#v, want a 400 field error for priorities", err)
	}
	w := httptest.NewRecorder()
	if !WriteFieldError(w, err) || w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "\\u0000") {
		t.Fatalf("WriteFieldError: %d %s", w.Code, w.Body.String())
	}
}

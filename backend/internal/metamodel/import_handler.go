package metamodel

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// MaxImportBody bounds the body of POST /api/v1/metamodel/import (413
// above it).
const MaxImportBody = 10 << 20

// Problem types of the import.
const (
	ProblemUnsupportedFormatVersion = "urn:reticora:problem:unsupported-format-version"
	ProblemImportConflict           = "urn:reticora:problem:metamodel-import-conflict"
)

// ImportHandler serves POST /api/v1/metamodel/import (WP-722, MET-54): a
// metamodel export v2 becomes a draft of the organization. The route needs
// citype:manage (server/authz.go); every item needs the write permission
// of its resource route as well (ItemPermission).
type ImportHandler struct {
	target ImportTargetReader
	store  ImportDraftStore
}

// NewImportHandler returns the import handler; without the target reader
// or the draft store (no database) the route answers 503.
func NewImportHandler(target ImportTargetReader, store ImportDraftStore) *ImportHandler {
	return &ImportHandler{target: target, store: store}
}

// RegisterRoutes registers the import route.
func (h *ImportHandler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/metamodel/import", h.Import)
}

// ImportResult is the answer of the import: the draft (null when nothing
// differs) and the report.
type ImportResult struct {
	DraftID *string      `json:"draft_id"`
	Report  ImportReport `json:"report"`
}

// errUnsupportedFormat refuses a file of another format version (422).
var errUnsupportedFormat = errors.New("only a metamodel export with format_version 2 can be imported")

// decodeImport reads the export v2 of the body: the format version first
// (another one is errUnsupportedFormat), then the document strictly like
// api.ReadJSON (an unknown field is an error).
// validation: the texts of encoding/json about the request body, like
// api.ReadJSON, and errUnsupportedFormat.
func decodeImport(body []byte) (*citype.MetamodelExportV2, error) {
	var probe struct {
		FormatVersion json.RawMessage `json:"format_version"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(probe.FormatVersion)) != "2" {
		return nil, errUnsupportedFormat
	}
	var doc citype.MetamodelExportV2
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("the body must be a single JSON document")
	}
	return &doc, nil
}

// Import handles POST /api/v1/metamodel/import: the body (at most 10 MiB,
// 413) is a metamodel export with format_version 2 (another version 422
// unsupported-format-version, an unknown field 400). The state of the
// organization is read in one snapshot and compared key by key; conflicts
// answer 422 with the report and create no draft, a file without
// differences 200 with report.unchanged, a caller without the permission of
// an item 403. Otherwise the items are captured into a new draft (one
// transaction) and the answer is 201 with draft_id, Location and the
// report.
func (h *ImportHandler) Import(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.target == nil || h.store == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the metamodel import needs the database")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxImportBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			api.WriteError(w, http.StatusRequestEntityTooLarge, "Payload Too Large", "the import file exceeds 10 MiB")
			return
		}
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "the request body could not be read")
		return
	}
	doc, err := decodeImport(body)
	if errors.Is(err, errUnsupportedFormat) {
		api.WriteProblem(w, http.StatusUnprocessableEntity, ProblemUnsupportedFormatVersion, "Unsupported Format Version", err.Error())
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	target, err := h.target.ReadImportTarget(r.Context(), t.OrganizationID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	plan := planImport(doc, target)
	if len(plan.report.Conflicts) > 0 {
		api.WriteProblemBody(w, http.StatusUnprocessableEntity, map[string]any{
			"type": ProblemImportConflict, "title": "Unprocessable Entity", "status": http.StatusUnprocessableEntity,
			"detail": "the import file conflicts with the metamodel of the organization; no draft was created",
			"report": plan.report,
		})
		return
	}
	if len(plan.items) == 0 {
		api.WriteJSON(w, http.StatusOK, ImportResult{Report: plan.report})
		return
	}
	if missing := missingPermissions(r, plan.items); len(missing) > 0 {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "the import needs the permissions "+strings.Join(missing, ", "))
		return
	}
	name := "Import " + time.Now().UTC().Format(time.RFC3339)
	d, err := h.store.CreateImportDraft(r.Context(), t.OrganizationID, name, plan.items)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/metamodel/drafts/"+d.ID)
	w.Header().Set("ETag", api.ETag(d.Version))
	api.WriteJSON(w, http.StatusCreated, ImportResult{DraftID: &d.ID, Report: plan.report})
}

// missingPermissions returns the write permissions of the items the caller
// lacks, sorted.
func missingPermissions(r *http.Request, items []ImportItem) []string {
	perms, _ := identity.PermissionsFromContext(r.Context())
	seen := map[string]bool{}
	var out []string
	for i := range items {
		var payload json.RawMessage
		if items[i].Payload != nil {
			payload, _ = json.Marshal(items[i].Payload)
		}
		for _, need := range ItemPermissions(items[i].Kind, items[i].Op, payload) {
			if seen[string(need)] || identity.HasPermission(perms, need) {
				continue
			}
			seen[string(need)] = true
			out = append(out, string(need))
		}
	}
	sort.Strings(out)
	return out
}

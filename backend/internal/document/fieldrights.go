package document

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// Field permissions of documents (RBA-07, WP-267, finding F-1 of WP-326):
// the answers of /api/v1/documents carry only the fields the field
// permissions of the object kind document let the principal read
// (permission.Project with the core fields of the kind), a write of a
// field it may not edit is 403 with the fields, like asset, contract and
// ticket. Title, version, scan state and the timestamps are structural.

// policyOf returns the field policy of the request or answers 500 (policy
// not resolvable, fail-closed).
func policyOf(w http.ResponseWriter, r *http.Request) (*permission.FieldPolicy, bool) {
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return nil, false
	}
	return policy, true
}

// Ref is the object reference of a document for its field permissions: a
// document has no client or site of its own (its links scope it).
func Ref(d *Document) permission.ObjectRef {
	return permission.ObjectRef{ID: d.ID}
}

// Project returns the document as the policy lets the principal read it:
// the document itself without a restriction.
func Project(policy *permission.FieldPolicy, d *Document) (any, error) {
	return permission.ProjectValue(policy, permission.KindDocument, "", Ref(d), d)
}

// ProjectList returns the documents as the policy lets the principal read
// them; nil when the policy does not restrict documents, the caller then
// answers the documents unchanged.
func ProjectList(policy *permission.FieldPolicy, docs []Document) ([]any, error) {
	if !permission.NeedsProjection(policy, permission.KindDocument) {
		return nil, nil
	}
	out := make([]any, len(docs))
	for i := range docs {
		body, err := Project(policy, &docs[i])
		if err != nil {
			return nil, err
		}
		out[i] = body
	}
	return out, nil
}

// Readable reports whether the principal may read the document field at
// the DTO path.
func Readable(policy *permission.FieldPolicy, d *Document, path string) bool {
	return permission.Readable(policy, permission.KindDocument, "", Ref(d), path)
}

// writeDocument answers the projected document.
func writeDocument(w http.ResponseWriter, status int, policy *permission.FieldPolicy, d *Document) {
	body, err := Project(policy, d)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, status, body)
}

// writeDocumentList answers a list page of documents, projected when the
// policy restricts documents.
func writeDocumentList(w http.ResponseWriter, policy *permission.FieldPolicy, list *api.ListResponse[Document]) {
	data, err := ProjectList(policy, list.Data)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if data == nil {
		api.WriteJSON(w, http.StatusOK, list)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[any]{Data: data, Total: list.Total, Limit: list.Limit,
		Offset: list.Offset, HasMore: list.HasMore, NextCursor: list.NextCursor})
}

// sentFields are the DTO paths a create request sets.
func (req *CreateRequest) sentFields() []string {
	var out []string
	if req.Description != "" {
		out = append(out, "description")
	}
	if req.FileName != "" {
		out = append(out, "file_name")
	}
	if req.FileSize != 0 {
		out = append(out, "file_size")
	}
	if req.MimeType != "" {
		out = append(out, "mime_type")
	}
	if req.Category != "" {
		out = append(out, "category")
	}
	if req.Tags != nil {
		out = append(out, "tags")
	}
	return out
}

// checkUpdate checks a PATCH against the field permissions (pattern of
// contract and asset): every sent field the principal may not read counts,
// whatever its value (no equality oracle), and every readable field whose
// value changes; each must be editable.
func checkUpdate(policy *permission.FieldPolicy, current *Document, req *UpdateRequest) error {
	if !policy.Restricted(permission.KindDocument) {
		return nil
	}
	var changed []string
	consider := func(path string, differs bool) {
		if !Readable(policy, current, path) || differs {
			changed = append(changed, path)
		}
	}
	if req.Title != nil {
		consider("title", *req.Title != current.Title)
	}
	if req.Description != nil {
		consider("description", *req.Description != current.Description)
	}
	if req.Category != nil {
		consider("category", *req.Category != current.Category)
	}
	if req.Tags != nil {
		consider("tags", !slices.Equal(req.Tags, current.Tags))
	}
	return permission.CheckWrite(policy, permission.KindDocument, "", Ref(current), changed)
}

// checkUpload checks a new version against the field permissions: the
// document mirrors size and MIME type of its newest version, so an upload
// writes file_size and mime_type (pattern of checkUpdate).
func checkUpload(policy *permission.FieldPolicy, current *Document, size int64, mimeType string) error {
	if !policy.Restricted(permission.KindDocument) {
		return nil
	}
	var changed []string
	if !Readable(policy, current, "file_size") || size != current.FileSize {
		changed = append(changed, "file_size")
	}
	if !Readable(policy, current, "mime_type") || mimeType != current.MimeType {
		changed = append(changed, "mime_type")
	}
	return permission.CheckWrite(policy, permission.KindDocument, "", Ref(current), changed)
}

// versionFields maps the members of a version onto the document fields
// they mirror.
var versionFields = map[string]string{"size": "file_size", "mime_type": "mime_type", "uploaded_by": "uploaded_by"}

// projectVersion returns the version without the members whose document
// field the principal may not read; the version itself without a
// restriction.
func projectVersion(policy *permission.FieldPolicy, doc *Document, v *Version) (any, error) {
	if !policy.Restricted(permission.KindDocument) {
		return v, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var body map[string]json.RawMessage
	if decodeErr := json.Unmarshal(raw, &body); decodeErr != nil {
		return nil, decodeErr
	}
	for member, field := range versionFields {
		if !Readable(policy, doc, field) {
			delete(body, member)
		}
	}
	return body, nil
}

// listFieldPaths are the DTO paths the list query reads: the category
// filter and sort, and the text search, which also matches file_name.
func listFieldPaths(filter *FilterParams) []string {
	var paths []string
	if filter.Category != "" {
		paths = append(paths, "category")
	}
	if filter.Search != "" {
		paths = append(paths, "file_name")
	}
	if column, _ := NormalizeSort(filter); column == "category" {
		paths = append(paths, "category")
	}
	return paths
}

// downloadName is the file name of a download: the document's own, or its
// id when the principal may not read file_name.
func downloadName(policy *permission.FieldPolicy, doc *Document) string {
	if Readable(policy, doc, "file_name") {
		return doc.FileName
	}
	return doc.ID
}

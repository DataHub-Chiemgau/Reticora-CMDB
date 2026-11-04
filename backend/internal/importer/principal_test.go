package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// TestStarterRefRoundTrip: the job parameters carry the identity
// reference of the starter (type, subject, key prefix of an API key) and
// the request and correlation id of the start through JSON, and nothing
// of its rights, scopes, client scope or groups (E-145, WP-779).
func TestStarterRefRoundTrip(t *testing.T) {
	all := identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	for _, tc := range []struct {
		name      string
		principal identity.Principal
		want      StarterRef
	}{
		{"api key", identity.Principal{Subject: testUser, OrganizationID: testOrg, Type: identity.PrincipalTypeAPIKey,
			Credential: "apikey:Pfx779abcdef", ClientScope: "c-1", Scope: &all, ServiceAccountID: "sa-1",
			Permissions:      []identity.Permission{identity.PermImportRun, identity.PermCIWrite},
			PermissionScopes: map[identity.Permission]identity.Scope{identity.PermCIWrite: {Clients: identity.ScopeSet{IDs: []string{"c-1"}}}}},
			StarterRef{Type: identity.PrincipalTypeAPIKey, Subject: testUser, KeyPrefix: "Pfx779abcdef", CorrelationID: "corr-779"}},
		{"session", identity.Principal{Subject: testUser, OrganizationID: testOrg, Type: identity.PrincipalTypeUser,
			Credential: "session:sid-1", Scope: &all, Groups: []string{"cmdb-admins"}, Permissions: []identity.Permission{identity.PermImportRun}},
			StarterRef{Type: identity.PrincipalTypeUser, Subject: testUser, CorrelationID: "corr-779"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := audit.WithRequestInfo(context.Background(), audit.RequestInfo{RequestID: "req-779", CorrelationID: "corr-779", Source: audit.SourceAPI})
			ctx = identity.WithPrincipal(ctx, tc.principal)
			params := JobParams{DocumentID: "d", DocumentVersion: 1, ProfileID: "p", Mode: "both", Starter: captureStarter(ctx),
				ScopeParams: jobs.CaptureScope(ctx)}
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			var back JobParams
			if err = json.Unmarshal(raw, &back); err != nil {
				t.Fatal(err)
			}
			if back.Starter == nil || *back.Starter != tc.want {
				t.Fatalf("starter %+v, want %+v (%s)", back.Starter, tc.want, raw)
			}
			if back.RequestID != "req-779" || back.Source != audit.SourceAPI {
				t.Fatalf("request facts %+v", back.ScopeParams)
			}
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			var starter map[string]json.RawMessage
			if err = json.Unmarshal(fields["starter"], &starter); err != nil {
				t.Fatal(err)
			}
			for key := range starter {
				if key != "type" && key != "subject" && key != "key_prefix" && key != "correlation_id" {
					t.Errorf("the starter reference holds %s: %s", key, raw)
				}
			}
			for _, leak := range []string{"permission", "scope", "group", "client", string(identity.PermCIWrite), "cmdb-admins", "sa-1", "sid-1"} {
				if strings.Contains(string(raw), leak) {
					t.Errorf("the job parameters hold %q: %s", leak, raw)
				}
			}
		})
	}
	if ref := captureStarter(context.Background()); ref != nil {
		t.Fatalf("starter without principal: %+v", ref)
	}
}

// TestNarrowScopeEncoding: the scope snapshot of the job row bounds the
// reloaded scope; nil in a dimension keeps what is held (all), an empty
// list grants none (E-08), ids keep those the held scope covers.
func TestNarrowScopeEncoding(t *testing.T) {
	held := identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{IDs: []string{"s-1", "s-2"}}, Teams: identity.ScopeSet{All: true}}
	got := narrowScope(&held, &jobs.Scope{Clients: nil, Sites: []string{"s-2", "s-3"}, Teams: []string{}})
	want := identity.Scope{Clients: identity.ScopeSet{All: true, IDs: nil}, Sites: identity.ScopeSet{IDs: []string{"s-2"}}, Teams: identity.ScopeSet{IDs: []string{}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("narrowed %+v, want %+v", got, want)
	}
	if s := dbSet(got.Clients); !s.IsAll() {
		t.Fatalf("nil snapshot is not all: %+v", s)
	}
	if s := dbSet(got.Teams); s.IsAll() || len(s.IDs()) != 0 {
		t.Fatalf("empty snapshot grants %+v", s)
	}
}

// fakeKeys reloads an API key from memory (identity.APIKeyService.Reload).
type fakeKeys struct {
	info *identity.APIKeyInfo
	err  error
}

func (f *fakeKeys) Reload(_ context.Context, prefix string) (*identity.APIKeyInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.info == nil || f.info.ID != prefix {
		return nil, identity.ErrAPIKeyInvalid
	}
	info := *f.info
	return &info, nil
}

// featureGate entitles every feature but those of denied.
type featureGate struct{ denied map[string]bool }

func (g featureGate) Status(_ context.Context, orgID, key string) (entitlement.Entitlement, entitlement.Status, error) {
	if g.denied[key] {
		return entitlement.Entitlement{}, entitlement.StatusNotEntitled, nil
	}
	return entitlement.Entitlement{OrganizationID: orgID, FeatureKey: key}, entitlement.StatusActive, nil
}

// TestRunReloadsAPIKey: a run started with an API key reloads the key
// before it begins (WP-779, E-145): with the key's rights it runs; a key
// revoked after the start ends it with import-api-key-invalid, a key
// without ci:write with import-forbidden, a key that no longer acts as
// the creator, an unresolved owner and a reference without key prefix
// with import-principal-missing, and an organization without api_access
// with feature-not-entitled. A failed lookup fails the run. No row of a
// failed run is written.
func TestRunReloadsAPIKey(t *testing.T) {
	all := identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	key := func() *identity.APIKeyInfo {
		return &identity.APIKeyInfo{ID: "Pfx779abcdef", OwnerID: testUser, OrganizationID: testOrg, Scope: &all,
			Scopes: []identity.Permission{identity.PermImportRun, identity.PermCIWrite}}
	}
	for _, tc := range []struct {
		name   string
		keys   *fakeKeys
		prefix string
		denied map[string]bool
		code   string
	}{
		{"valid", &fakeKeys{info: key()}, "Pfx779abcdef", nil, ""},
		{"revoked", &fakeKeys{}, "Pfx779abcdef", nil, CodeAPIKeyInvalid},
		{"without ci:write", &fakeKeys{info: func() *identity.APIKeyInfo {
			k := key()
			k.Scopes = []identity.Permission{identity.PermImportRun}
			return k
		}()}, "Pfx779abcdef", nil, CodeForbidden},
		{"other owner", &fakeKeys{info: func() *identity.APIKeyInfo {
			k := key()
			k.OwnerID = "00000000-0000-4000-8000-0000000000f2"
			return k
		}()}, "Pfx779abcdef", nil, CodePrincipalMissing},
		{"owner not resolved", &fakeKeys{info: func() *identity.APIKeyInfo {
			k := key()
			k.Scope = nil
			return k
		}()}, "Pfx779abcdef", nil, CodePrincipalMissing},
		{"no key prefix", &fakeKeys{info: key()}, "", nil, CodePrincipalMissing},
		{"no api_access", &fakeKeys{info: key()}, "Pfx779abcdef", map[string]bool{entitlement.FeatureAPIAccess: true}, CodeFeatureNotEntitled},
		{"lookup fails", &fakeKeys{err: fmt.Errorf("%w: down", identity.ErrAPIKeyUnavailable)}, "Pfx779abcdef", nil, "import-failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newImportStack(t)
			w := &fakeWriter{entity: "ci"}
			h := NewImportHandler(s.profiles, s.docs, s.blobs).WithJobs(s.jobs).WithStarters(allowStarters, allowStarters).
				WithAPIKeys(tc.keys).WithWriter(w).WithGates(nil, featureGate{denied: tc.denied})
			profileID := s.profile(t, testOrg, "csv", FormatCSV, "")
			docID := s.document(t, testOrg, "text/csv", csvRows(2), document.ScanClean, 0)
			params := JobParams{DocumentID: docID, DocumentVersion: 1, ProfileID: profileID, Mode: "both",
				Starter:     &StarterRef{Type: identity.PrincipalTypeAPIKey, Subject: testUser, KeyPrefix: tc.prefix},
				ScopeParams: jobs.ScopeParams{RequestID: "req-779", Source: audit.SourceAPI}}
			job, _, err := s.jobs.Enqueue(requestContext(testOrg, testUser, nil), testOrg, JobKind, params,
				jobs.EnqueueOptions{CreatedBy: testUser})
			if err != nil {
				t.Fatal(err)
			}
			queue := jobs.NewQueue(s.jobs, nil)
			queue.Handle(jobs.Kind{Name: JobKind, MaxAttempts: 1, Cancellable: true}, h.RunJob)
			data, _ := json.Marshal(jobs.Message{OrganizationID: testOrg, JobID: job.ID, Kind: JobKind})
			queue.Process(context.Background(), &memDelivery{data: data})
			done, _ := s.jobs.Get(context.Background(), testOrg, job.ID)
			if tc.code == "" {
				if done.Status != jobs.StatusSucceeded || w.written() != 2 {
					t.Fatalf("job %s %q, %d rows written", done.Status, done.Error, w.written())
				}
				return
			}
			if done.Status != jobs.StatusFailed || errorCode(done.Error) != tc.code {
				t.Fatalf("job %s %q, want failed with %s", done.Status, done.Error, tc.code)
			}
			if w.written() != 0 {
				t.Fatalf("%d rows written", w.written())
			}
		})
	}
}

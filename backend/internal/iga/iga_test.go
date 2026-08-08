package iga

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

func TestParseUserNameFilter(t *testing.T) {
	got, ok := ParseUserNameFilter(`userName eq "alice@example.test"`)
	if !ok || got != "alice@example.test" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := ParseUserNameFilter(`displayName co "Alice"`); ok {
		t.Fatal("unsupported filter accepted")
	}
}
func TestSCIMConnectorUsesPagingAndPatch(t *testing.T) {
	var sawAuth bool
	var patchPath string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Users":
			if r.URL.Query().Get("startIndex") != "2" || r.URL.Query().Get("count") != "1" {
				t.Fatalf("bad paging: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"totalResults": 1, "Resources": []map[string]any{{"id": "u1", "userName": "a", "active": true}}})
		case r.Method == http.MethodPatch && r.URL.Path == "/Users/u1":
			patchPath = r.URL.Path
			w.WriteHeader(204)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c, err := NewSCIMConnector(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, total, err := c.ReadAccounts(context.Background(), 2, 1)
	if err != nil || total != 1 || len(got) != 1 || got[0].ID != "u1" {
		t.Fatalf("bad read %#v %d %v", got, total, err)
	}
	if !sawAuth {
		t.Fatal("bearer auth missing")
	}
	if err := c.DisableAccount(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if patchPath != "/Users/u1" {
		t.Fatalf("patch not called")
	}
	_, err = NewSCIMConnector("http://example.test", "", &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}})
	if err == nil {
		t.Fatal("http base URL accepted")
	}
}
func TestLifecycleResolutionCreatesTasks(t *testing.T) {
	repo := NewMemoryRepository()
	active := true
	p := &LifecyclePolicy{OrganizationID: "org", Name: "join", Event: "joiner", Active: active, Conditions: JSONMap{"department": "IT"}, Actions: []JSONMap{{"connector_id": "c1", "action": TaskActionCreateAccount, "userName": "ignored"}}}
	if err := repo.CreatePolicy(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	svc := NewLifecycleService(repo)
	tasks, err := svc.Resolve(context.Background(), "org", IdentityChange{Event: "joiner", UserID: "u1", Attributes: JSONMap{"department": "IT"}})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%d err=%v", len(tasks), err)
	}
	listed, total, _ := repo.ListTasks(context.Background(), "org", "", api.PaginationParams{Limit: 10})
	if total != 1 || listed[0].Action != TaskActionCreateAccount {
		t.Fatalf("task not persisted")
	}
}
func TestTaskRunnerRetryBackoff(t *testing.T) {
	repo := NewMemoryRepository()
	_ = repo.CreateConnector(context.Background(), &ConnectorConfig{OrganizationID: "org", Name: "bad", Type: ConnectorTypeSCIM, BaseURL: "https://bad.invalid"})
	conns, _, _ := repo.ListConnectors(context.Background(), "org", api.PaginationParams{Limit: 1})
	task := &ProvisioningTask{OrganizationID: "org", ConnectorID: conns[0].ID, Action: TaskActionDisableAccount, ExternalID: "u1", MaxAttempts: 3}
	_ = repo.CreateTask(context.Background(), task)
	runner := NewTaskRunner(repo, NewRegistry(nil, &http.Client{Timeout: time.Millisecond}), nil)
	_ = runner.RunTask(context.Background(), task)
	stored, _ := repo.GetTask(context.Background(), "org", task.ID)
	if stored.Status != TaskStatusPending || stored.Attempts != 1 || !stored.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("expected retry with backoff, got %#v", stored)
	}
}
func TestDetectDrift(t *testing.T) {
	findings := DetectDrift("org", "c", []Account{{ID: "expected", UserName: "e"}}, []Account{{ID: "orphan", UserName: "o"}})
	if len(findings) != 2 {
		t.Fatalf("got %d findings", len(findings))
	}
	kinds := map[string]bool{}
	for _, f := range findings {
		kinds[f.DriftType] = true
	}
	if !kinds["orphan_account"] || !kinds["missing_account"] {
		t.Fatalf("bad drift types %#v", kinds)
	}
}

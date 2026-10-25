package config

// WP-422 (OPS-01, OPS-03, F-036): the Kubernetes manifests provide every key
// the server's start check requires outside development. Moved from the
// defect test of the Phase-1 acceptance audit (group T5,
// k8s_required_env_auditdefect_test.go): commit 75aa419 made
// RETICORA_OIDC_ISSUER_URL mandatory outside development
// (requiredOutsideDevelopment), but deploy/k8s/base/deployment.yaml and the
// staging overlay (which replaced the whole env list) never provided it, so
// the pod ended in CrashLoopBackOff with "RETICORA_ENVIRONMENT=staging
// requires RETICORA_OIDC_ISSUER_URL".
//
// The test renders base, overlays/staging and overlays/prod with kustomize
// v5 (as the CI job k8s-manifests does), takes the env of the reticora-server
// container (literal values; secret/config references and envFrom objects
// rendered in the same build count as provided), and asserts
//  1. every key of k8sRequiredEnv (requiredOutsideDevelopment, the OIDC
//     client and RETICORA_INVITATION_ACCEPT_URL, without which issuing and
//     renewing invitations answers 503, WP-421) is provided, and none by a
//     reference marked optional: true, and
//  2. Load() with exactly that env (references filled with placeholders,
//     every other RETICORA_* variable unset) passes Validate(false), i.e.
//     the start check of cmd/server accepts it.
//
// Negative cases: fixture overlays (written to a temporary directory on top
// of base or staging) without the issuer, without the accept URL and with an
// optional master key reference are reported.
//
// kustomize v5.8.1 as in CI (go install sigs.k8s.io/kustomize/kustomize/v5@v5.8.1);
// without KUSTOMIZE the test is skipped, in the CI (CI=true) it fails:
//   cd backend && KUSTOMIZE=$(go env GOPATH)/bin/kustomize \
//     go test -count=1 -v -run 'TestAuditT5K8sRenderedEnvSatisfiesStartCheck' ./internal/config/

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// k8sRequiredEnv are the keys every rendered overlay must provide: the start
// check's requiredOutsideDevelopment plus the OIDC client and the accept page
// of the invitation mail, which the start check does not enforce but without
// which the login or the invitations do not work (deploy/README.md).
var k8sRequiredEnv = append(slices.Clone(requiredOutsideDevelopment),
	"RETICORA_OIDC_CLIENT_ID", "RETICORA_OIDC_CLIENT_SECRET", "RETICORA_OIDC_REDIRECT_URL",
	"RETICORA_INVITATION_ACCEPT_URL")

type k8sObject struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Data       map[string]string `yaml:"data"`
	StringData map[string]string `yaml:"stringData"`
	Spec       struct {
		Template struct {
			Spec struct {
				Containers []k8sContainer `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type k8sContainer struct {
	Name string `yaml:"name"`
	Env  []struct {
		Name      string         `yaml:"name"`
		Value     *string        `yaml:"value"`
		ValueFrom map[string]any `yaml:"valueFrom"`
	} `yaml:"env"`
	EnvFrom []struct {
		Prefix       string `yaml:"prefix"`
		ConfigMapRef *struct {
			Name string `yaml:"name"`
		} `yaml:"configMapRef"`
		SecretRef *struct {
			Name string `yaml:"name"`
		} `yaml:"secretRef"`
	} `yaml:"envFrom"`
}

// kustomizeBinary returns KUSTOMIZE. Without it the test is skipped; in the
// CI (CI=true) it fails, so the check cannot pass there without rendering.
func kustomizeBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("KUSTOMIZE"); p != "" {
		return p
	}
	if ci, _ := strconv.ParseBool(os.Getenv("CI")); ci {
		t.Fatal("KUSTOMIZE not set in the CI: the rendered manifests must be checked (go install sigs.k8s.io/kustomize/kustomize/v5@v5.8.1)")
	}
	t.Skip("KUSTOMIZE not set; skipping the check of the rendered Kubernetes manifests")
	return ""
}

// renderedEnv is the env of the reticora-server container of one build.
type renderedEnv struct {
	values     map[string]string // name -> literal value ("" for references)
	refs       map[string]bool   // filled at runtime from a secret/config reference
	optional   map[string]bool   // references marked optional: true
	unresolved []string          // envFrom objects missing from the build
}

// renderServerEnv renders dir and returns the env of the reticora-server
// container.
func renderServerEnv(t *testing.T, kustomize, dir string) renderedEnv {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(kustomize, "build", dir)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("kustomize build %s: %v\n%s", dir, err, stderr.String())
	}
	objects := map[string]k8sObject{}
	var server *k8sContainer
	dec := yaml.NewDecoder(&stdout)
	for {
		var obj k8sObject
		err := dec.Decode(&obj)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode kustomize output of %s: %v", dir, err)
		}
		objects[obj.Kind+"/"+obj.Metadata.Name] = obj
		if obj.Kind == "Deployment" && obj.Metadata.Name == "reticora-server" {
			for i := range obj.Spec.Template.Spec.Containers {
				c := obj.Spec.Template.Spec.Containers[i]
				for _, e := range c.Env {
					if strings.HasPrefix(e.Name, "RETICORA_") {
						server = &c
						break
					}
				}
			}
		}
	}
	if server == nil {
		t.Fatalf("%s renders no reticora-server Deployment with a RETICORA_* container", dir)
	}
	r := renderedEnv{values: map[string]string{}, refs: map[string]bool{}, optional: map[string]bool{}}
	for _, ef := range server.EnvFrom {
		var key string
		switch {
		case ef.ConfigMapRef != nil:
			key = "ConfigMap/" + ef.ConfigMapRef.Name
		case ef.SecretRef != nil:
			key = "Secret/" + ef.SecretRef.Name
		default:
			continue
		}
		obj, ok := objects[key]
		if !ok {
			// External object: its keys are not part of the manifests.
			r.unresolved = append(r.unresolved, "envFrom "+key)
			continue
		}
		for k, v := range obj.Data {
			r.values[ef.Prefix+k] = v
		}
		for k, v := range obj.StringData {
			r.values[ef.Prefix+k] = v
		}
	}
	for _, e := range server.Env {
		switch {
		case e.Value != nil:
			r.values[e.Name] = *e.Value
		case e.ValueFrom != nil:
			r.values[e.Name] = ""
			r.refs[e.Name] = true
			for _, kind := range []string{"secretKeyRef", "configMapKeyRef"} {
				if ref, ok := e.ValueFrom[kind].(map[string]any); ok && ref["optional"] == true {
					r.optional[e.Name] = true
				}
			}
		default:
			r.values[e.Name] = ""
		}
	}
	return r
}

// placeholder fills a secret/config reference with a syntactically usable
// value; the start check only requires the key to be set and the URLs to be
// absolute.
func placeholder(key string) string {
	switch key {
	case "RETICORA_DATABASE_URL":
		return "postgres://reticora:secret@reticora-db:5432/reticora?sslmode=require"
	case "RETICORA_REDIS_URL":
		return "redis://redis:6379"
	case "RETICORA_OIDC_ISSUER_URL":
		return "https://id.example.com/realms/reticora"
	case "RETICORA_OIDC_REDIRECT_URL":
		return "https://cmdb.example.com/auth/callback"
	case "RETICORA_INVITATION_ACCEPT_URL":
		return "https://cmdb.example.com/invitations/accept"
	default:
		return "k8s-secret-placeholder-0123456789abcdef0123456789abcdef"
	}
}

// checkRenderedEnv returns the keys of k8sRequiredEnv the rendered env does
// not provide, those provided only by an optional reference, and the
// verdict of the start check on exactly that env.
func checkRenderedEnv(t *testing.T, r renderedEnv) (missing, optional []string, startErr error) {
	t.Helper()
	for _, key := range k8sRequiredEnv {
		if _, ok := r.values[key]; !ok {
			missing = append(missing, key)
		} else if r.optional[key] {
			optional = append(optional, key)
		}
	}
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "RETICORA_") {
			t.Setenv(k, "")
		}
	}
	for k, v := range r.values {
		if r.refs[k] {
			v = placeholder(k)
		}
		t.Setenv(k, v)
	}
	return missing, optional, Load().Validate(false)
}

// writeFixtureOverlay writes a kustomization on top of the repository's
// overlay dir with the given strategic merge patch of the server container
// and returns its directory. kustomize only accepts relative paths to
// another kustomization.
func writeFixtureOverlay(t *testing.T, repo, dir, containerPatch string) string {
	t.Helper()
	fixture := t.TempDir()
	rel, err := filepath.Rel(fixture, filepath.Join(repo, dir))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - " + rel + "\n")
	b.WriteString("patches:\n  - target:\n      kind: Deployment\n      name: reticora-server\n    patch: |-\n")
	b.WriteString("      apiVersion: apps/v1\n      kind: Deployment\n      metadata:\n        name: reticora-server\n")
	b.WriteString("      spec:\n        template:\n          spec:\n            containers:\n              - name: server\n")
	for _, line := range strings.Split(strings.TrimRight(containerPatch, "\n"), "\n") {
		b.WriteString("                " + line + "\n")
	}
	if err := os.WriteFile(filepath.Join(fixture, "kustomization.yaml"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestAuditT5K8sRenderedEnvSatisfiesStartCheck(t *testing.T) {
	kustomize := kustomizeBinary(t)
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, overlay := range []string{"deploy/k8s/base", "deploy/k8s/overlays/staging", "deploy/k8s/overlays/prod"} {
		t.Run(overlay, func(t *testing.T) {
			r := renderServerEnv(t, kustomize, filepath.Join(repo, overlay))
			names := make([]string, 0, len(r.values))
			for k := range r.values {
				names = append(names, k)
			}
			sort.Strings(names)
			environment := r.values["RETICORA_ENVIRONMENT"]
			if environment == "" || environment == EnvDevelopment {
				t.Fatalf("%s: rendered RETICORA_ENVIRONMENT=%q; a cluster deployment must run as staging or production", overlay, environment)
			}

			missing, optional, startErr := checkRenderedEnv(t, r)
			// 1. Every required key is provided by the rendered manifests,
			//    none by a reference the kubelet may leave unset.
			if len(missing) > 0 {
				t.Errorf("%s (RETICORA_ENVIRONMENT=%s): the server container env lacks %s; rendered env: %s; unresolved: %v",
					overlay, environment, strings.Join(missing, ", "), strings.Join(names, " "), r.unresolved)
			}
			if len(optional) > 0 {
				t.Errorf("%s: %s only from a reference with optional: true", overlay, strings.Join(optional, ", "))
			}
			// 2. The start check of cmd/server accepts exactly the rendered env.
			if startErr != nil {
				t.Errorf("%s: the server would exit at start (CrashLoopBackOff): %v", overlay, startErr)
			}
		})
	}

	// Negative cases: the check reports a key a manifest drops.
	for _, tc := range []struct {
		name, overlay, patch string
		missing, optional    []string
		startErr             string // "" = the start check accepts the env
	}{
		{
			name: "fixture without RETICORA_OIDC_ISSUER_URL", overlay: "deploy/k8s/base",
			patch:    "env:\n  - name: RETICORA_OIDC_ISSUER_URL\n    $patch: delete",
			missing:  []string{"RETICORA_OIDC_ISSUER_URL"},
			startErr: "RETICORA_ENVIRONMENT=production requires RETICORA_OIDC_ISSUER_URL",
		},
		{
			name: "fixture without RETICORA_INVITATION_ACCEPT_URL", overlay: "deploy/k8s/overlays/staging",
			patch:   "env:\n  - name: RETICORA_INVITATION_ACCEPT_URL\n    $patch: delete",
			missing: []string{"RETICORA_INVITATION_ACCEPT_URL"},
		},
		{
			name: "fixture with an optional master key reference", overlay: "deploy/k8s/overlays/staging",
			patch:    "env:\n  - name: RETICORA_MASTER_KEY\n    valueFrom:\n      secretKeyRef:\n        optional: true",
			optional: []string{"RETICORA_MASTER_KEY"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := renderServerEnv(t, kustomize, writeFixtureOverlay(t, repo, tc.overlay, tc.patch))
			missing, optional, startErr := checkRenderedEnv(t, r)
			if !slices.Equal(missing, tc.missing) || !slices.Equal(optional, tc.optional) {
				t.Errorf("reported missing %v, optional %v; want missing %v, optional %v", missing, optional, tc.missing, tc.optional)
			}
			switch {
			case tc.startErr == "" && startErr != nil:
				t.Errorf("start check: %v, want none", startErr)
			case tc.startErr != "" && (startErr == nil || !strings.Contains(startErr.Error(), tc.startErr)):
				t.Errorf("start check: %v, want an error containing %q", startErr, tc.startErr)
			}
		})
	}
}

// TestK8sREADMENamesRequiredEnv: deploy/README.md documents every key of
// k8sRequiredEnv and the objects the manifests take them from (WP-422).
func TestK8sREADMENamesRequiredEnv(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range append(slices.Clone(k8sRequiredEnv), "reticora-config", "reticora-secrets", "reticora-db-runtime",
		"oidc-issuer-url", "oidc-client-id", "oidc-client-secret", "oidc-redirect-url", "invitation-accept-url") {
		if !bytes.Contains(readme, []byte("`"+want+"`")) {
			t.Errorf("deploy/README.md does not name `%s`", want)
		}
	}
}

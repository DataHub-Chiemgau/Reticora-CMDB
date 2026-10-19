//go:build auditdefect

package config

// Phase-1 acceptance audit, group T5-install-ops-gate: DEFECT test (fails
// until the Kubernetes manifests are fixed).
//
// WP-097 (OPS-01) P1 "K8s manifests do not provide RETICORA_OIDC_ISSUER_URL,
// which WP-097 made mandatory, so the server exits at start in staging and
// production" and WP-099 (OPS-03) P1 "Overlays with the injected master key
// still do not start: RETICORA_OIDC_ISSUER_URL is absent from all kustomize
// builds": commit 75aa419 made RETICORA_OIDC_ISSUER_URL mandatory outside
// development (requiredOutsideDevelopment), but deploy/k8s/base/deployment.yaml
// and the staging overlay (which replaces the whole env list) never provide
// it and use no envFrom, so the pod ends in CrashLoopBackOff with
// "RETICORA_ENVIRONMENT=staging requires RETICORA_OIDC_ISSUER_URL".
// Test gaps closed: high "K8s rendered env is not checked against the
// required configuration" (WP-097, WP-099).
//
// The test renders base, overlays/staging and overlays/prod with kustomize
// v5 (as the CI job k8s-manifests does), takes the env of the reticora-server
// container (literal values; secret/config references and envFrom objects
// rendered in the same build count as provided), and asserts
//   1. every key of requiredOutsideDevelopment is provided, and
//   2. Load() with exactly that env (secret references filled with
//      placeholders, every other RETICORA_* variable unset) passes
//      Validate(false), i.e. the start check of cmd/server accepts it.
//
// Retest (kustomize v5.8.1 as in CI: go install sigs.k8s.io/kustomize/kustomize/v5@v5.8.1):
//   cd backend && KUSTOMIZE=$(go env GOPATH)/bin/kustomize \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT5K8sRenderedEnvSatisfiesStartCheck' ./internal/config/

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

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

func kustomizeBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("KUSTOMIZE"); p != "" {
		return p
	}
	if p, err := exec.LookPath("kustomize"); err == nil {
		return p
	}
	t.Fatal("kustomize v5 is required (CI: go install sigs.k8s.io/kustomize/kustomize/v5@v5.8.1); set KUSTOMIZE=<path>")
	return ""
}

// renderedServerEnv renders dir and returns the env of the reticora-server
// container (name -> literal value), the names filled at runtime from a
// secret/config reference, and the envFrom objects missing from the build.
func renderedServerEnv(t *testing.T, kustomize, dir string) (env map[string]string, refs map[string]bool, unresolved []string) {
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
	env = map[string]string{}
	refs = map[string]bool{}
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
			unresolved = append(unresolved, "envFrom "+key)
			continue
		}
		for k, v := range obj.Data {
			env[ef.Prefix+k] = v
		}
		for k, v := range obj.StringData {
			env[ef.Prefix+k] = v
		}
	}
	for _, e := range server.Env {
		switch {
		case e.Value != nil:
			env[e.Name] = *e.Value
		case e.ValueFrom != nil:
			env[e.Name] = ""
			refs[e.Name] = true
		default:
			env[e.Name] = ""
		}
	}
	return env, refs, unresolved
}

// placeholder fills a secret/config reference with a syntactically usable
// value; the start check only requires the key to be set.
func placeholder(key string) string {
	switch key {
	case "RETICORA_DATABASE_URL":
		return "postgres://reticora:secret@reticora-db:5432/reticora?sslmode=require"
	case "RETICORA_REDIS_URL":
		return "redis://redis:6379"
	case "RETICORA_OIDC_ISSUER_URL":
		return "https://id.example.com/realms/reticora"
	default:
		return "k8s-secret-placeholder-0123456789abcdef0123456789abcdef"
	}
}

func TestAuditT5K8sRenderedEnvSatisfiesStartCheck(t *testing.T) {
	kustomize := kustomizeBinary(t)
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, overlay := range []string{"deploy/k8s/base", "deploy/k8s/overlays/staging", "deploy/k8s/overlays/prod"} {
		t.Run(overlay, func(t *testing.T) {
			env, refs, unresolved := renderedServerEnv(t, kustomize, filepath.Join(repo, overlay))
			names := make([]string, 0, len(env))
			for k := range env {
				names = append(names, k)
			}
			sort.Strings(names)
			environment := env["RETICORA_ENVIRONMENT"]
			if environment == "" || environment == EnvDevelopment {
				t.Fatalf("%s: rendered RETICORA_ENVIRONMENT=%q; a cluster deployment must run as staging or production", overlay, environment)
			}

			// 1. Every key the start check requires outside development is
			//    provided by the rendered manifests.
			var missing []string
			for _, key := range requiredOutsideDevelopment {
				if _, ok := env[key]; !ok {
					missing = append(missing, key)
				}
			}
			if len(missing) > 0 {
				t.Errorf("%s (RETICORA_ENVIRONMENT=%s): the server container env lacks %s (requiredOutsideDevelopment); rendered env: %s; unresolved: %v",
					overlay, environment, strings.Join(missing, ", "), strings.Join(names, " "), unresolved)
			}

			// 2. The start check of cmd/server accepts exactly the rendered env.
			for _, kv := range os.Environ() {
				if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "RETICORA_") {
					t.Setenv(k, "")
				}
			}
			for k, v := range env {
				if refs[k] {
					v = placeholder(k)
				}
				t.Setenv(k, v)
			}
			if err := Load().Validate(false); err != nil {
				t.Errorf("%s: the server would exit at start (CrashLoopBackOff): %v", overlay, err)
			}
		})
	}
}

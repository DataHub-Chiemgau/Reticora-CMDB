package admp_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The configuration of the manual smoke test against an ADMP test
// environment (smoke_test.go, build tag admp_smoke, WP-1077 AK7). The
// environment names only the base URL, the id of the api_token credential
// (E-249: username and token) and lists of known portId and connection
// ids; the secret never stands in an environment variable, the test loads
// it through credential.Service.Decrypt (stored or vault_ref, CRD-02).
// This file has no build tag: TestAdmpSmokeReadsNoSecretFromEnv runs in CI.

// Variables of the smoke test.
const (
	smokeEnvBaseURL       = "RETICORA_ADMP_SMOKE_BASE_URL"
	smokeEnvCredentialID  = "RETICORA_ADMP_SMOKE_CREDENTIAL_ID"
	smokeEnvPortIDs       = "RETICORA_ADMP_SMOKE_PORT_IDS"
	smokeEnvConnectionIDs = "RETICORA_ADMP_SMOKE_CONNECTION_IDS"
)

// smokeEnvNames are all variables the smoke configuration reads.
var smokeEnvNames = []string{smokeEnvBaseURL, smokeEnvCredentialID, smokeEnvPortIDs, smokeEnvConnectionIDs}

// smokeConfig is the configuration of a smoke run: no secret.
type smokeConfig struct {
	BaseURL       string
	CredentialID  string
	PortIDs       []int64
	ConnectionIDs []int64
}

var errSmokeIDs = errors.New("admp smoke: an id list holds something else than decimal ids")

// loadSmokeConfig reads the configuration through getenv (os.Getenv). Nil
// without base URL or credential id: the smoke test skips itself.
func loadSmokeConfig(getenv func(string) string) (*smokeConfig, error) {
	cfg := &smokeConfig{BaseURL: strings.TrimSpace(getenv(smokeEnvBaseURL)), CredentialID: strings.TrimSpace(getenv(smokeEnvCredentialID))}
	ports, connections := getenv(smokeEnvPortIDs), getenv(smokeEnvConnectionIDs)
	if cfg.BaseURL == "" || cfg.CredentialID == "" {
		return nil, nil
	}
	var err error
	if cfg.PortIDs, err = smokeIDs(ports); err != nil {
		return nil, err
	}
	if cfg.ConnectionIDs, err = smokeIDs(connections); err != nil {
		return nil, err
	}
	return cfg, nil
}

// smokeIDs parses a comma-separated list of decimal ids.
func smokeIDs(raw string) ([]int64, error) {
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil || n < 1 {
			return nil, errSmokeIDs
		}
		out = append(out, n)
	}
	return out, nil
}

// TestAdmpSmokeReadsNoSecretFromEnv: the configuration reads exactly the
// four variables of the plan, also when the environment offers a token or
// password; its fields hold no secret; the smoke files reach the
// environment only through loadSmokeConfig(os.Getenv) and name no other
// RETICORA_ADMP_SMOKE_ variable.
func TestAdmpSmokeReadsNoSecretFromEnv(t *testing.T) {
	env := map[string]string{
		smokeEnvBaseURL: "https://admp-test.example.net", smokeEnvCredentialID: "8b0e4c1e-2f6d-4d7a-9a51-0f1c2d3e4f50",
		smokeEnvPortIDs: "301, 302", smokeEnvConnectionIDs: "4711",
		"RETICORA_ADMP_SMOKE_TOKEN": "env-token-canary", "RETICORA_ADMP_SMOKE_PASSWORD": "env-password-canary",
		"RETICORA_ADMP_SMOKE_USERNAME": "env-user-canary",
	}
	var read []string
	cfg, err := loadSmokeConfig(func(name string) string {
		read = append(read, name)
		return env[name]
	})
	if err != nil || cfg == nil {
		t.Fatalf("configuration: %+v %v", cfg, err)
	}
	sort.Strings(read)
	want := append([]string(nil), smokeEnvNames...)
	sort.Strings(want)
	if !slices.Equal(read, want) {
		t.Errorf("variables read %v, want exactly %v", read, want)
	}
	if cfg.BaseURL != env[smokeEnvBaseURL] || cfg.CredentialID != env[smokeEnvCredentialID] ||
		!slices.Equal(cfg.PortIDs, []int64{301, 302}) || !slices.Equal(cfg.ConnectionIDs, []int64{4711}) {
		t.Errorf("configuration %+v", cfg)
	}
	if text := strings.Join([]string{cfg.BaseURL, cfg.CredentialID}, " "); strings.Contains(text, "canary") {
		t.Errorf("the configuration holds a secret of the environment: %s", text)
	}
	if cfg, err = loadSmokeConfig(func(string) string { return "" }); cfg != nil || err != nil {
		t.Errorf("without variables: %+v %v, want nil (skip)", cfg, err)
	}
	if _, err = loadSmokeConfig(func(name string) string {
		if name == smokeEnvPortIDs {
			return "301,x"
		}
		return env[name]
	}); !errors.Is(err, errSmokeIDs) {
		t.Errorf("invalid id list: %v", err)
	}

	// The sources of the smoke test: the environment only through
	// loadSmokeConfig(os.Getenv), no other smoke variable.
	for _, file := range []string{"smoke_test.go", "smoke_config_test.go"} {
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, file, nil, parser.ParseComments)
		if perr != nil {
			t.Fatal(perr)
		}
		getenvAsArgument := 0
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if fn, ok := x.Fun.(*ast.Ident); ok && fn.Name == "loadSmokeConfig" && len(x.Args) == 1 {
					if sel, isSel := x.Args[0].(*ast.SelectorExpr); isSel && isPkg(sel, "os", "Getenv") {
						getenvAsArgument++
						return false
					}
				}
			case *ast.SelectorExpr:
				for _, name := range []string{"Getenv", "LookupEnv", "Environ", "ExpandEnv", "Setenv"} {
					if isPkg(x, "os", name) || isPkg(x, "syscall", name) {
						t.Errorf("%s: os.%s outside loadSmokeConfig(os.Getenv) at %s", file, name, fset.Position(x.Pos()))
					}
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					v, _ := strconv.Unquote(x.Value)
					for _, name := range smokeVariable.FindAllString(v, -1) {
						if !slices.Contains(smokeEnvNames, name) && file == "smoke_test.go" {
							t.Errorf("%s names the variable %s", file, name)
						}
					}
				}
			}
			return true
		})
		if file == "smoke_test.go" && getenvAsArgument != 1 {
			t.Errorf("smoke_test.go reads the environment %d times through loadSmokeConfig(os.Getenv), want once", getenvAsArgument)
		}
		if file == "smoke_config_test.go" && getenvAsArgument != 0 {
			t.Errorf("smoke_config_test.go calls loadSmokeConfig(os.Getenv)")
		}
	}
}

// smokeVariable finds the names of smoke variables in a text.
var smokeVariable = regexp.MustCompile(`RETICORA_ADMP_SMOKE_[A-Z_]*[A-Z]`)

// isPkg reports whether sel is pkg.name.
func isPkg(sel *ast.SelectorExpr, pkg, name string) bool {
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg && sel.Sel.Name == name
}

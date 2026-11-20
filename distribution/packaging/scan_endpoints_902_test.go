package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #902 (EPIC O, matrix row O-16): the example observability bundle carries no
// credential and no real endpoint. Same discipline as scan_endpoints_test.go:
// every value planted here is made up, and every rule is shown to fire on a
// planted shape and to stay quiet on the shapes the bundle really uses.

const madeUpGrafanaPassword = "a-password-this-test-invented"

// The two rule names, spelled as the strings a Violation carries, so this
// file pins what a reader of the gate output sees.
const (
	ruleCommittedCredential   = "committed-credential"
	ruleExampleBundleEndpoint = "example-bundle-endpoint"
)

func scanPlanted(t *testing.T, file, body string) []Violation {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, file)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ScanForLeakedEndpoints(root, []string{file})
	if err != nil {
		t.Fatalf("scanning the planted fixture: %v", err)
	}
	return got
}

func TestTheBundleCredentialAndEndpointShapesAreCaught_902(t *testing.T) {
	cases := []struct {
		name, file, body, rule string
	}{
		{
			"a committed Grafana admin password in the bundle's compose file",
			"container/observability/compose.observability.yaml",
			"services:\n  grafana:\n    environment:\n      GF_SECURITY_ADMIN_PASSWORD: " + madeUpGrafanaPassword + "\n",
			ruleCommittedCredential,
		},
		{
			"the same password as an env-file line anywhere in the tree",
			"docs/grafana.env",
			"GF_SECURITY_ADMIN_PASSWORD=" + madeUpGrafanaPassword + "\n",
			ruleCommittedCredential,
		},
		{
			"a scrape token typed into the bundle's prometheus.yml",
			"container/observability/prometheus/prometheus.yml",
			"scrape_configs:\n  - job_name: retnd\n    bearer_token: rsc_a-token-this-test-invented\n",
			ruleCommittedCredential,
		},
		{
			"a real-shaped scrape target in prometheus.yml",
			"container/observability/prometheus/prometheus.yml",
			"scrape_configs:\n  - job_name: retnd\n    static_configs:\n      - targets:\n          - " + madeUpRoutableHost + ":8080\n",
			ruleExampleBundleEndpoint,
		},
		{
			"a real-shaped datasource URL with no port",
			"container/observability/grafana/provisioning/datasources/datasources.yml",
			"datasources:\n  - name: Loki\n    url: https://" + madeUpRoutableHost + "/loki\n",
			ruleExampleBundleEndpoint,
		},
		{
			"a routable address as an Alertmanager target",
			"container/observability/prometheus/prometheus.yml",
			"alerting:\n  alertmanagers:\n    - static_configs:\n        - targets: [\"" + madeUpRoutableAddr + ":9093\"]\n",
			ruleExampleBundleEndpoint,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanPlanted(t, tc.file, tc.body); !firedRule(got, tc.rule) {
				t.Fatalf("planting this shape did not trip %s, so that rule cannot fail and is not a guard.\nplanted in %s:\n%s\nreported: %v", tc.rule, tc.file, tc.body, got)
			}
		})
	}
}

func TestWhatTheBundleRulesMustNotReport_902(t *testing.T) {
	cases := []struct {
		name, file, body string
	}{
		{"the required-variable interpolation the bundle uses", "container/observability/compose.observability.yaml",
			"      GF_SECURITY_ADMIN_PASSWORD: ${GF_SECURITY_ADMIN_PASSWORD:?GF_SECURITY_ADMIN_PASSWORD must be set}\n"},
		{"a token read from a file", "container/observability/prometheus/prometheus.yml",
			"    authorization:\n      type: Bearer\n      credentials_file: /run/secrets/retnd_scrape_token\n"},
		{"a compose secret named after the token", "container/observability/compose.observability.yaml",
			"secrets:\n  retnd_scrape_token:\n    file: ${RETND_SCRAPE_TOKEN_FILE:?name the token file}\n"},
		{"compose service names with ports", "container/observability/prometheus/prometheus.yml",
			"      - targets:\n          - retnd:8080\n        - alertmanager:9093\n"},
		{"service-name URLs", "container/observability/grafana/provisioning/datasources/datasources.yml",
			"    url: http://prometheus:9090\n    url: http://loki:3100\n"},
		{"reserved literals", "container/observability/README.md",
			"Grafana listens on 127.0.0.1:3000; a remote one might be https://grafana.invalid:3000.\n"},
		{"an image reference with a version tag", "container/observability/compose.observability.yaml",
			"    image: grafana/grafana:11.3.0\n    image: prom/prometheus:v2.55.1\n"},
		{"the matrix's prose naming the variable", "docs/conformance/epic-o-matrix.md",
			"a committed `GF_SECURITY_ADMIN_PASSWORD=` literal and a real-shaped hostname\n"},
		{"a routable URL outside the bundle with no SSH next to it, which this rule does not reach",
			"docs/links.md", "See https://" + madeUpRoutableHost + "/docs.\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanPlanted(t, tc.file, tc.body); len(got) != 0 {
				t.Fatalf("a shape the bundle really uses was reported, which is how a rule gets switched off: %v\n%s", got, tc.body)
			}
		})
	}
}

// The bundle as it is in the tree, scanned on its own, so a failure here
// names the bundle rather than being one line among the whole-tree sweep.
func TestTheShippedBundleIsClean_902(t *testing.T) {
	root := RepoRoot
	var rel []string
	err := filepath.Walk(filepath.Join(root, "container", "observability"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			r, _ := filepath.Rel(root, path)
			rel = append(rel, filepath.ToSlash(r))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking container/observability: %v", err)
	}
	if len(rel) < 8 {
		t.Fatalf("container/observability holds %d files, which is too few to be the bundle", len(rel))
	}
	got, err := ScanForLeakedEndpoints(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range got {
		if strings.HasPrefix(v.Path, "container/observability/") {
			t.Errorf("%s: %s (%s)", v.Path, v.Rule, v.Detail)
		}
	}
}

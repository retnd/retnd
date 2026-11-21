package packaging

import "testing"

// #1595: the four credential shapes the bundle scan missed. Every value is
// made up, and each shape gets a planted red and a quiet control.

func TestTheBundleScanCatchesTheShapesItMissed_1595(t *testing.T) {
	cases := []struct{ name, file, body string }{
		{"a credential under a JSON-quoted key", "container/observability/grafana/provisioning/datasources/datasources.json",
			"{\"basicAuthPassword\": \"a-made-up-secret-1595\"}\n"},
		{"a quoted token key in YAML", "container/observability/prometheus/prometheus.yml",
			"    \"api_key\": 'a-made-up-key-1595'\n"},
		{"Grafana's admin password in JSON form", "container/observability/compose.observability.json",
			"{\"environment\": {\"GF_SECURITY_ADMIN_PASSWORD\": \"" + madeUpGrafanaPassword + "\"}}\n"},
		{"Grafana's admin password in JSON form, anywhere in the tree", "docs/grafana.json",
			"{\"GF_SECURITY_ADMIN_PASSWORD\":\"" + madeUpGrafanaPassword + "\"}\n"},
		{"an inline Bearer header", "container/observability/prometheus/prometheus.yml",
			"    headers:\n      Authorization: Bearer a1b2c3d4e5f6a7b8c9d0\n"},
		{"an inline Bearer header in JSON", "container/observability/grafana/provisioning/datasources/datasources.json",
			"{\"httpHeaderValue1\": \"Authorization: Bearer a1b2c3d4e5f6a7b8c9d0\"}\n"},
		{"an inline Basic header", "container/observability/prometheus/prometheus.yml",
			"    headers:\n      Authorization: Basic dXNlcjpoeC1wYXNzd29yZC0xNTk1\n"},
		{"a literal rsc_ token under a neutral key", "container/observability/prometheus/prometheus.yml",
			"    labels:\n      note: rsc_Zk3Ym9Qw4Vt8Lp2Nx6Rb1Hd\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanPlanted(t, tc.file, tc.body); !firedRule(got, ruleCommittedCredential) {
				t.Fatalf("this shape was not reported as %s, so a credential written this way gets through.\nplanted in %s:\n%s\nreported: %v", ruleCommittedCredential, tc.file, tc.body, got)
			}
		})
	}
}

func TestTheBundleScanStaysQuietOnTheShapesTheBundleUses_1595(t *testing.T) {
	cases := []struct{ name, file, body string }{
		{"a JSON key with an interpolated value", "container/observability/compose.observability.json",
			"{\"GF_SECURITY_ADMIN_PASSWORD\": \"${GF_SECURITY_ADMIN_PASSWORD:?must be set}\"}\n"},
		{"a Bearer header read from the environment", "container/observability/prometheus/prometheus.yml",
			"      Authorization: Bearer ${RETND_SCRAPE_TOKEN}\n"},
		{"the word Bearer in prose and as a type", "container/observability/README.md",
			"Prometheus sends it as a Bearer credential.\n    authorization:\n      type: Bearer\n"},
		{"a Basic header read from the environment", "container/observability/prometheus/prometheus.yml",
			"      Authorization: Basic ${RETND_BASIC_AUTH}\n"},
		{"the prefix named in prose", "container/observability/README.md",
			"The mint prints a rsc_ token once, so rsc_ is how you spot one.\n"},
		{"a quoted key with a file path", "container/observability/prometheus/prometheus.yml",
			"    \"credentials_file\": \"/run/secrets/retnd_scrape_token\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanPlanted(t, tc.file, tc.body); len(got) != 0 {
				t.Fatalf("a shape the bundle really uses was reported: %v\n%s", got, tc.body)
			}
		})
	}
}

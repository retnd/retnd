package packaging

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// #902 (EPIC O, FR-102, matrix rows O-15 and O-16): the example observability
// bundle in container/observability is wired to the canonical deployment as it
// really is, carries its credentials only as required operator input, and
// never becomes canonical itself.
//
// The wiring facts (the network's name, the engine's service name and port)
// are read out of container/compose.yaml rather than typed here, so a change
// to the canonical file that would strand the bundle fails this test instead
// of leaving a Prometheus scraping a name that no longer resolves.

const bundleDir = "container/observability"

type bundleCompose struct {
	Name     string `yaml:"name"`
	Networks map[string]struct {
		Name     string `yaml:"name"`
		External bool   `yaml:"external"`
	} `yaml:"networks"`
	Secrets map[string]struct {
		File string `yaml:"file"`
	} `yaml:"secrets"`
	Services map[string]struct {
		Image       string            `yaml:"image"`
		Profiles    []string          `yaml:"profiles"`
		Environment map[string]string `yaml:"environment"`
		Networks    []string          `yaml:"networks"`
		Ports       []string          `yaml:"ports"`
		Volumes     []string          `yaml:"volumes"`
	} `yaml:"services"`
}

func readBundleCompose(t *testing.T) bundleCompose {
	t.Helper()
	var c bundleCompose
	data, err := os.ReadFile(Path(filepath.Join(bundleDir, "compose.observability.yaml")))
	if err != nil {
		t.Fatalf("reading the bundle's compose file: %v", err)
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("parsing the bundle's compose file: %v", err)
	}
	return c
}

// canonicalWiring reads the project name, the engine's network and the
// engine's listening port out of container/compose.yaml.
func canonicalWiring(t *testing.T) (project, network, service, port string) {
	t.Helper()
	var c struct {
		Name     string `yaml:"name"`
		Services map[string]struct {
			Command     any               `yaml:"command"`
			Environment map[string]string `yaml:"environment"`
			Networks    []string          `yaml:"networks"`
			Ports       []string          `yaml:"ports"`
		} `yaml:"services"`
	}
	data, err := os.ReadFile(Path("container/compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	// The engine is the service with no published port: web-ui is the only
	// one that publishes, and /metrics is the engine's.
	for name, s := range c.Services {
		if len(s.Ports) == 0 && len(s.Networks) == 1 {
			if service != "" {
				t.Fatalf("container/compose.yaml has two unpublished single-network services (%s, %s); this test cannot tell which is the engine", service, name)
			}
			service, network = name, s.Networks[0]
			addr := s.Environment["LISTEN_ADDR"]
			port = strings.TrimPrefix(addr, ":")
		}
	}
	if c.Name == "" || service == "" || port == "" {
		t.Fatalf("could not read the project name, the engine service and its LISTEN_ADDR out of container/compose.yaml (got %q, %q, %q)", c.Name, service, port)
	}
	return c.Name, network, service, port
}

func TestTheBundleReadmeSaysItIsAnExampleOnItsFirstLine_902(t *testing.T) {
	data, err := os.ReadFile(Path(filepath.Join(bundleDir, "README.md")))
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(data), "\n")
	lower := strings.ToLower(first)
	if !strings.Contains(lower, "example") || !strings.Contains(lower, "not a supported deployment") {
		t.Fatalf("the first line of %s/README.md is %q; it has to say this is an example and not a supported deployment, because it is the line an operator reads before deciding to run it", bundleDir, first)
	}
}

// requiredVarRe is compose's required-variable form with no default:
// ${NAME:?message}. A `:-` or `-` default would let the stack start with
// whatever the default is, which for a password is a committed credential.
func requiredVar(name string) *regexp.Regexp {
	return regexp.MustCompile(`^\$\{` + name + `:\?([^}]*)\}$`)
}

func TestTheBundleTakesItsCredentialsOnlyAsRequiredOperatorInput_902(t *testing.T) {
	c := readBundleCompose(t)

	pw, ok := c.Services["grafana"].Environment["GF_SECURITY_ADMIN_PASSWORD"]
	if !ok {
		t.Fatalf("grafana sets no GF_SECURITY_ADMIN_PASSWORD, so it would start with Grafana's own default admin password")
	}
	m := requiredVar("GF_SECURITY_ADMIN_PASSWORD").FindStringSubmatch(pw)
	if m == nil {
		t.Fatalf("GF_SECURITY_ADMIN_PASSWORD is %q; it has to be ${GF_SECURITY_ADMIN_PASSWORD:?...}, required with no default, so the stack refuses to start without it", pw)
	}
	if !strings.Contains(m[1], "GF_SECURITY_ADMIN_PASSWORD") {
		t.Fatalf("the refusal for a missing Grafana password is %q; it has to name the variable", m[1])
	}

	tok, ok := c.Secrets["retnd_scrape_token"]
	if !ok {
		t.Fatalf("the bundle declares no retnd_scrape_token secret, so the scrape token has nowhere to come from but a committed file")
	}
	m = requiredVar("RETND_SCRAPE_TOKEN_FILE").FindStringSubmatch(tok.File)
	if m == nil || !strings.Contains(m[1], "rotate-scrape-token") {
		t.Fatalf("the scrape token secret's file is %q; it has to be ${RETND_SCRAPE_TOKEN_FILE:?...} with a refusal naming the mint command", tok.File)
	}
}

func TestTheBundleJoinsTheCanonicalNetworkAndScrapesTheEngine_902(t *testing.T) {
	project, network, service, port := canonicalWiring(t)
	c := readBundleCompose(t)

	if c.Name == "" || c.Name == project {
		t.Fatalf("the bundle's compose project is %q; it must be a project of its own, not %q, or `down -v` on it takes the canonical stack's volumes", c.Name, project)
	}

	// The default has to be the canonical stack's engine network; an
	// operator on a provider that names it differently overrides it.
	want := project + "_" + network
	var external string
	for key, n := range c.Networks {
		if n.External {
			if n.Name != want && n.Name != "${RETND_NETWORK:-"+want+"}" {
				t.Errorf("external network %q is named %q; the canonical stack's engine network is %q, so it has to be that or ${RETND_NETWORK:-%s}", key, n.Name, want, want)
			}
			external = key
		}
	}
	if external == "" {
		t.Fatalf("the bundle joins no external network, so Prometheus cannot reach the engine, which publishes no port")
	}

	// Only Prometheus joins it. The engine trusts forwarded headers from its
	// peers on that network (container/compose.yaml reserves that position
	// for web-ui), so every extra container on it widens that trust.
	for name, s := range c.Services {
		for _, n := range s.Networks {
			if n == external && name != "prometheus" {
				t.Errorf("service %s joins %s; only prometheus may, because every container on that network is a direct peer of the engine", name, want)
			}
		}
	}

	var prom struct {
		ScrapeConfigs []struct {
			JobName       string `yaml:"job_name"`
			MetricsPath   string `yaml:"metrics_path"`
			StaticConfigs []struct {
				Targets []string `yaml:"targets"`
			} `yaml:"static_configs"`
			Authorization struct {
				CredentialsFile string `yaml:"credentials_file"`
			} `yaml:"authorization"`
		} `yaml:"scrape_configs"`
	}
	data, err := os.ReadFile(Path(filepath.Join(bundleDir, "prometheus", "prometheus.yml")))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &prom); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sc := range prom.ScrapeConfigs {
		if sc.JobName != "retnd" {
			continue
		}
		found = true
		if sc.MetricsPath != "/metrics" {
			t.Errorf("the retnd job scrapes %q, not /metrics", sc.MetricsPath)
		}
		if sc.Authorization.CredentialsFile != "/run/secrets/retnd_scrape_token" {
			t.Errorf("the retnd job reads its token from %q, not the compose secret /run/secrets/retnd_scrape_token", sc.Authorization.CredentialsFile)
		}
		target := service + ":" + port
		ok := false
		for _, st := range sc.StaticConfigs {
			for _, tg := range st.Targets {
				ok = ok || tg == target
			}
		}
		if !ok {
			t.Errorf("the retnd job does not scrape %s, which is the engine's service name and LISTEN_ADDR port in container/compose.yaml", target)
		}
	}
	if !found {
		t.Fatalf("prometheus.yml has no job named retnd")
	}
}

func TestTheBundlePublishesNothingBeyondGrafanaOnLoopback_902(t *testing.T) {
	c := readBundleCompose(t)
	for name, s := range c.Services {
		for _, p := range s.Ports {
			if name != "grafana" || !strings.HasPrefix(p, "127.0.0.1:") {
				t.Errorf("service %s publishes %q; the bundle publishes Grafana on loopback and nothing else", name, p)
			}
		}
		if s.Image == "" || strings.HasSuffix(s.Image, ":latest") || !strings.Contains(s.Image, ":") {
			t.Errorf("service %s runs image %q; pin an exact version so the example is the same example tomorrow", name, s.Image)
		}
	}
}

func TestTheBundleMountsTheLogPipelinesItShips_902(t *testing.T) {
	c := readBundleCompose(t)
	for svc, file := range map[string]string{"promtail": "promtail/promtail.yaml", "alloy": "alloy/config.alloy"} {
		mounted := false
		for _, v := range c.Services[svc].Volumes {
			mounted = mounted || strings.HasPrefix(v, "./"+file+":")
		}
		if !mounted {
			t.Errorf("service %s does not mount ./%s, the pipeline O1.2 pinned the labels of", svc, file)
		}
		if _, err := os.Stat(Path(filepath.Join(bundleDir, file))); err != nil {
			t.Errorf("%s/%s: %v", bundleDir, file, err)
		}
	}
	if len(c.Services["alloy"].Profiles) == 0 {
		t.Errorf("alloy runs by default beside promtail, so every line would be shipped twice; it needs a profile")
	}
}

// Nothing canonical swallows the example. The packaging derivation's inputs
// and outputs, the container build and the canonical compose file must not
// name the bundle, so the example never becomes something an installer,
// provider or store listing ships.
func TestNothingCanonicalNamesTheExampleBundle_902(t *testing.T) {
	for _, f := range []string{
		"container/compose.yaml",
		"container/Dockerfile",
		"container/release-manifest.json",
		"distribution/packaging/canonical.json",
		"distribution/packaging/compliance.json",
		"distribution/packaging/conformance.json",
		"distribution/packaging/submission.json",
		"scripts/install/embed_compose.py",
		"scripts/install/install_docker_host.py",
	} {
		data, err := os.ReadFile(Path(f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, needle := range [][]byte{[]byte("observability/"), []byte("compose.observability")} {
			if bytes.Contains(data, needle) {
				t.Errorf("%s names %q; the example bundle must not become an input or output of anything canonical (#902)", f, needle)
			}
		}
	}

	var canonical struct {
		Include any `yaml:"include"`
	}
	data, _ := os.ReadFile(Path("container/compose.yaml"))
	if err := yaml.Unmarshal(data, &canonical); err != nil {
		t.Fatal(err)
	}
	if canonical.Include != nil {
		t.Errorf("container/compose.yaml has an include: block; the canonical definition is one file, and an include is how an example becomes part of it")
	}
}

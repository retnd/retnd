// The jobs that turn a published release into a GitHub Release with CLI
// tarballs (issue #1078).
//
// What this holds in place: the three new jobs exist and have the shape
// they were agreed with, and nothing about the signing path moved while
// they were added. The signing identity is built out of the workflow's
// `on:` block, and the only job allowed to mint an OIDC token is
// `publish`, so a release job that grew an id-token or a trigger edit
// would change what every later release is verified against.
package packaging

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// onBlockSHA256 pins the bytes of the `on:` block, from the `on:` line up
// to the blank line before `concurrency:`. It is a hash on purpose: the
// block carries comments that explain the trigger, and a reformat that
// kept the meaning would still be a change a reviewer has to look at.
const onBlockSHA256 = "803cff1f4a45a75d8c5434abf9131af729f9b1263d7f3847a95136003917bec2"

type releaseJob struct {
	Name        string         `yaml:"name"`
	Needs       any            `yaml:"needs"`
	If          string         `yaml:"if"`
	RunsOn      any            `yaml:"runs-on"`
	Permissions map[string]any `yaml:"permissions"`
}

func readReleaseWorkflowRaw(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(Path(SigningWorkflowPath))
	if err != nil {
		t.Fatalf("cannot read %s: %v", SigningWorkflowPath, err)
	}
	return string(raw)
}

func readReleaseJobs(t *testing.T) map[string]releaseJob {
	t.Helper()
	var wf struct {
		Jobs map[string]releaseJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(readReleaseWorkflowRaw(t)), &wf); err != nil {
		t.Fatalf("cannot parse %s: %v", SigningWorkflowPath, err)
	}
	return wf.Jobs
}

func needsList(j releaseJob) []string {
	switch v := j.Needs.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, e := range v {
			out = append(out, e.(string))
		}
		return out
	}
	return nil
}

func TestReleaseWorkflowTriggerBlockIsByteUnchanged(t *testing.T) {
	s := readReleaseWorkflowRaw(t)
	a := strings.Index(s, "\non:\n")
	b := strings.Index(s, "\nconcurrency:")
	if a < 0 || b < a {
		t.Fatalf("cannot find the on: block in %s", SigningWorkflowPath)
	}
	sum := sha256.Sum256([]byte(s[a+1 : b]))
	if got := hex.EncodeToString(sum[:]); got != onBlockSHA256 {
		t.Fatalf("the on: block of %s changed (sha256 %s, pinned %s). The trigger is part of the cosign identity; do not edit it to add a job.", SigningWorkflowPath, got, onBlockSHA256)
	}
}

func TestPublishJobNeedsAndPermissionsAreUnchanged(t *testing.T) {
	p, ok := readReleaseJobs(t)["publish"]
	if !ok {
		t.Fatal("no publish job")
	}
	if got, want := needsList(p), []string{"decide", "require-release-gate", "compliance-artifacts"}; !reflect.DeepEqual(got, want) {
		t.Errorf("publish needs = %v, want %v", got, want)
	}
	want := map[string]any{"contents": "read", "packages": "write", "id-token": "write"}
	if !reflect.DeepEqual(p.Permissions, want) {
		t.Errorf("publish permissions = %v, want %v", p.Permissions, want)
	}
}

func TestOnlyPublishHoldsAnIDToken(t *testing.T) {
	for name, j := range readReleaseJobs(t) {
		if _, has := j.Permissions["id-token"]; has && name != "publish" {
			t.Errorf("job %q declares id-token; only publish may, it is the signing identity", name)
		}
	}
	if n := strings.Count(readReleaseWorkflowRaw(t), "id-token: write"); n != 1 {
		t.Errorf("id-token: write appears %d times in the workflow (comments included), want exactly the one in publish", n)
	}
}

func TestReleaseWorkflowHasTheCLIReleaseJobs(t *testing.T) {
	jobs := readReleaseJobs(t)
	for _, name := range []string{"cli-binaries", "cli-darwin-smoke", "github-release"} {
		j, ok := jobs[name]
		if !ok {
			t.Errorf("release.yml has no %s job", name)
			continue
		}
		if !strings.Contains(j.If, "needs.decide.outputs.publish == 'true'") && name != "cli-darwin-smoke" {
			t.Errorf("%s must only run when decide says this run publishes; if: %q", name, j.If)
		}
		if _, has := j.Permissions["packages"]; has {
			t.Errorf("%s must not hold packages", name)
		}
	}
	if j, ok := jobs["cli-darwin-smoke"]; ok {
		if j.RunsOn != "macos-latest" {
			t.Errorf("cli-darwin-smoke runs-on = %v, want macos-latest", j.RunsOn)
		}
		if got := needsList(j); !reflect.DeepEqual(got, []string{"decide", "cli-binaries"}) {
			t.Errorf("cli-darwin-smoke needs = %v", got)
		}
	}
	if j, ok := jobs["cli-binaries"]; ok {
		if got := needsList(j); !reflect.DeepEqual(got, []string{"decide", "publish"}) {
			t.Errorf("cli-binaries needs = %v, want [decide publish]", got)
		}
	}
	if j, ok := jobs["github-release"]; ok {
		if got := needsList(j); !reflect.DeepEqual(got, []string{"decide", "cli-binaries", "cli-darwin-smoke"}) {
			t.Errorf("github-release needs = %v", got)
		}
		if !reflect.DeepEqual(j.Permissions, map[string]any{"contents": "write"}) {
			t.Errorf("github-release permissions = %v, want only contents: write", j.Permissions)
		}
	}
	raw := readReleaseWorkflowRaw(t)
	for _, banned := range []string{"HOMEBREW", "homebrew", "brew "} {
		if strings.Contains(raw, banned) {
			t.Errorf("release.yml mentions %q; the Homebrew tap was cancelled", banned)
		}
	}
}

func TestReleaseCutIsZeroPointSixZero(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("LoadCanonical: %v", err)
	}
	if c.Image.Tag != "0.6.0" {
		t.Errorf("canonical image tag = %q, want 0.6.0", c.Image.Tag)
	}
	if c.Image.Published {
		t.Errorf("canonical image.published must be false until the release workflow records the push")
	}
	cl, err := os.ReadFile(Path("CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(cl)
	u := strings.Index(s, "## [Unreleased]")
	r := strings.Index(s, "## [0.6.0] - 2026-10-12")
	if u < 0 || r < 0 || u > r {
		t.Fatalf("CHANGELOG needs an [Unreleased] heading above [0.6.0] - 2026-10-12 (unreleased at %d, 0.6.0 at %d)", u, r)
	}
	if strings.TrimSpace(s[u+len("## [Unreleased]"):r]) != "" {
		t.Errorf("[Unreleased] must be empty after the cut")
	}
}

func TestProvenanceDocSaysTarballsAreUnsigned(t *testing.T) {
	b, err := os.ReadFile(Path("docs/compliance/release-provenance.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "SHA256SUMS") || !strings.Contains(strings.ToLower(s), "not signed") {
		t.Errorf("docs/compliance/release-provenance.md must say the CLI tarballs carry SHA256SUMS and are not signed")
	}
}

func TestReadmeKeepsItsStatusBadgeRow(t *testing.T) {
	b, err := os.ReadFile(Path("README.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	logo := strings.Index(s, "docs/site/assets/logo.svg")
	if logo < 0 {
		t.Fatal("README has no logo block")
	}
	rest := s[logo:]
	if i := strings.Index(rest, "</p>"); i >= 0 {
		rest = rest[i+4:]
	}
	if i := strings.Index(rest, "</p>"); i >= 0 {
		rest = rest[:i]
	}
	if !strings.Contains(rest, `align="center"`) {
		t.Errorf("the badge row must be a centred block directly under the logo")
	}
	for _, want := range []string{
		"actions/workflows/ci.yml/badge.svg",
		"actions/workflows/release.yml/badge.svg",
		"actions/workflows/nightly-e2e.yml/badge.svg",
		"actions/workflows/pages.yml/badge.svg",
		"Apache--2.0",
		"github/v/release/retnd/retnd",
		"go-mod/go-version/retnd/retnd",
	} {
		if !strings.Contains(rest, want) {
			t.Errorf("badge row is missing %q", want)
		}
	}
	if n, m := strings.Count(rest, "<img "), strings.Count(rest, ` alt="`); n < 7 || n != m {
		t.Errorf("want at least 7 badges each with alt text, got %d images and %d alts", n, m)
	}
	if strings.Contains(strings.ToLower(rest), "homebrew") {
		t.Errorf("no Homebrew badge")
	}
}

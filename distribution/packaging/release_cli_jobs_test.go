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
	"encoding/json"
	"image/gif"
	"os"
	"reflect"
	"regexp"
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
	n := 0
	for _, line := range strings.Split(readReleaseWorkflowRaw(t), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "id-token:") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("id-token is set on %d lines of the workflow, want exactly the one in publish", n)
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

// TestReleaseCutNamesOneVersionEverywhere holds the cut together without
// naming a version: whatever canonical.json says the image tag is, the
// release manifest, the installer and the newest dated CHANGELOG heading must
// say the same, and [Unreleased] must sit above that heading. It used to
// assert 0.6.0 literally, which meant every release had to edit the test
// that guards the cut.
func TestReleaseCutNamesOneVersionEverywhere(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("LoadCanonical: %v", err)
	}
	tag := c.Image.Tag
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(tag) {
		t.Fatalf("canonical image tag = %q, want a semantic version with no leading v", tag)
	}
	if want := "ghcr.io/retnd/retnd:" + tag; c.Image.Reference != want {
		t.Errorf("canonical image reference = %q, want %q", c.Image.Reference, want)
	}
	// image.published is not asserted here. It is false from the cut until the
	// release workflow records the push and true afterwards, and the merge-back
	// job runs this test after setting it, so pinning either value fails one of
	// the two moments (#1104). TestReleaseManifestRegistryDigestTracksTheCanonicalPublishFlag
	// is the check that the flag and the recorded digests agree.

	mb, err := os.ReadFile(Path("container/release-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatalf("release manifest: %v", err)
	}
	if m.Version != tag {
		t.Errorf("container/release-manifest.json version = %q, canonical image tag = %q", m.Version, tag)
	}

	ib, err := os.ReadFile(Path("scripts/install/install_docker_host.py"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `CARRIED_RELEASE = "` + tag + `"`; !strings.Contains(string(ib), want) {
		t.Errorf("scripts/install/install_docker_host.py does not carry %s", want)
	}

	cl, err := os.ReadFile(Path("CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(cl)
	u := strings.Index(s, "## [Unreleased]")
	loc := regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\] - \d{4}-\d{2}-\d{2}$`).FindStringSubmatchIndex(s)
	if u < 0 || loc == nil || u > loc[0] {
		t.Fatalf("CHANGELOG needs an [Unreleased] heading above the newest dated release heading (unreleased at %d, heading at %v)", u, loc)
	}
	if got := s[loc[2]:loc[3]]; got != tag {
		t.Errorf("newest dated CHANGELOG heading is %s, canonical image tag is %s", got, tag)
	}
	// [Unreleased] is not asserted empty: once a release is out it holds the next
	// release's entries, so pinning it empty fails the first change after the cut.
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
		"badge/license-MIT-blue",
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

// The tour sits under the logo and the badge row and ahead of the first
// paragraph, so someone landing on the repo sees the product before they
// read about it (#1084).
func TestReadmeOpensWithTheTourGif(t *testing.T) {
	b, err := os.ReadFile(Path("README.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	const tour = "docs/assets/readme-tour.gif"
	img := strings.Index(s, tour)
	badges := strings.Index(s, "actions/workflows/ci.yml/badge.svg")
	first := strings.Index(s, "A backup producer somewhere writes a dump")
	if img < 0 {
		t.Fatalf("README does not show %s", tour)
	}
	if badges < 0 || first < 0 || !(badges < img && img < first) {
		t.Errorf("the tour must come after the badge row and before the first paragraph (badges %d, tour %d, first paragraph %d)", badges, img, first)
	}
	tag := s[strings.LastIndex(s[:img], "<img"):]
	tag = tag[:strings.Index(tag, ">")]
	if !strings.Contains(tag, ` alt="`) || strings.Contains(tag, ` alt=""`) {
		t.Errorf("the tour needs alt text that says what it shows: %s", tag)
	}

	f, err := os.Open(Path(tour))
	if err != nil {
		t.Fatalf("the tour file is missing: %v", err)
	}
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatalf("%s is not a GIF: %v", tour, err)
	}
	var cs int
	for _, d := range g.Delay {
		cs += d
	}
	if secs := float64(cs) / 100; secs < 28 || secs > 32 {
		t.Errorf("the tour runs %.1fs, want about 30s", secs)
	}
	if g.LoopCount != 0 {
		t.Errorf("the tour should loop forever, loop count is %d", g.LoopCount)
	}
	if fi, err := os.Stat(Path(tour)); err != nil || fi.Size() > 6<<20 {
		t.Errorf("the tour must stay under 6 MB so the README loads, got %v (%v)", fi, err)
	}
}

// Every publish of the image keeps a versioned GitHub Release that points at
// the package page, and a second publish of the same version refreshes that
// release instead of failing on the tag that already exists (#1102).
func TestGitHubReleaseLinksThePackageAndSurvivesARepublish(t *testing.T) {
	raw := readReleaseWorkflowRaw(t)
	i := strings.Index(raw, "\n  github-release:")
	if i < 0 {
		t.Fatal("release.yml has no github-release job")
	}
	job := raw[i:]
	for _, want := range []string{
		"https://github.com/retnd/retnd/pkgs/container/retnd",
		"gh release view",
		"gh release upload",
		"--clobber",
		"gh release edit",
		"gh release create",
	} {
		if !strings.Contains(job, want) {
			t.Errorf("the github-release job must contain %q", want)
		}
	}
	if strings.Index(job, "gh release view") > strings.Index(job, "gh release create") {
		t.Errorf("the job must look for an existing release before it creates one")
	}
}

package packaging

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestReleaseTargetsAreOnlyLinuxAmd64AndDarwinArm64 holds #1452: a release
// builds, hashes and verifies linux/amd64 and darwin/arm64, and nothing
// else. Every place a second architecture used to be written down is read
// here, so a manifest, recorder, workflow step, package claim or installer
// table that still names another one fails by name.
func TestReleaseTargetsAreOnlyLinuxAmd64AndDarwinArm64(t *testing.T) {
	t.Parallel()

	if got := strings.Join(MustLoad().Architectures, ","); got != "amd64" {
		t.Errorf("canonical.json claims %q, want only amd64", got)
	}

	manifest, err := ReadReleaseManifest()
	if err != nil {
		t.Fatalf("read the release manifest: %v", err)
	}
	if got := strings.Join(manifest.ArchitectureSet(), ","); got != "amd64" {
		t.Errorf("container/release-manifest.json records %q, want only amd64", got)
	}

	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(Path(rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(data)
	}

	if got := regexp.MustCompile(`(?m)^platforms="([^"]*)"`).FindStringSubmatch(read("scripts/release/build-cli-tarballs.sh")); len(got) != 2 || got[1] != "linux/amd64 darwin/arm64" {
		t.Errorf("build-cli-tarballs.sh platforms = %q, want %q", got, "linux/amd64 darwin/arm64")
	}

	// Files whose release-facing content must not name another architecture.
	for _, rel := range []string{
		"scripts/bdtools/release/record_release_hashes.py",
		"scripts/bdtools/release/publish_image.py",
		"container/compose.yaml",
		"apps/casaos/compose/retnd.yml",
		"apps/zimaos/compose/retnd.yml",
		"apps/synology/spk/arch.go",
		"distribution/packaging/conformance.json",
	} {
		for n, line := range strings.Split(read(rel), "\n") {
			if strings.Contains(line, "arm64") && !strings.Contains(line, "darwin") && !strings.Contains(line, "1452") {
				t.Errorf("%s:%d still names arm64: %s", rel, n+1, strings.TrimSpace(line))
			}
		}
	}

	for _, rel := range []string{"docs/release-workflow.md", "docs/deployment.md", "docs/runtime-contract.md"} {
		if strings.Contains(read(rel), "linux/arm64") {
			t.Errorf("%s still documents a linux/arm64 release artefact", rel)
		}
	}

	if yml := read(".github/workflows/release.yml"); strings.Contains(yml, "amd64 and arm64") || strings.Contains(yml, "linux/arm64") {
		t.Error("release.yml still builds or names linux/arm64")
	}

	if py := read("scripts/install/install_docker_host.py"); regexp.MustCompile(`"(aarch64|arm64)":\s*"arm64"`).MatchString(py) {
		t.Error("install_docker_host.py SUPPORTED_ARCH still maps an ARM host to an image that is no longer published")
	}
}

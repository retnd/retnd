package agentboundary_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/tests/agentcorpus"
)

// The boundary drills that need no running engine (#1352, FR-96, UV.18,
// UV.19). EPIC U adds a client of /api/v1 and nothing else, so the container
// contract, the host runner and the privilege boundary ADR 0020 draws are
// byte-identical to what they were when the epic was measured, and the one
// flag that could carry a secret on a command line does not exist. The drills
// that need a serving engine (revoke mid-stream, expiry, the limiter, the two
// cross-credential refusals) are in apps/common/webhost.
//
// Written against the spec; not run.

const epicUBase = "9cd53e1f"

func root(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

// moved names the files of the canonical deployment that differ from base in
// dir, through scripts/release/contract-unchanged.sh: the reference compose
// file, the per-platform compose files, the Unraid templates, the TrueNAS
// catalog, the four packaging manifests and the capabilities package. That
// script is the rule check-epic-{n,t,u}-closed.sh share, so this drill and the
// release's closing check cannot disagree about what counts as a move. A
// release cut moves the image tag in every one of those files and
// canonical.json's release record, and none of that changes how a container
// runs (#1331, #1407), so a version bump alone is not a move here either.
// Anything else, an untracked file included, still is.
func moved(t *testing.T, dir, base string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(root(t), "scripts", "release", "contract-unchanged.sh"), base)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("contract-unchanged.sh %s: %v\n%s", base, err, errb.String())
	}
	var files []string
	for _, l := range strings.Split(out.String(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	return files
}

// boundary is the host runner and the ADR that fixes its privilege: the
// process, its socket protocol, its verb, and the decision itself.
var boundary = []string{
	"core/internal/hostrunner",
	"core/cmd/retnd/workflowrunner.go",
	"docs/adr/0020-host-workflow-runner.md",
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return out.String()
}

// changed lists every path under the given pathspecs that differs from base in
// the working tree, an untracked file counting as a change, because a new file
// in one of these places is exactly the "one reasonable-looking line" the
// drill exists to catch.
func changed(t *testing.T, dir, base string, specs []string) []string {
	t.Helper()
	var out []string
	diff := git(t, dir, append([]string{"diff", "--name-only", base, "--"}, specs...)...)
	untracked := git(t, dir, append([]string{"ls-files", "--others", "--exclude-standard", "--"}, specs...)...)
	for _, l := range strings.Split(diff+untracked, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func base() string {
	if b := os.Getenv("EPIC_U_BASE"); b != "" {
		return b
	}
	return epicUBase
}

func TestTheCanonicalDeploymentIsAnEmptyDiffAgainstTheEpicBase(t *testing.T) {
	if got := moved(t, root(t), base()); len(got) > 0 {
		t.Errorf("EPIC U moved the canonical deployment (compare %s):\n  %s", base(), strings.Join(got, "\n  "))
	}
}

func TestTheHostRunnerAndTheADR0020BoundaryAreByteIdentical(t *testing.T) {
	if got := changed(t, root(t), base(), boundary); len(got) > 0 {
		t.Errorf("EPIC U touched the host runner or ADR 0020 (compare %s):\n  %s", base(), strings.Join(got, "\n  "))
	}
}

// The control for both: a throwaway repository with the same layout, where a
// line added to the compose file, a file added beside it, and a line added to
// the runner each have to be seen, a release version bump alone must not be,
// and a clean tree has to be clean.
func TestTheCanonicalDeploymentDrill_WouldCatchOne(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("container/compose.yaml", "services:\n  serve:\n    image: ghcr.io/retnd/retnd:0.7.2\n    read_only: true\n")
	// Built from elements for the reason core/tests/deploymentdiff gives: a
	// literal "apps/<provider>/" in core reads as core importing that
	// provider to the provider-removal-preserves-core check (#1082).
	composeDir := strings.Join([]string{"apps", "generic", "compose"}, "/")
	write(composeDir+"/retnd.yaml", "services: {}\n")
	write("core/internal/hostrunner/runner.go", "package hostrunner\n")
	git(t, dir, "init", "-q")
	git(t, dir, "-c", "user.name=drill", "-c", "user.email=drill@example.invalid", "add", "-A")
	git(t, dir, "-c", "user.name=drill", "-c", "user.email=drill@example.invalid", "commit", "-qm", "base", "--no-verify")
	head := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))

	if got := moved(t, dir, head); len(got) != 0 {
		t.Fatalf("a clean tree reads as changed: %v", got)
	}
	write("container/compose.yaml", "services:\n  serve:\n    image: ghcr.io/retnd/retnd:0.8.0\n    read_only: true\n")
	if got := moved(t, dir, head); len(got) != 0 {
		t.Errorf("a release version bump alone reads as a move: %v", got)
	}
	write("container/compose.yaml", "services:\n  serve:\n    image: ghcr.io/retnd/retnd:0.8.0\n    read_only: true\n    volumes: [\"/var/run/docker.sock:/s\"]\n")
	if got := moved(t, dir, head); len(got) != 1 || got[0] != "container/compose.yaml" {
		t.Errorf("a mount added to the compose file reads as %v", got)
	}
	write(composeDir+"/extra.yaml", "services: {}\n")
	if got := moved(t, dir, head); len(got) != 2 {
		t.Errorf("an untracked file beside the compose files is not counted: %v", got)
	}
	write("core/internal/hostrunner/runner.go", "package hostrunner\n\nconst widen = true\n")
	if got := changed(t, dir, head, boundary); len(got) != 1 {
		t.Errorf("a line added to the runner reads as %v", got)
	}
}

// run starts the stdio verb with args and a closed stdin, so a verb that
// accepted its flags would start serving and then see EOF rather than hang.
func run(t *testing.T, bin string, env []string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader("")
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%v hung: %s", args, e.String())
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), o.String(), e.String()
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0, o.String(), e.String()
}

// A planted --token flag fails the usage guard (UV.19): exit 2, the flag
// refused by name, and the secret on that command line never echoed back.
func TestAPlantedTokenFlagFailsTheUsageGuard(t *testing.T) {
	bin := agentcorpus.BuildRetnd(t)
	config := agentcorpus.WriteConfig(t)
	secret := agentcorpus.Token()
	for _, args := range [][]string{
		{"agent", "serve", "--stdio", "--token", secret, "--config", config},
		{"agent", "serve", "--stdio", "--token=" + secret, "--config", config},
		{"agent", "serve", "--stdio", "-token", secret, "--config", config},
	} {
		code, stdout, stderr := run(t, bin, nil, args...)
		if code != 2 {
			t.Errorf("%v exited %d, want the usage code 2\n%s", args[3], code, stderr)
		}
		if strings.Contains(stdout+stderr, secret) {
			t.Errorf("%v echoed the token it was given:\n%s", args[3], stdout+stderr)
		}
	}
}

// The other half of the same guard: a token file anyone else can read is
// refused, and the verb does not serve on it.
func TestATokenFileOthersCanReadIsRefused(t *testing.T) {
	bin := agentcorpus.BuildRetnd(t)
	config := agentcorpus.WriteConfig(t)
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604} {
		file := filepath.Join(t.TempDir(), "agent.token")
		if err := os.WriteFile(file, []byte(agentcorpus.Token()+"\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(file, mode); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := run(t, bin, nil, "agent", "serve", "--stdio", "--token-file", file, "--config", config)
		if code == 0 {
			t.Errorf("mode %o: the verb accepted a token file others can read", mode)
		}
		if strings.Contains(stdout, `"jsonrpc"`) {
			t.Errorf("mode %o: the verb started speaking the protocol on a readable file", mode)
		}
		if !strings.Contains(stderr, "0600") && !strings.Contains(strings.ToLower(stderr), "mode") && !strings.Contains(strings.ToLower(stderr), "permission") {
			t.Errorf("mode %o: the refusal does not say why: %s", mode, stderr)
		}
	}
}

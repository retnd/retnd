package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/service"
)

// #1568: `settings observability rotate-scrape-token` is run as
// `... > scrape-token` to hand Prometheus its credentials_file, so standard
// output is the token and nothing else. The mode announcement and the FR-23
// startup lines went there beside it, and a file written that way was a
// credential Prometheus could not use.
//
// The plain form is driven as a child process, so what is asserted is what
// lands on the real file descriptors when a shell redirects them. The --json
// forms are held to #1152's rule, one document on standard output.

const (
	cliChild1568Env  = "RETND_TEST_CLI_CHILD_1568"
	cliChild1568Args = "RETND_TEST_CLI_CHILD_1568_ARGS"
)

// TestCLIChildProcess1568 is not a test. It is the entry point the test below
// re-executes this binary into, and it skips itself in an ordinary run.
func TestCLIChildProcess1568(t *testing.T) {
	if os.Getenv(cliChild1568Env) != "1" {
		t.Skip("child-process entry point: only runs when a parent test re-executes this binary")
	}
	os.Exit(run(strings.Split(os.Getenv(cliChild1568Args), "\n")))
}

func TestSettingsObservability_1568_RotateScrapeTokenStdoutIsExactlyTheToken(t *testing.T) {
	configPath, _ := workflowFixture(t)

	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIChildProcess1568$")
	cmd.Env = append(os.Environ(),
		cliChild1568Env+"=1",
		cliChild1568Args+"="+strings.Join([]string{"settings", "observability", "rotate-scrape-token", "--config", configPath}, "\n"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("rotate-scrape-token: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	if !regexp.MustCompile(`\Arsc_[A-Za-z0-9_-]{43}\n\z`).Match(stdout.Bytes()) {
		t.Fatalf("stdout is not exactly the token and a newline, so `> scrape-token` writes a credential Prometheus cannot use:\n%q\nstderr:\n%s", stdout.String(), stderr.String())
	}
	token := strings.TrimSuffix(stdout.String(), "\n")
	if !strings.Contains(stderr.String(), "scrape token minted") {
		t.Errorf("the status line belongs on stderr, where an operator still sees it; stderr:\n%s", stderr.String())
	}

	// And what stdout carries is the credential the endpoint verifies.
	svc, cleanup, err := service.Open(context.Background(), configPath)
	if err != nil {
		t.Fatalf("service.Open: %v", err)
	}
	defer func() { _ = cleanup() }()
	if !svc.VerifyScrapeToken(token) {
		t.Errorf("the token printed on stdout is not the one the service verifies")
	}
}

func TestSettingsObservability_1568_JSONFormsPrintOneDocument(t *testing.T) {
	configPath, _ := workflowFixture(t)

	t.Run("rotate-scrape-token --json", func(t *testing.T) {
		out, code := cliJSON(t, "settings", "observability", "rotate-scrape-token", "--json", "--config", configPath)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		var minted apicontract.MintedScrapeToken
		strictDecode(t, out, &minted)
		if !strings.HasPrefix(minted.Token, "rsc_") {
			t.Errorf("the document carries no rsc_ token: %+v", minted)
		}
	})
	t.Run("patch --json", func(t *testing.T) {
		out, code := cliJSON(t, "settings", "observability", "patch", "--metrics-enabled", "--json", "--config", configPath)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		var s apicontract.ObservabilitySettingsResponse
		strictDecode(t, out, &s)
		if !s.Metrics.Enabled {
			t.Errorf("the patch document says metrics are off: %+v", s.Metrics)
		}
	})
	t.Run("show --json", func(t *testing.T) {
		out, code := cliJSON(t, "settings", "observability", "--json", "--config", configPath)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		var s apicontract.ObservabilitySettingsResponse
		strictDecode(t, out, &s)
	})
}

// Beside a serving engine the engine mints, and the mode announcement that
// says so is the line that used to land on stdout in front of the token.
func TestSettingsObservability_1568_RotateScrapeTokenBesideAnEngineStdoutIsExactlyTheToken(t *testing.T) {
	cliConfig, engine := routedFixture(t)
	engine.handle("POST /settings/observability/scrape-token", func(w http.ResponseWriter, r *http.Request) {
		minted, err := engine.svc.MintScrapeToken(r.Context())
		if err != nil {
			refuseServiceError(w, "mintScrapeToken", err)
			return
		}
		writeJSON(w, http.StatusCreated, apicontract.MintedScrapeToken{
			Token:       minted.Token,
			ScrapeToken: apicontract.ScrapeTokenState{Exists: minted.State.Exists, Prefix: minted.State.Prefix},
		})
	})

	var code int
	out := captureStdout(t, func() {
		code = run([]string{"settings", "observability", "rotate-scrape-token", "--config", cliConfig})
	})
	if code != 0 {
		t.Fatalf("rotate-scrape-token beside an engine = %d, want 0; stdout:\n%s", code, out)
	}
	if !slices.Contains(engine.requests(), "POST /settings/observability/scrape-token") {
		t.Fatalf("the engine never saw the mint, so this did not test the attached route: %q", engine.requests())
	}
	if !regexp.MustCompile(`\Arsc_[A-Za-z0-9_-]{43}\n\z`).MatchString(out) {
		t.Fatalf("stdout is not exactly the token and a newline:\n%q", out)
	}
	if !engine.svc.VerifyScrapeToken(strings.TrimSuffix(out, "\n")) {
		t.Errorf("the token printed on stdout is not the one the engine verifies")
	}
}

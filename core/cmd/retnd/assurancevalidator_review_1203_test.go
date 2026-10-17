package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// #1203 review: the terminal noun, what an update may leave out, and which
// process a read asks.

func TestAssuranceValidators_TheOldSingularNounIsNotAVerb(t *testing.T) {
	configPath, _ := writeValidatorTestConfig(t)
	if code := run([]string{"assurance", "validator", "list", "--config", configPath}); code != exitUsage {
		t.Errorf("`assurance validator` exit %d, want usage %d: the route and the issue say validators", code, exitUsage)
	}
	if code := run([]string{"assurance", "validators", "list", "--config", configPath}); code != exitOK {
		t.Errorf("`assurance validators list` exit %d", code)
	}
}

func TestAssuranceValidators_AnUpdateSaysWhereItGoes(t *testing.T) {
	configPath, _ := writeValidatorTestConfig(t)
	script := writeScriptFixture(t, goodCLIValidator)
	if code := run([]string{"assurance", "validators", "add", "v", "--pattern", "^a", "--backup-set", cliSet, "--script", script, "--config", configPath}); code != exitOK {
		t.Fatalf("add exit %d", code)
	}
	before, _ := os.ReadFile(configPath)

	// Leaving the scope out used to widen a set-scoped validator to every set.
	for _, argv := range [][]string{
		{"assurance", "validators", "update", "v", "--pattern", "^b"},
		{"assurance", "validators", "update", "v", "--pattern", "^b", "--backup-set", cliSet, "--global"},
	} {
		if code := run(append(argv, "--config", configPath)); code != exitUsage {
			t.Errorf("%v exit %d, want usage %d", argv, code, exitUsage)
		}
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Error("a refused update changed the configuration")
	}

	if code := run([]string{"assurance", "validators", "update", "v", "--pattern", "^b", "--global", "--config", configPath}); code != exitOK {
		t.Fatalf("update --global exit %d", code)
	}
	raw, _ := os.ReadFile(configPath)
	if !strings.Contains(string(raw), "pattern: ^b") || strings.Contains(string(raw), "backup_set: "+cliSet) {
		t.Errorf("--global did not make it global:\n%s", raw)
	}
	if code := run([]string{"assurance", "validators", "update", "v", "--pattern", "^c", "--backup-set", cliSet, "--config", configPath}); code != exitOK {
		t.Fatalf("update --backup-set exit %d", code)
	}
	raw, _ = os.ReadFile(configPath)
	if !strings.Contains(string(raw), "backup_set: "+cliSet) {
		t.Errorf("--backup-set did not scope it:\n%s", raw)
	}
}

func TestAssuranceValidators_ReadsAskTheServingEngineNotThisHostsFiles(t *testing.T) {
	cliConfig, engine := routedValidatorFixture(t)
	if _, err := engine.svc.CreateCustomValidator(t.Context(), service.CreateCustomValidatorRequest{
		Name: "engine-only", Pattern: `^E-`, Script: goodCLIValidator,
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		argv    []string
		request string
		want    string
	}{
		{"list", []string{"assurance", "validators", "list", "--json"}, "GET /assurance/validators", `"engine-only"`},
		{"script", []string{"assurance", "validators", "script", "engine-only"}, "GET /assurance/validators/engine-only/script", "RECOVERY_TEST_FILE"},
		{"patterns", []string{"assurance", "validators", "patterns", "--json"}, "GET /assurance/validator-patterns", `"patterns"`},
		{"dry run", []string{"assurance", "validators", "add", "--pattern", `^E-`, "--dry-run", "--json"}, "POST /assurance/validator-preview", `"valid": true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stepUpSeen.Delete(tc.request)
			var code int
			out := captureStdout(t, func() { code = run(withConfig(tc.argv, cliConfig)) })
			if code != exitOK {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if _, asked := stepUpSeen.Load(tc.request); !asked {
				t.Errorf("the engine never saw %s: the read answered from this host's own files", tc.request)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("output does not carry the engine's answer %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestAssuranceValidators_AnUpdateWithoutAScriptKeepsTheEnginesScriptByHash(t *testing.T) {
	cliConfig, engine := routedValidatorFixture(t)
	created, err := engine.svc.CreateCustomValidator(t.Context(), service.CreateCustomValidatorRequest{Name: "kept", Pattern: `^K-`, Script: goodCLIValidator})
	if err != nil {
		t.Fatal(err)
	}
	var code int
	stderr := captureStderr(t, func() {
		code = captureStdoutCode(t, func() int {
			return run(withConfig([]string{"assurance", "validators", "update", "kept", "--pattern", `^L-`, "--global"}, cliConfig))
		})
	})
	if code != exitOK {
		t.Fatalf("exit %d\nstderr: %s", code, stderr)
	}
	got, _ := engine.svc.CustomValidatorScript(t.Context(), "kept")
	if got.SHA256 != created.ScriptSHA256 {
		t.Error("the engine's script changed")
	}
	list, _ := engine.svc.ListCustomValidators(t.Context(), service.CustomValidatorsFilter{})
	if len(list.Validators) != 1 || list.Validators[0].Pattern != `^L-` {
		t.Errorf("the engine holds %+v", list.Validators)
	}
}

func TestPrintCustomValidatorList_WarnsAboutAmbiguitiesAndARefusedDirectory(t *testing.T) {
	var buf bytes.Buffer
	printCustomValidatorList(&buf, service.CustomValidators{
		Available: true, RunnerConfigured: true,
		DirectoryRefused: "the hook directory has permissions 0777",
		Ambiguities: []service.CustomValidatorAmbiguity{
			{BackupSetID: "src/set-1", Validators: []string{"a", "b"}, Example: "src/set-1/A-1.txt"},
		},
	})
	out := buf.String()
	for _, want := range []string{"directory refused", "permissions 0777", "ambiguous", "a, b", "src/set-1/A-1.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing does not say %q:\n%s", want, out)
		}
	}
}

// A backup name is whatever the remote called it, and a pattern is whatever
// somebody typed. Neither gets to move the cursor or repaint the screen.
func TestPrintCustomValidatorList_NeutralisesControlCharacters(t *testing.T) {
	var buf bytes.Buffer
	printCustomValidatorList(&buf, service.CustomValidators{
		Available: true, RunnerConfigured: true,
		Validators: []service.CustomValidator{{
			Name: "v", Pattern: "^a\x1b[2J", BackupSetID: "src/set\r1", ScriptFile: "v.local.sh", ScriptPresent: true,
			Matches: 1, MatchSets: []string{"src/set\r1"}, ExampleName: "evil\nname\x07\x1b]0;title\x07",
		}},
		Ambiguities: []service.CustomValidatorAmbiguity{{BackupSetID: "s/s", Validators: []string{"a", "b"}, Example: "s/s/x\x1b[31m"}},
	})
	for _, r := range buf.String() {
		if r < 0x20 && r != '\n' || r == 0x7f {
			t.Fatalf("a control character %q reached the terminal:\n%q", r, buf.String())
		}
	}
	if strings.Contains(buf.String(), "evil\nname") {
		t.Errorf("a newline inside a backup name split the listing:\n%q", buf.String())
	}
}

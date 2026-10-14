package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// Issue #1148 (S1.3): eight configuration writes were refused with exit 3
// whenever an engine was serving, although the API routes for every one
// of them exist:
//
//	settings workflow patch
//	settings workflow env set | unset
//	backup-set workflow patch
//	backup-set workflow env set | unset
//	backup-set retention (set a policy) | --inherit
//
// So on any live deployment none of them worked. These tests drive the
// real CLI against a fake engine that serves the real service layer
// (fakeengine_test.go) and assert the change LANDED THROUGH THE API: the
// engine's configuration revision moves, the engine holds the change, the
// request the engine saw is the one the route table names, and the CLI's
// own config.yaml is byte for byte what it was. The offline path (nothing
// serving) is pinned beside them so routing cannot cost it.

// routedFixture is a CLI configuration with a workflow root, an engine
// serving a second file for the same deployment that has the same root,
// and the CLI pointed at that engine.
func routedFixture(t *testing.T) (cliConfig string, engine *fakeEngine) {
	t.Helper()
	cliConfig, root := workflowFixture(t)

	engineConfig := writeTestConfigFor(t, cliConfig)
	raw, err := os.ReadFile(engineConfig)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	block := "workflows:\n" +
		"  root: " + root + "\n" +
		"  global:\n" +
		"    before_dir: global-before\n" +
		"    after_dir: global-after\n" +
		"  script_timeout: 2m\n"
	if err := os.WriteFile(engineConfig, append(raw, []byte(block)...), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	engine = startFakeEngine(t, engineConfig)
	engine.attach(t)
	return cliConfig, engine
}

type configWriteRow struct {
	name string
	// argv is the command after the verb group, with --config appended by
	// the runner.
	argv []string
	// request is the "METHOD path" the engine must have seen.
	request string
	// seed prepares the engine's own state before the CLI runs.
	seed func(t *testing.T, e *fakeEngine)
	// landed asserts the engine now holds the change.
	landed func(t *testing.T, e *fakeEngine)
}

func configWriteRows() []configWriteRow {
	return []configWriteRow{
		{
			name:    "settings workflow patch",
			argv:    []string{"settings", "workflow", "patch", "--script-timeout", "3m"},
			request: "PATCH /settings/workflow",
			landed: func(t *testing.T, e *fakeEngine) {
				got, err := e.svc.WorkflowSettings(t.Context())
				if err != nil {
					t.Fatalf("WorkflowSettings: %v", err)
				}
				if got.ScriptTimeout.String() != "3m0s" {
					t.Errorf("the engine's workflow script timeout is %s, and the CLI patched it to 3m", got.ScriptTimeout)
				}
			},
		},
		{
			name:    "settings workflow env set",
			argv:    []string{"settings", "workflow", "env", "set", "PGDATABASE", "--value", "prod"},
			request: "PUT /settings/workflow/environment/PGDATABASE",
			landed: func(t *testing.T, e *fakeEngine) {
				vars, err := e.svc.ListWorkflowEnv(t.Context(), "")
				if err != nil {
					t.Fatalf("ListWorkflowEnv: %v", err)
				}
				if len(vars) != 1 || vars[0].Name != "PGDATABASE" || vars[0].Value != "prod" {
					t.Errorf("the engine's deployment environment is %+v, and the CLI set PGDATABASE=prod", vars)
				}
			},
		},
		{
			name: "settings workflow env unset",
			argv: []string{"settings", "workflow", "env", "unset", "PGDATABASE"},
			seed: func(t *testing.T, e *fakeEngine) {
				if _, err := e.svc.SetWorkflowEnv(t.Context(), "", service.WorkflowEnvVar{Name: "PGDATABASE", Value: "prod", HasValue: true}); err != nil {
					t.Fatalf("seeding the engine's environment: %v", err)
				}
			},
			request: "DELETE /settings/workflow/environment/PGDATABASE",
			landed: func(t *testing.T, e *fakeEngine) {
				vars, _ := e.svc.ListWorkflowEnv(t.Context(), "")
				if len(vars) != 0 {
					t.Errorf("the engine still holds %+v, and the CLI unset PGDATABASE", vars)
				}
			},
		},
		{
			name:    "backup-set workflow patch",
			argv:    []string{"backup-set", "workflow", "patch", cliSet, "--script-timeout", "90s"},
			request: "PATCH /backup-sets/" + cliSet + "/workflow",
			landed: func(t *testing.T, e *fakeEngine) {
				got, err := e.svc.BackupSetWorkflow(t.Context(), cliSet)
				if err != nil {
					t.Fatalf("BackupSetWorkflow: %v", err)
				}
				if got.ScriptTimeout.String() != "1m30s" {
					t.Errorf("the engine's pinned timeout for %s is %s, and the CLI pinned 90s", cliSet, got.ScriptTimeout)
				}
			},
		},
		{
			name:    "backup-set workflow env set",
			argv:    []string{"backup-set", "workflow", "env", cliSet, "set", "LANG", "--value", "C"},
			request: "PUT /backup-sets/" + cliSet + "/workflow/environment/LANG",
			landed: func(t *testing.T, e *fakeEngine) {
				vars, err := e.svc.ListWorkflowEnv(t.Context(), cliSet)
				if err != nil {
					t.Fatalf("ListWorkflowEnv: %v", err)
				}
				if len(vars) != 1 || vars[0].Name != "LANG" || vars[0].Value != "C" {
					t.Errorf("the engine's environment for %s is %+v, and the CLI set LANG=C", cliSet, vars)
				}
			},
		},
		{
			name: "backup-set workflow env unset",
			argv: []string{"backup-set", "workflow", "env", cliSet, "unset", "LANG"},
			seed: func(t *testing.T, e *fakeEngine) {
				if _, err := e.svc.UpdateBackupSetWorkflow(t.Context(), cliSet, service.UpdateBackupSetWorkflowRequest{BeforeDir: ptr("set-before")}); err != nil {
					t.Fatalf("seeding the engine's set workflow: %v", err)
				}
				if _, err := e.svc.SetWorkflowEnv(t.Context(), cliSet, service.WorkflowEnvVar{Name: "LANG", Value: "C", HasValue: true}); err != nil {
					t.Fatalf("seeding the engine's set environment: %v", err)
				}
			},
			request: "DELETE /backup-sets/" + cliSet + "/workflow/environment/LANG",
			landed: func(t *testing.T, e *fakeEngine) {
				vars, _ := e.svc.ListWorkflowEnv(t.Context(), cliSet)
				if len(vars) != 0 {
					t.Errorf("the engine still holds %+v for %s, and the CLI unset LANG", vars, cliSet)
				}
			},
		},
		{
			name:    "backup-set retention set",
			argv:    []string{"backup-set", "retention", cliSet, "--daily-days", "30", "--weekly-months", "6", "--monthly-months", "12"},
			request: "PUT /backup-sets/" + cliSet + "/retention",
			landed: func(t *testing.T, e *fakeEngine) {
				got, err := e.svc.BackupSetRetention(t.Context(), cliSet)
				if err != nil {
					t.Fatalf("BackupSetRetention: %v", err)
				}
				if !got.IsOverride || got.Override == nil || got.Override.DailyDays != 30 || got.Override.WeeklyMonths != 6 || got.Override.MonthlyMonths != 12 {
					t.Errorf("the engine holds %+v for %s, and the CLI gave it a 30/6/12 policy of its own", got, cliSet)
				}
			},
		},
		{
			name: "backup-set retention inherit",
			argv: []string{"backup-set", "retention", cliSet, "--inherit"},
			seed: func(t *testing.T, e *fakeEngine) {
				if _, err := e.svc.SetBackupSetRetention(t.Context(), cliSet, service.RetentionOverride{DailyDays: 30, WeeklyMonths: 6, MonthlyMonths: 12}); err != nil {
					t.Fatalf("seeding the engine's override: %v", err)
				}
			},
			request: "DELETE /backup-sets/" + cliSet + "/retention",
			landed: func(t *testing.T, e *fakeEngine) {
				got, err := e.svc.BackupSetRetention(t.Context(), cliSet)
				if err != nil {
					t.Fatalf("BackupSetRetention: %v", err)
				}
				if got.IsOverride {
					t.Errorf("the engine still holds an override for %s, and the CLI asked it to inherit", cliSet)
				}
			},
		},
	}
}

func ptr[T any](v T) *T { return &v }

// withConfig puts --config after the verb group so the same row works for
// every spelling.
func withConfig(argv []string, cfg string) []string {
	return append(append([]string{}, argv...), "--config", cfg)
}

// TestEngineRefusedConfigWritesLandThroughTheEngine_1148 is the report:
// each of the eight verbs, with an engine serving, exits 0, reaches the
// engine by the route the table names, moves the engine's configuration
// revision, and leaves the CLI's own file alone.
func TestEngineRefusedConfigWritesLandThroughTheEngine_1148(t *testing.T) {
	for _, row := range configWriteRows() {
		t.Run(row.name, func(t *testing.T) {
			cliConfig, engine := routedFixture(t)
			if row.seed != nil {
				row.seed(t, engine)
			}

			beforeCLI := readFile(t, cliConfig)
			revisionBefore := engine.svc.ConfigRevision()

			var code int
			stderr := captureStderr(t, func() {
				code = captureStdoutCode(t, func() int { return run(withConfig(row.argv, cliConfig)) })
			})
			if code != 0 {
				t.Fatalf("%s beside a serving engine exited %d, want 0 (it was refused with exit 3 before #1148)\nstderr: %s", row.name, code, stderr)
			}
			if !containsRequest(engine.requests(), row.request) {
				t.Errorf("the engine never saw %q; it saw %v", row.request, engine.requests())
			}
			if engine.svc.ConfigRevision() == revisionBefore {
				t.Error("the engine's config_revision did not move, so the change did not land through the API")
			}
			if after := readFile(t, cliConfig); after != beforeCLI {
				t.Error("the CLI's own config.yaml changed as well as the engine's")
			}
			row.landed(t, engine)
		})
	}
}

// TestConfigWritesStillWorkWithNothingServing_1148 pins the offline path:
// routing must not cost it. Exit 0 and the file holds the change.
func TestConfigWritesStillWorkWithNothingServing_1148(t *testing.T) {
	for _, row := range configWriteRows() {
		t.Run(row.name, func(t *testing.T) {
			clearInheritedRouteSettings()
			cliConfig, _ := workflowFixture(t)
			if row.seed != nil {
				seedOffline(t, cliConfig, row.name)
			}
			before := readFile(t, cliConfig)

			var code int
			stderr := captureStderr(t, func() {
				code = captureStdoutCode(t, func() int { return run(withConfig(row.argv, cliConfig)) })
			})
			if code != 0 {
				t.Fatalf("%s with nothing serving exited %d, want 0\nstderr: %s", row.name, code, stderr)
			}
			if readFile(t, cliConfig) == before {
				t.Errorf("%s with nothing serving left config.yaml untouched", row.name)
			}
		})
	}
}

// seedOffline applies a row's seed to a file-backed service directly.
func seedOffline(t *testing.T, cliConfig, name string) {
	t.Helper()
	svc, closeFn, err := service.Open(t.Context(), cliConfig)
	if err != nil {
		t.Fatalf("opening %s: %v", cliConfig, err)
	}
	defer func() { _ = closeFn() }()
	e := &fakeEngine{svc: svc}
	for _, row := range configWriteRows() {
		if row.name == name && row.seed != nil {
			row.seed(t, e)
		}
	}
}

// TestKeepsExit3WhenNoRouteIsNamed_1148: the refusal survives for an
// operator who named no engine. Only the route changes the answer.
func TestKeepsExit3WhenNoRouteIsNamed_1148(t *testing.T) {
	for _, row := range configWriteRows() {
		t.Run(row.name, func(t *testing.T) {
			clearInheritedRouteSettings()
			cliConfig, _ := workflowFixture(t)
			attachEngineTo(t, cliConfig)
			if row.seed != nil {
				// Seeding needs the file's own service, which the
				// announcement above does not stop (it only refuses writes
				// that come through the CLI's door).
				seedOffline(t, cliConfig, row.name)
			}
			before := readFile(t, cliConfig)

			var code int
			captureStderr(t, func() {
				code = captureStdoutCode(t, func() int { return run(withConfig(row.argv, cliConfig)) })
			})
			if code != 3 {
				t.Errorf("%s with an engine serving and no route named exited %d, want 3", row.name, code)
			}
			if readFile(t, cliConfig) != before {
				t.Error("a refused write changed config.yaml")
			}
		})
	}
}

// lastJSONObject returns the final pretty-printed object on stdout, which
// is how a script reads a write's --json (settingsworkflow.go's preamble:
// the mode line and FR-23's startup events come first).
func lastJSONObject(t *testing.T, stdout string) map[string]any {
	t.Helper()
	lines := strings.Split(stdout, "\n")
	start := -1
	for i, l := range lines {
		if l == "{" {
			start = i
		}
	}
	if start < 0 {
		t.Fatalf("no JSON object on stdout:\n%s", stdout)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.Join(lines[start:], "\n")), &out); err != nil {
		t.Fatalf("the last object on stdout is not JSON: %v\n%s", err, stdout)
	}
	return out
}

func jsonRows() []configWriteRow {
	var out []configWriteRow
	for _, r := range configWriteRows() {
		r.argv = append(append([]string{}, r.argv...), "--json")
		out = append(out, r)
	}
	return out
}

// TestConfigWriteJSONIsTheWireObject_1148: --json on a routed write is
// the object the API returned for that call, field for field.
func TestConfigWriteJSONIsTheWireObject_1148(t *testing.T) {
	for _, row := range jsonRows() {
		t.Run(row.name, func(t *testing.T) {
			cliConfig, engine := routedFixture(t)
			if row.seed != nil {
				row.seed(t, engine)
			}
			var code int
			var stdout string
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() { code = run(withConfig(row.argv, cliConfig)) })
			})
			if code != 0 {
				t.Fatalf("exit %d\nstderr: %s", code, stderr)
			}
			raw := engine.answered(row.request)
			if raw == nil {
				t.Fatalf("the engine recorded no answer for %q", row.request)
			}
			var want map[string]any
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatalf("the engine's answer is not JSON: %v", err)
			}
			got := lastJSONObject(t, stdout)
			if !reflect.DeepEqual(got, want) {
				g, _ := json.MarshalIndent(got, "", " ")
				w, _ := json.MarshalIndent(want, "", " ")
				t.Errorf("--json is not the wire object.\nCLI printed:\n%s\nAPI returned:\n%s", g, w)
			}
		})
	}
}

// TestConfigWriteJSONOfflineHasTheWireShape_1148: with nothing serving the
// same flag prints the same shape, so a script does not have to know
// which world it ran in. Compared against what the wire projection of the
// resulting state is.
func TestConfigWriteJSONOfflineHasTheWireShape_1148(t *testing.T) {
	for _, row := range jsonRows() {
		t.Run(row.name, func(t *testing.T) {
			clearInheritedRouteSettings()
			cliConfig, _ := workflowFixture(t)
			if row.seed != nil {
				seedOffline(t, cliConfig, row.name)
			}
			var code int
			var stdout string
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() { code = run(withConfig(row.argv, cliConfig)) })
			})
			if code != 0 {
				t.Fatalf("exit %d\nstderr: %s", code, stderr)
			}
			got := lastJSONObject(t, stdout)

			svc, closeFn, err := service.Open(t.Context(), cliConfig)
			if err != nil {
				t.Fatalf("reopening: %v", err)
			}
			defer func() { _ = closeFn() }()
			want := wireOfState(t, svc, row.name)
			if !reflect.DeepEqual(got, want) {
				g, _ := json.MarshalIndent(got, "", " ")
				w, _ := json.MarshalIndent(want, "", " ")
				t.Errorf("offline --json is not the wire shape.\nCLI printed:\n%s\nwire projection of the state:\n%s", g, w)
			}
		})
	}
}

// wireOfState is the wire projection, through the fake engine's own
// mirror of the handlers, of the state a row's verb leaves behind.
func wireOfState(t *testing.T, svc *service.BackupService, name string) map[string]any {
	t.Helper()
	var v any
	switch {
	case strings.HasPrefix(name, "settings workflow env"):
		vars, _ := svc.ListWorkflowEnv(t.Context(), "")
		v = envListOf("", vars)
	case strings.HasPrefix(name, "backup-set workflow env"):
		vars, _ := svc.ListWorkflowEnv(t.Context(), cliSet)
		v = envListOf(cliSet, vars)
	case strings.HasPrefix(name, "settings workflow patch"):
		s, _ := svc.WorkflowSettings(t.Context())
		v = fakeWorkflowSettingsBody(s)
	case strings.HasPrefix(name, "backup-set workflow patch"):
		s, _ := svc.BackupSetWorkflow(t.Context(), cliSet)
		v = fakeBackupSetWorkflowBody(s)
	case strings.HasPrefix(name, "backup-set retention"):
		s, _ := svc.BackupSetRetention(t.Context(), cliSet)
		v = fakeRetentionBody(s)
	default:
		t.Fatalf("no projection for %q", name)
	}
	raw, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}


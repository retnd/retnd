package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// #1319 (T2.4), the CLI half of EPIC T's equivalence row: the suite verbs
// (suites list, policy patch with a suite, key create and rotate with a
// suite, the re-encryption dry run) driven through the direct route and
// through an engine (the API) leave the same configuration and the same key
// registry, answer with the same object and exit with the same code, step by
// step. The flow is the rotate-across-suites one an EPIC N deployment takes
// on upgrade: age/v1 key, policy moved to age-pq/v1 (told to rotate),
// rotation, dry run over the old suite's copies, and the registry read back.
// The browser half is ui/shared/e2e/1319-suite-equivalence.spec.ts.
//
// Written by reading the verbs in encryption.go and the fake engine; not
// run. Each names the defect that turns it red.

var suiteFlow = []writeStep{
	{"list the suites before anything is written", []string{"encryption", "suites", "--json"}},
	{"create an age/v1 key, as an EPIC N deployment has", []string{"encryption", "key", "create", "--suite", "age/v1", "--json"}},
	{"move the policy to age-pq/v1 with no key under it", []string{"encryption", "policy", "patch", "--mode", "required", "--managed", "--suite", "age-pq/v1", "--json"}},
	{"rotate across suites", []string{"encryption", "key", "rotate", "--suite", "age-pq/v1", "--json"}},
	{"dry-run a re-encryption of the old suite's copies", []string{"encryption", "reencrypt", "--suite", "age/v1", "--dry-run", "--json"}},
	{"list the suites after the rotation", []string{"encryption", "suites", "--json"}},
	{"read the key registry back", []string{"encryption", "key", "list", "--json"}},
}

var keyIDShape = regexp.MustCompile(`^k[0-9a-f]{16}$`)

// deepScrub replaces every key id with a placeholder and drops every instant,
// at any depth, so two deployments' answers compare on what they say rather
// than on ids and clocks no two of them share. Suite ids, statuses, counts,
// flags and sentences are kept: they are what the row is about.
func deepScrub(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if strings.HasSuffix(k, "_at") || k == "created" || k == "exported" {
				continue
			}
			out[k] = deepScrub(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deepScrub(e)
		}
		return out
	case string:
		if keyIDShape.MatchString(x) {
			return "<key>"
		}
		return x
	}
	return v
}

type suiteRun struct {
	code   int
	answer any
	config string
}

func runSuiteFlow(t *testing.T, steps []writeStep, configPath, writtenConfig string, dirs ...string) []suiteRun {
	t.Helper()
	var runs []suiteRun
	for _, s := range steps {
		args := append(append([]string{}, s.args...), "--config", configPath)
		code, out, errOut := cli996(t, args...)
		r := suiteRun{code: code, config: normalised(readFile(t, writtenConfig), dirs...)}
		if code != exitOK {
			t.Errorf("%s exited %d: %s", s.name, code, errOut)
		} else {
			var v any
			if err := json.Unmarshal([]byte(out), &v); err != nil {
				t.Fatalf("%s: the --json answer is not JSON: %v\n%s", s.name, err, out)
			}
			r.answer = deepScrub(v)
		}
		runs = append(runs, r)
	}
	return runs
}

func TestEverySuiteVerbAnswersAndWritesTheSameOnBothRoutes_1319(t *testing.T) {
	clearTheRoute(t)

	direct := writeTestConfig(t)
	directRuns := runSuiteFlow(t, suiteFlow, direct, direct, filepath.Dir(direct))

	// The engine's file sits beside the CLI's, not in a directory of its own
	// the way startFakeEngineFor puts it. Managed key files live beside the
	// configuration (keycustody.DirBeside), so they are part of the
	// deployment the way the journal is: with the engine in its own
	// directory the routed create and rotate wrote the key files there, and
	// the direct reads below (key list, the dry run) looked for them beside
	// the CLI's file and reported both keys unreachable. A real CLI and
	// engine share one config.yaml, so one directory is the truthful
	// arrangement; the second file name is still what lets the engine route's
	// writes be watched apart from the CLI's.
	cliSide := writeTestConfig(t)
	engineConfig := filepath.Join(filepath.Dir(cliSide), "engine.yaml")
	if err := os.WriteFile(engineConfig, []byte(readFile(t, cliSide)), 0o644); err != nil {
		t.Fatal(err)
	}
	engine := startFakeEngine(t, engineConfig)
	engine.attach(t)
	engineRuns := runSuiteFlow(t, suiteFlow, cliSide, engine.configPath, filepath.Dir(cliSide))

	if len(directRuns) != len(suiteFlow) || len(engineRuns) != len(suiteFlow) {
		t.Fatalf("ran %d direct and %d engine steps, want %d each", len(directRuns), len(engineRuns), len(suiteFlow))
	}
	for i, s := range suiteFlow {
		d, e := directRuns[i], engineRuns[i]
		if d.code != e.code {
			t.Errorf("%s: exit %d directly and %d through the engine", s.name, d.code, e.code)
		}
		if !reflect.DeepEqual(d.answer, e.answer) {
			t.Errorf("%s: the two routes answered different objects\ndirect: %v\nengine: %v", s.name, d.answer, e.answer)
		}
		if d.config != e.config {
			t.Errorf("%s: the two routes left different configuration\ndirect:\n%s\nengine:\n%s", s.name, d.config, e.config)
		}
	}

	// The rows carry what they claim, so equality is not two empty answers.
	policy, _ := directRuns[2].answer.(map[string]any)
	if policy["rotation_needed"] != true || !strings.Contains(asString(policy["rotate_command"]), "rotate --suite age-pq/v1") {
		t.Errorf("the policy patch to a suite with no key did not say to rotate: %v", policy)
	}
	if !strings.Contains(directRuns[2].config, "suite: age-pq/v1") {
		t.Errorf("the policy patch did not write the suite:\n%s", directRuns[2].config)
	}
	keys, _ := directRuns[6].answer.(map[string]any)
	if got := suitesByStatus(keys); !reflect.DeepEqual(got, map[string]string{"age/v1": "superseded", "age-pq/v1": "active"}) {
		t.Errorf("after the rotation the registry reads %v, want age/v1 superseded and age-pq/v1 active", got)
	}

	// The writes took the engine route (the fake records paths, not the
	// ?suite= query; the key list comparison above is what shows the suite
	// arrived). The reads (suites, the re-encryption dry run, key
	// list) read the deployment directly on both routes, over the one
	// journal both configurations name, so they compare the state the
	// routed writes left.
	var sawCreate, sawRotate, sawPolicy bool
	for _, req := range engine.requests() {
		sawCreate = sawCreate || req == "POST /encryption/keys"
		sawRotate = sawRotate || req == "POST /encryption/keys/rotate"
		sawPolicy = sawPolicy || strings.Contains(req, "/encryption/policy")
	}
	if !sawCreate || !sawRotate || !sawPolicy {
		t.Errorf("the engine did not see the create, rotate and policy writes: %v", engine.requests())
	}
	if got := readFile(t, cliSide); strings.Contains(got, "retained_copy_encryption") {
		t.Errorf("a routed write changed the CLI's own configuration:\n%s", got)
	}
}

// The comparison can fail: a deployment that skipped the rotation and one
// that did not differ in the registry read, in the suites listing and in the
// dry run. Red if deepScrub flattens the answers that carry the difference.
func TestTheSuiteEquivalenceComparisonCanActuallyFail_1319(t *testing.T) {
	clearTheRoute(t)
	a, b := writeTestConfig(t), writeTestConfig(t)
	withRotation := runSuiteFlow(t, suiteFlow, a, a, filepath.Dir(a))
	without := append(append([]writeStep{}, suiteFlow[:3]...), suiteFlow[4:]...)
	noRotation := runSuiteFlow(t, without, b, b, filepath.Dir(b))
	if reflect.DeepEqual(withRotation[len(withRotation)-1].answer, noRotation[len(noRotation)-1].answer) {
		t.Fatal("a registry with a rotation across suites and one without compare equal after scrubbing")
	}
	if reflect.DeepEqual(withRotation[5].answer, noRotation[4].answer) {
		t.Fatal("the suites listing after a rotation and without one compare equal after scrubbing")
	}
}

func asString(v any) string { s, _ := v.(string); return s }

// suitesByStatus reads `key list --json` as suite id -> status.
func suitesByStatus(answer map[string]any) map[string]string {
	out := map[string]string{}
	list, _ := answer["keys"].([]any)
	for _, k := range list {
		m, _ := k.(map[string]any)
		out[asString(m["suite"])] = asString(m["status"])
	}
	return out
}

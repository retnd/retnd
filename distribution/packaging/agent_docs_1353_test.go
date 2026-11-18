package packaging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// EPIC U's docs pass (#1353): the site's `agent` row held to the verbs the
// binary registers, the five agents.* fields held to the config inventory,
// and the inventory's summary held to its own rows. These follow the shape of
// the `encryption` row guard in site_reference_test.go, on purpose: the same
// extractor reads a Go switch instead of a map.

var agentRow = regexp.MustCompile(`(?s)<tr><td><code>agent</code></td>(.*?)</tr>`)

// agentVerbs reads the verbs `retnd agent` accepts out of the switch in the
// named function. A top-level verb that has its own switch (token,
// confirmations) is listed once per sub-verb as "token create", so the row has
// to name each act and not just the family.
func agentVerbs(t *testing.T, srcs map[string]string) []string {
	t.Helper()
	cases := func(fn string) []string {
		for _, src := range srcs {
			i := strings.Index(src, "func "+fn+"(")
			if i < 0 {
				continue
			}
			body := src[i:]
			if j := strings.Index(body, "\n}\n"); j >= 0 {
				body = body[:j]
			}
			var out []string
			for _, m := range regexp.MustCompile(`(?m)^\s*case ((?:"[a-z-]+",?\s*)+):`).FindAllStringSubmatch(body, -1) {
				for _, w := range regexp.MustCompile(`"([a-z-]+)"`).FindAllStringSubmatch(m[1], -1) {
					out = append(out, w[1])
				}
			}
			return out
		}
		return nil
	}
	var verbs []string
	for _, v := range cases("cmdAgent") {
		sub := map[string]string{"token": "cmdAgentToken", "confirmations": "cmdAgentConfirmations"}[v]
		if sub == "" {
			verbs = append(verbs, v)
			continue
		}
		for _, s := range cases(sub) {
			verbs = append(verbs, v+" "+s)
		}
	}
	sort.Strings(verbs)
	return verbs
}

func TestTheSiteReferenceAgentRowNamesEveryAgentVerb(t *testing.T) {
	srcs := map[string]string{}
	files, err := filepath.Glob(Path("core/cmd/retnd/agent*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no core/cmd/retnd/agent*.go to read the agent verbs from: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		srcs[f] = string(b)
	}
	verbs := agentVerbs(t, srcs)
	// The floor is the specification's own verb table (EPIC U section 12). A
	// reader that finds fewer has stopped reading the dispatch, and a row held
	// to nothing is the row lying quietly.
	for _, want := range []string{"serve", "tools", "audit", "token create", "token list", "token revoke", "confirmations list", "confirmations approve", "confirmations deny"} {
		found := false
		for _, v := range verbs {
			found = found || v == want
		}
		if !found {
			t.Fatalf("the agent dispatch in core/cmd/retnd/agent*.go does not register %q (read %v). Either the verb is missing or cmdAgent, cmdAgentToken and cmdAgentConfirmations no longer hold their verbs in a switch this test can read", want, verbs)
		}
	}
	row := agentRow.FindStringSubmatch(htmlRegion(t, readReference(t), "CLI-COMMANDS"))
	if row == nil {
		t.Fatal("docs/site/reference.html's command table has no `agent` row to hold to the verbs")
	}
	if missing := undocumentedAgentVerbs(row[1], verbs); len(missing) > 0 {
		t.Errorf("docs/site/reference.html's `agent` row does not name these verbs the binary registers: %v.\n"+
			"Each needs a <code> span in that row that starts with the verb, for a sub-verb as \"token create\" or \"confirmations approve\".", missing)
	}
}

// undocumentedAgentVerbs is the verbs no <code> span in the row starts with,
// read with or without the "agent " the row writes each one with (and an
// optional "retnd "), so "agent token create NAME" names "token create".
// The encryption row's reader allows only its own prefix.
func undocumentedAgentVerbs(row string, verbs []string) []string {
	var missing []string
	for _, v := range verbs {
		named := regexp.MustCompile(`<code>(retnd )?(agent )?` + regexp.QuoteMeta(v) + `(&lt;| |</code>)`)
		if !named.MatchString(row) {
			missing = append(missing, v)
		}
	}
	return missing
}

func TestTheAgentVerbCheckCanActuallyFail(t *testing.T) {
	src := "func cmdAgent(args []string) int {\n\tswitch args[0] {\n\tcase \"token\":\n\t\treturn cmdAgentToken(args)\n\tcase \"serve\":\n\t\treturn 0\n\tcase \"confirmations\":\n\t\treturn 1\n\t}\n}\n\n" +
		"func cmdAgentToken(args []string) int {\n\tswitch args[0] {\n\tcase \"create\", \"list\":\n\t\treturn 0\n\t}\n}\n\n" +
		"func cmdAgentConfirmations(args []string) int {\n\tswitch args[0] {\n\tcase \"approve\":\n\t\treturn 0\n\t}\n}\n"
	got := agentVerbs(t, map[string]string{"agent.go": src})
	want := []string{"confirmations approve", "serve", "token create", "token list"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the reader found %v, want %v", got, want)
	}
	row := `<td><code>agent token create NAME</code>, <code>token list</code>, <code>serve --stdio</code></td>`
	if m := undocumentedAgentVerbs(row, got); len(m) != 1 || m[0] != "confirmations approve" {
		t.Fatalf("dropping the confirmations verbs from a row must report exactly them, got %v", m)
	}
}

type inventoryField struct {
	Field    string `json:"field"`
	YAMLPath string `json:"yaml_path"`
	Scope    string `json:"scope"`
	Default  any    `json:"default"`
	UI       struct {
		Configurable   bool    `json:"configurable"`
		Recommendation *string `json:"recommendation"`
	} `json:"ui"`
}

type inventory struct {
	ScopeValues []string         `json:"scope_values"`
	Summary     map[string]any   `json:"summary"`
	Fields      []inventoryField `json:"fields"`
}

// inventoryProblems is the inventory held to its own rows: the summary counts
// that are derivable from the fields, and the scope of every field. The
// yaml_fields and env_reachable counts are not held here because they were
// already one off from their rows before EPIC U and this guard does not get to
// quietly redefine them.
func inventoryProblems(inv inventory) []string {
	var out []string
	num := func(v any) int {
		f, _ := v.(float64)
		return int(f)
	}
	byScope, already, toAdd, fileOnly := map[string]int{}, 0, 0, 0
	scopes := map[string]bool{}
	for _, s := range inv.ScopeValues {
		scopes[s] = true
	}
	for _, f := range inv.Fields {
		if !scopes[f.Scope] {
			out = append(out, f.Field+" has scope "+f.Scope+", which is not in scope_values")
		}
		byScope[f.Scope]++
		rec := ""
		if f.UI.Recommendation != nil {
			rec = *f.UI.Recommendation
		}
		switch {
		case f.UI.Configurable:
			already++
		case strings.HasPrefix(rec, "add"):
			toAdd++
		case strings.HasPrefix(strings.ToLower(rec), "file-only"):
			fileOnly++
		}
	}
	if got := num(inv.Summary["total"]); got != len(inv.Fields) {
		out = append(out, "summary.total says "+strconv.Itoa(got)+" and there are "+strconv.Itoa(len(inv.Fields))+" fields")
	}
	for name, want := range map[string]int{"ui_already": already, "ui_to_add": toAdd, "file_only": fileOnly} {
		if got := num(inv.Summary[name]); got != want {
			out = append(out, "summary."+name+" says "+strconv.Itoa(got)+" and the fields say "+strconv.Itoa(want))
		}
	}
	sum, _ := inv.Summary["by_scope"].(map[string]any)
	for scope, want := range byScope {
		if got := num(sum[scope]); got != want {
			out = append(out, "summary.by_scope."+scope+" says "+strconv.Itoa(got)+" and the fields say "+strconv.Itoa(want))
		}
	}
	sort.Strings(out)
	return out
}

func readInventory(t *testing.T) inventory {
	t.Helper()
	raw, err := os.ReadFile(Path("docs/config-inventory.json"))
	if err != nil {
		t.Fatalf("read docs/config-inventory.json: %v", err)
	}
	var inv inventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatalf("docs/config-inventory.json is not valid JSON: %v", err)
	}
	return inv
}

func TestTheConfigInventoryAgreesWithItsOwnSummary(t *testing.T) {
	for _, p := range inventoryProblems(readInventory(t)) {
		t.Error(p)
	}
}

func TestTheConfigInventoryCarriesTheFiveAgentsFieldsAtDeploymentScope(t *testing.T) {
	want := map[string]string{
		"agents.http_transport":            "off",
		"agents.rate_limit_per_minute":     "60",
		"agents.confirmation_ttl":          "15m",
		"agents.audit_retention_days":      "90",
		"agents.max_pending_confirmations": "5",
	}
	have := map[string]inventoryField{}
	for _, f := range readInventory(t).Fields {
		if strings.HasPrefix(f.Field, "agents.") {
			have[f.Field] = f
		}
	}
	for name, def := range want {
		f, ok := have[name]
		if !ok {
			t.Errorf("docs/config-inventory.json has no %s field", name)
			continue
		}
		if f.Scope != "deployment" {
			t.Errorf("%s has scope %q, want deployment", name, f.Scope)
		}
		if s, _ := f.Default.(string); !strings.HasPrefix(s, def) {
			t.Errorf("%s defaults to %v, the specification says %s", name, f.Default, def)
		}
	}
	if len(have) != len(want) {
		t.Errorf("docs/config-inventory.json has %d agents.* fields, the specification has %d", len(have), len(want))
	}
}

func TestTheInventoryCheckCanActuallyFail(t *testing.T) {
	rec := "file-only: x"
	good := func() inventory {
		inv := inventory{ScopeValues: []string{"deployment"}, Summary: map[string]any{
			"total": 1.0, "ui_already": 0.0, "ui_to_add": 0.0, "file_only": 1.0, "by_scope": map[string]any{"deployment": 1.0},
		}}
		f := inventoryField{Field: "a", Scope: "deployment"}
		f.UI.Recommendation = &rec
		inv.Fields = []inventoryField{f}
		return inv
	}
	if got := inventoryProblems(good()); len(got) != 0 {
		t.Fatalf("the control inventory should be clean: %v", got)
	}
	// Leaving the summary where it was after adding a field is the mistake this
	// guard exists for: every count that should move by one is named.
	inv := good()
	inv.Fields = append(inv.Fields, inv.Fields[0])
	got := strings.Join(inventoryProblems(inv), "\n")
	for _, want := range []string{"summary.total", "summary.file_only", "summary.by_scope.deployment"} {
		if !strings.Contains(got, want) {
			t.Errorf("a field added without moving the summary was not reported at %s: %s", want, got)
		}
	}
	inv = good()
	inv.Fields[0].Scope = "elsewhere"
	if got := strings.Join(inventoryProblems(inv), "\n"); !strings.Contains(got, "not in scope_values") {
		t.Errorf("a scope outside scope_values was not reported: %s", got)
	}
}

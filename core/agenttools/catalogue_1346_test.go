package agenttools

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/service"
)

// The catalogue's guards (#1346). Each one is held by a planted violation
// the test below it feeds the same pure function, so a guard that has
// stopped guarding fails here and not in review.

// The names table of docs/EPIC-U-mcp-server.md section 12, as the test's own
// copy: a tool added to or dropped from the table without the spec moving
// fails here.
var (
	specRead      = []string{"status", "backup_sets_list", "backup_set_show", "backups_list", "backup_show", "copies_show", "encryption_state", "assurance_due", "assurance_show", "activity", "activity_live", "quarantine_list", "operations_list", "operation_show", "storage_mediums_list", "storage_usage", "retention_preview", "workflow_runs_list", "workflow_run_show"}
	specOperate   = []string{"run_cycle", "run_backup_set", "recovery_test_start", "verify_copy", "scrub_start", "retry_backup", "quarantine_revalidate"}
	specConfigure = []string{"backup_set_enabled", "backup_set_retention_set", "backup_set_retention_clear", "backup_set_assurance_set"}
	specResources = []string{"retnd://status", "retnd://backup-sets", "retnd://backup-sets/{source}/{set}", "retnd://backups/{source}/{set}/{name}", "retnd://activity", "retnd://encryption", "retnd://assurance/due"}
	specPrompts   = []string{"triage-health", "explain-backup", "prepare-recovery-test"}
)

func namesAt(scope Scope) []string {
	var out []string
	for _, r := range Tools() {
		if r.Scope == scope {
			out = append(out, r.Tool)
		}
	}
	return out
}

func TestTheCatalogueHoldsTheSpecsNamesAndNothingElse(t *testing.T) {
	for scope, want := range map[Scope][]string{ScopeRead: specRead, ScopeOperate: specOperate, ScopeConfigure: specConfigure} {
		got := namesAt(scope)
		if !slices.Equal(sorted(got), sorted(want)) {
			t.Errorf("%s tools = %v, the spec names %v", scope, got, want)
		}
	}
	var uris, names []string
	for _, r := range Resources() {
		uris = append(uris, r.URI)
		if tool, ok := Lookup(r.Tool); !ok || tool.Scope != ScopeRead {
			t.Errorf("resource %s is backed by %q, which is not a read tool", r.URI, r.Tool)
		}
	}
	for _, p := range Prompts() {
		names = append(names, p.Name)
		for _, tn := range p.Tools {
			if tool, ok := Lookup(tn); !ok || tool.Scope != ScopeRead {
				t.Errorf("prompt %s draws on %q, which is not a read tool", p.Name, tn)
			}
		}
	}
	if !slices.Equal(sorted(uris), sorted(specResources)) {
		t.Errorf("resources = %v, the spec names %v", uris, specResources)
	}
	if !slices.Equal(sorted(names), sorted(specPrompts)) {
		t.Errorf("prompts = %v, the spec names %v", names, specPrompts)
	}
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

// The three words are the token store's own. A scope the catalogue spells
// differently from the store would be a tool no token could ever call.
func TestTheScopesAreTheTokenStoresScopes(t *testing.T) {
	for _, pair := range [][2]string{{string(ScopeRead), service.AgentScopeRead}, {string(ScopeOperate), service.AgentScopeOperate}, {string(ScopeConfigure), service.AgentScopeConfigure}} {
		if pair[0] != pair[1] {
			t.Errorf("catalogue scope %q is the store's %q", pair[0], pair[1])
		}
	}
}

// malformed reports what is wrong with the shape of a table: a tool with no
// description or the wrong scope for its method, a refusal with no reason, a
// tool named twice, a (method, route, action) answered twice.
func malformed(rows []Row) []string {
	var out []string
	names, keys := map[string]bool{}, map[string]bool{}
	byRoute := map[string]int{}
	for _, r := range rows {
		byRoute[r.Key()]++
	}
	for _, r := range rows {
		id := r.Key() + " " + r.Action
		if keys[id] {
			out = append(out, "duplicate row for "+strings.TrimSpace(id))
		}
		keys[id] = true
		if byRoute[r.Key()] > 1 && r.Action == "" {
			out = append(out, r.Key()+" shares its route with another row and names no action")
		}
		if r.Tool == "" {
			if strings.TrimSpace(r.Why) == "" {
				out = append(out, r.Key()+" is a refusal with no reason")
			}
			continue
		}
		if names[r.Tool] {
			out = append(out, "tool "+r.Tool+" is named twice")
		}
		names[r.Tool] = true
		if strings.TrimSpace(r.Description) == "" || r.Why != "" {
			out = append(out, "tool "+r.Tool+" needs a description and no why")
		}
		if (r.Method == "GET") != (r.Scope == ScopeRead) {
			out = append(out, "tool "+r.Tool+" has scope "+string(r.Scope)+" on "+r.Method)
		}
		if r.Untrusted == nil {
			out = append(out, "tool "+r.Tool+" does not say what is untrusted, not even that nothing is")
		}
	}
	return out
}

func TestEveryRowIsWellFormed(t *testing.T) {
	for _, problem := range malformed(Rows()) {
		t.Error(problem)
	}
}

func TestTheShapeGuardSeesAPlantedMalformedRow(t *testing.T) {
	good := Rows()
	dup := append(slices.Clone(good), good[0])
	if got := malformed(dup); len(got) == 0 {
		t.Error("a duplicated row was not reported")
	}
	noWhy := append(slices.Clone(good), Row{Method: "GET", Route: "/nowhere"})
	if got := malformed(noWhy); len(got) == 0 {
		t.Error("a refusal with no reason was not reported")
	}
	if got := malformed(good); len(got) != 0 {
		t.Errorf("the control table was reported: %v", got)
	}
}

func TestEveryToolHasATerminalTwinThatIsACommand(t *testing.T) {
	for _, r := range Tools() {
		twin := Twin(r)
		if !strings.HasPrefix(twin, "retnd ") {
			t.Errorf("%s has no terminal twin in core/cliecho (got %q)", r.Tool, twin)
		}
	}
}

// ---------------------------------------------------------- denied routes ---

// toolsOverDeniedRoutes is the guard: the tool rows whose route the deny list
// matches. concrete is how a pattern becomes a path.
func toolsOverDeniedRoutes(rows []Row) []string {
	var out []string
	for _, r := range rows {
		if r.Tool != "" && Denied(r.Method, concrete(r.Route)) {
			out = append(out, r.Tool+" over "+r.Key())
		}
	}
	return out
}

// Falsify: register key_export over /encryption/keys/{id}/export.
func TestNoToolExistsOverADeniedRoute(t *testing.T) {
	if got := toolsOverDeniedRoutes(Rows()); len(got) != 0 {
		t.Errorf("tools over denied routes: %v", got)
	}
}

func TestTheDeniedRouteGuardGoesRedForAToolOverAnExport(t *testing.T) {
	planted := append(Rows(), Row{Tool: "key_export", Method: "POST", Route: "/encryption/keys/{id}/export", Scope: ScopeOperate, Description: "x", Untrusted: []string{}})
	if got := toolsOverDeniedRoutes(planted); len(got) != 1 || !strings.Contains(got[0], "key_export") {
		t.Errorf("a tool over the key export was not reported: %v", got)
	}
}

// The routes that can return material or change what is protected, as
// concrete requests. Every one is refused to a token under any scope, and the
// controls beside them are not, so the list is neither empty nor everything.
func TestTheDenyListHoldsTheMaterialBearingRoutes(t *testing.T) {
	denied := []struct{ method, path string }{
		{"POST", "/auth/login"}, {"GET", "/auth/session"}, {"GET", "/auth/anything/at/all"},
		{"GET", "/system/first-run"}, {"POST", "/system/first-run/skip"},
		{"POST", "/recoveries"}, {"GET", "/recoveries/g1"}, {"POST", "/backups/src/set/name/recovery"},
		{"GET", "/agent/tokens"}, {"POST", "/agent/tokens/agt_1/revoke"},
		{"GET", "/encryption/keys"}, {"POST", "/encryption/keys/rotate"}, {"POST", "/encryption/keys/k1/export"},
		{"POST", "/encryption/keys/k1/confirm"}, {"POST", "/encryption/keys/k1/retire"},
		{"POST", "/encryption/providers/test"},
		{"POST", "/storage-credentials"}, {"GET", "/storage-mediums/m1/configuration"}, {"PUT", "/storage-mediums/m1/configuration"},
		{"POST", "/storage-mediums/preflight"}, {"POST", "/storage-mediums/m1/preflight"},
		{"GET", "/ssh-keys"}, {"POST", "/ssh-keys"}, {"GET", "/ssh/key-candidates"}, {"POST", "/ssh/host-key-probe"},
		{"GET", "/settings/workflow/environment"}, {"PUT", "/settings/workflow/environment/TOKEN"},
		{"GET", "/backup-sets/src/set/workflow/environment"}, {"DELETE", "/backup-sets/src/set/workflow/environment/X"},
		{"GET", "/workflow-runs/r1/steps/s1/logs"}, {"GET", "/assurance/validators/v1/script"},
		{"POST", "/backup-sets"}, {"DELETE", "/backup-sets/src/set"}, {"POST", "/storage-mediums"}, {"DELETE", "/storage-mediums/m1"},
		{"POST", "/catalog/rebuild"}, {"POST", "/backup-sets/src/set/retention/apply"}, {"POST", "/quarantine/src/set/n/reinstate"},
		// The router cleans a path before it matches, and so does the list.
		{"GET", "/encryption//keys"}, {"GET", "/system/../encryption/keys"}, {"GET", "/encryption/keys/"},
	}
	for _, c := range denied {
		if !Denied(c.method, c.path) {
			t.Errorf("%s %s is not denied", c.method, c.path)
		}
	}
	allowed := []struct{ method, path string }{
		{"GET", "/system/version"}, {"GET", "/system/health"}, {"GET", "/backup-sets"}, {"GET", "/backup-sets/src/set"},
		{"GET", "/backups"}, {"GET", "/backups/src/set/name"}, {"GET", "/encryption/policy"}, {"GET", "/encryption/copies"},
		{"GET", "/workflow-runs"}, {"GET", "/workflow-runs/r1"}, {"GET", "/storage-mediums"}, {"GET", "/storage-mediums/m1/usage"},
		{"POST", "/backup-sets/src/set/enabled"}, {"POST", "/operations"}, {"GET", "/agent/tools"},
		{"GET", "/ssh-keys-are-not-this"}, {"GET", "/authorities"},
	}
	for _, c := range allowed {
		if Denied(c.method, c.path) {
			t.Errorf("%s %s is denied and has a tool or no reason to be", c.method, c.path)
		}
	}
}

// Every entry of the deny list is a refusal row, which is where its reason is
// stated: a denial with no row is a refusal nobody explained.
func TestEveryDeniedRouteIsAStatedRefusal(t *testing.T) {
	rows := Rows()
	for _, d := range denied {
		hit := false
		for _, r := range rows {
			if r.Tool == "" && (d.method == "" || d.method == r.Method) && matches(d.pattern, concrete(r.Route)) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("the deny list holds %s %s and no refusal row says why", d.method, d.pattern)
		}
	}
}

// A change reaches a route only through a tool row, and a route several acts
// share only through the acts that have one.
func TestAMutationIsOfferedOnlyOverAToolRoute(t *testing.T) {
	for _, c := range []struct {
		method, path, action string
		want                 bool
	}{
		{"POST", "/backup-sets/src/set/enabled", "", true},
		{"PUT", "/backup-sets/src/set/retention", "", true},
		{"DELETE", "/backup-sets/src/set/retention", "", true},
		{"PATCH", "/backup-sets/src/set/assurance", "", true},
		{"POST", "/operations", "run_cycle", true},
		{"POST", "/operations", "run_backup_set", true},
		{"POST", "/operations", "restore_placement", false},
		{"POST", "/operations", "restore_snapshot", false},
		{"POST", "/operations", "hold_snapshot", false},
		{"POST", "/operations", "", false},
		{"POST", "/backups/src/set/name/retry", "", true},
		{"PATCH", "/backup-sets/src/set", "", false},
		{"POST", "/encryption/reencrypt", "", false},
		{"DELETE", "/backup-sets/src/set/encryption", "", false},
		{"POST", "/backup-sets/src/set/enabled/extra", "", false},
	} {
		if got := Offered(c.method, c.path, c.action); got != c.want {
			t.Errorf("Offered(%s %s %q) = %v, want %v", c.method, c.path, c.action, got, c.want)
		}
	}
}

// ----------------------------------------------------------- descriptions ---

// nonLiteralDescriptions names the rows whose Description is anything but one
// string literal: a concatenation, a call, a variable. Those are the shapes
// that let a backup set's name into a sentence an agent reads as a tool's own.
func nonLiteralDescriptions(src string) []string {
	f, err := parser.ParseFile(token.NewFileSet(), "tools.go", src, 0)
	if err != nil {
		return []string{"does not parse: " + err.Error()}
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Description" {
			if lit, ok := kv.Value.(*ast.BasicLit); !ok || lit.Kind != token.STRING {
				out = append(out, "a Description that is not a string literal")
			}
		}
		return true
	})
	return out
}

// Falsify: interpolate a backup set name into a description.
func TestToolDescriptionsAreStaticAndPinned(t *testing.T) {
	src, err := os.ReadFile("tools.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range nonLiteralDescriptions(string(src)) {
		t.Error(p)
	}

	golden, err := os.ReadFile(filepath.Join("testdata", "descriptions.golden"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range Tools() {
		got = append(got, r.Tool+"\t"+string(r.Scope)+"\t"+r.Description)
		if strings.ContainsAny(r.Description, "%{}<>`$") {
			t.Errorf("%s: a description carries a placeholder or markup: %q", r.Tool, r.Description)
		}
	}
	if want := strings.TrimSuffix(string(golden), "\n"); strings.Join(got, "\n") != want {
		t.Errorf("the descriptions moved from testdata/descriptions.golden. A new sentence is a review of what an agent will read, so edit the golden by hand in the same commit.\ngot:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func TestTheStaticGuardSeesAnInterpolatedDescription(t *testing.T) {
	planted := `package x
var rows = []Row{{Tool: "a", Description: "Read " + setName}, {Tool: "b", Description: fmt.Sprintf("Read %s", n)}, {Tool: "c", Description: "Read it."}}`
	if got := nonLiteralDescriptions(planted); len(got) != 2 {
		t.Errorf("got %v, want the concatenation and the call and not the literal", got)
	}
}

// -------------------------------------------------------------- untrusted ---

// The outside origin of each tool's result, as the table of what a deployment
// does not write: a remote file's name, a validator's output, a script's
// output, an activity message, a remote hostname. Each field named here has to
// be in the tool's Untrusted list.
var outsideOrigins = map[string][]string{
	"status":               {"backup_sets[].reason"},
	"backup_sets_list":     {"backup_sets[].host", "backup_sets[].remote_path"},
	"backup_set_show":      {"host", "remote_path"},
	"backups_list":         {"artifacts[].name", "artifacts[].validation_detail", "artifacts[].remote_path"},
	"backup_show":          {"name", "validation_detail", "remote_path"},
	"assurance_due":        {"items[].artifact_name"},
	"activity":             {"events[].artifact_name", "events[].detail"},
	"activity_live":        {"sets[].events[].message", "deployment.events[].message"},
	"quarantine_list":      {"artifacts[].name", "artifacts[].quarantine_reason"},
	"operations_list":      {"operations[].error", "operations[].result"},
	"operation_show":       {"error", "result"},
	"storage_mediums_list": {"mediums[].endpoint"},
	"retention_preview":    {"verdicts[].artifact", "verdicts[].reason"},
	"workflow_runs_list":   {"runs[].failed_script", "runs[].steps[].script_name"},
	"workflow_run_show":    {"failed_script", "steps[].script_name"},
}

func missingUntrusted(rows []Row) []string {
	var out []string
	for _, r := range rows {
		for _, f := range outsideOrigins[r.Tool] {
			if !slices.Contains(r.Untrusted, f) {
				out = append(out, r.Tool+" does not name "+f)
			}
		}
	}
	return out
}

// Falsify: blank Untrusted on backups_list.
func TestEveryOutsideOriginFieldIsNamedUntrusted(t *testing.T) {
	for _, p := range missingUntrusted(Rows()) {
		t.Error(p)
	}
	for name := range outsideOrigins {
		if _, ok := Lookup(name); !ok {
			t.Errorf("the origins table names %q, which is not a tool", name)
		}
	}
}

func TestTheUntrustedGuardSeesABlankedList(t *testing.T) {
	planted := Rows()
	for i := range planted {
		if planted[i].Tool == "backups_list" {
			planted[i].Untrusted = nil
		}
	}
	if got := missingUntrusted(planted); len(got) == 0 {
		t.Error("blanking Untrusted on backups_list was not reported")
	}
}

// An untrusted path that names a field the response does not have protects
// nothing, so each one is resolved against the contract's own response schema.
func TestEveryUntrustedPathIsAFieldOfTheResponse(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "v1", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, r := range Tools() {
		if len(r.Untrusted) == 0 {
			continue
		}
		route := r.Route
		if route == "/backup-sets/*" {
			route = "/backup-sets/{source}/{set}"
		}
		var op struct {
			Responses map[string]struct {
				Content map[string]struct {
					Schema schema `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		}
		if err := json.Unmarshal(doc.Paths[route][strings.ToLower(r.Method)], &op); err != nil || len(op.Responses["200"].Content) == 0 {
			t.Fatalf("%s: no 200 response in the contract for %s", r.Tool, route)
		}
		root := op.Responses["200"].Content["application/json"].Schema
		for _, path := range r.Untrusted {
			if !resolves(root, strings.Split(path, "."), doc.Components.Schemas) {
				t.Errorf("%s: %q is not a field of the %s response", r.Tool, path, route)
			}
		}
	}
}

type schema struct {
	Ref        string            `json:"$ref"`
	Items      *schema           `json:"items"`
	Properties map[string]schema `json:"properties"`
}

func resolves(s schema, path []string, all map[string]schema) bool {
	if s.Ref != "" {
		s = all[strings.TrimPrefix(s.Ref, "#/components/schemas/")]
	}
	if len(path) == 0 {
		return true
	}
	seg := path[0]
	array := strings.HasSuffix(seg, "[]")
	seg = strings.TrimSuffix(seg, "[]")
	next, ok := s.Properties[seg]
	if !ok {
		return false
	}
	if array {
		if next.Items == nil {
			return false
		}
		next = *next.Items
	}
	return resolves(next, path[1:], all)
}

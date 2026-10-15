package webhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Issues #1163 and #1164 as the contract and the prose say them. The Go side
// is the arbiter: the spec lists what the router exempts, and the docs say
// which profiles can do what.

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"..", "..", ".."}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type openAPI1163 struct {
	Paths map[string]map[string]struct {
		Description string   `json:"description"`
		Exempt      []string `json:"x-destructive-gate-exempt-actions"`
		Gate        bool     `json:"x-destructive-gate"`
		Responses   map[string]struct {
			Codes []string `json:"x-error-codes"`
		} `json:"responses"`
	} `json:"paths"`
	Components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`
}

func loadOpenAPI1163(t *testing.T) openAPI1163 {
	t.Helper()
	var doc openAPI1163
	if err := json.Unmarshal([]byte(readRepoFile(t, "api", "v1", "openapi.json")), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestContract1164_TheSpecListsTheExemptActionsTheRouterExempts(t *testing.T) {
	op := loadOpenAPI1163(t).Paths["/operations"]["post"]
	got := append([]string(nil), op.Exempt...)
	sort.Strings(got)
	var want []string
	for a := range destructiveGateExemptActions {
		want = append(want, a)
	}
	sort.Strings(want)
	if len(want) == 0 {
		t.Fatal("the router exempts nothing, so this compared two empty lists")
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("POST /operations x-destructive-gate-exempt-actions = %v, want what the router exempts: %v", got, want)
	}
	if !op.Gate {
		t.Error("POST /operations lost x-destructive-gate: restores and runs are still gated on it")
	}
	for _, a := range want {
		if !strings.Contains(op.Description, a) {
			t.Errorf("the description of POST /operations does not name the exempt action %s", a)
		}
	}
	if strings.Contains(op.Description, "refuses every action") {
		t.Error("the description of POST /operations still says it refuses every action")
	}
}

func TestContract1163_TheSpecSaysWhichProfilesTheGateOpensFor(t *testing.T) {
	doc := loadOpenAPI1163(t)
	for _, tc := range []struct{ path, method string }{{"/operations", "post"}, {"/backup-sets/{source}/{set}/retention/apply", "post"}} {
		d := doc.Paths[tc.path][tc.method].Description
		for _, want := range []string{"local-account", "gateway", "#92"} {
			if !strings.Contains(d, want) {
				t.Errorf("%s %s does not say %q\n  got: %s", tc.method, tc.path, want, d)
			}
		}
		if strings.Contains(d, "In every build of this release") {
			t.Errorf("%s %s still says every build of this release refuses it", tc.method, tc.path)
		}
	}
	op := doc.Paths["/operations"]["post"].Description
	for _, want := range []string{"X-Retnd-Step-Up-Password", "X-Retnd-Insecure-Transport-Ack", "acknowledged"} {
		if !strings.Contains(op, want) {
			t.Errorf("POST /operations does not describe %q", want)
		}
	}
}

func TestContract1163_TheNewRefusalsAreDeclared(t *testing.T) {
	doc := loadOpenAPI1163(t)
	has := func(path, status, code string) bool {
		for _, c := range doc.Paths[path]["post"].Responses[status].Codes {
			if c == code {
				return true
			}
		}
		return false
	}
	for _, code := range []string{"STEP_UP_REQUIRED", "STEP_UP_FAILED", "STEP_UP_UNAVAILABLE", "INSECURE_TRANSPORT_ACK_REQUIRED"} {
		if !has("/operations", "403", code) {
			t.Errorf("POST /operations 403 does not declare %s", code)
		}
	}
	if !has("/operations", "400", "ACKNOWLEDGEMENT_REQUIRED") {
		t.Error("POST /operations 400 does not declare ACKNOWLEDGEMENT_REQUIRED")
	}
	if !has("/backup-sets/{source}/{set}/retention/apply", "403", "INSECURE_TRANSPORT_ACK_REQUIRED") {
		t.Error("retention apply 403 does not declare INSECURE_TRANSPORT_ACK_REQUIRED")
	}
}

func TestContract1163_TheRequestAndCapabilityFieldsExist(t *testing.T) {
	doc := loadOpenAPI1163(t)
	for _, tc := range []struct{ schema, field string }{
		{"SnapshotRestoreRequest", `"acknowledged"`},
		{"SnapshotHoldReleaseRequest", `"acknowledged"`},
		{"DestructiveOperationsStatus", `"insecure_transport_ack_required"`},
		{"DestructiveOperationsStatus", `"step_up_required"`},
	} {
		if !strings.Contains(string(doc.Components.Schemas[tc.schema]), tc.field) {
			t.Errorf("schema %s has no %s", tc.schema, tc.field)
		}
	}
}

// The prose that used to say these were terminal-only everywhere says which
// profile can do what, and the two epic documents that pinned the old
// behaviour are corrected.
func TestDocs1163_SayWhichProfilesCanRunWhich(t *testing.T) {
	for _, f := range [][]string{{"docs", "site", "reference.html"}, {"docs", "site", "web-ui.html"}, {"docs", "recovery-without-a-terminal.md"}} {
		text := read1163(t, f...)
		for _, want := range []string{"local-account", "gateway", "#1163", "#1164"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not mention %q", filepath.Join(f...), want)
			}
		}
	}
	changelog := readRepoFile(t, "CHANGELOG.md")
	for _, want := range []string{"#1163", "#1164"} {
		if !strings.Contains(changelog, want) {
			t.Errorf("CHANGELOG.md has no entry for %s", want)
		}
	}
	adr := readRepoFile(t, "docs", "adr", "0029-the-recovery-boundary.md")
	i := strings.Index(adr, "Addendum")
	if i < 0 {
		t.Fatal("ADR 0029 has no addendum for #1163 and #1164")
	}
	for _, want := range []string{"#92", "#1163", "#1164", "step-up", "loopback", "Secure"} {
		if !strings.Contains(adr[i:], want) {
			t.Errorf("the ADR 0029 addendum does not mention %q", want)
		}
	}
	if strings.Contains(readRepoFile(t, "docs", "EPIC-L-scripted-backup-workflows.md"), "is refused `403 DESTRUCTIVE_OPERATIONS_DISABLED` everywhere") {
		t.Error("EPIC-L still says a browser cannot start a run anywhere (#1163 opened the gate for local-account profiles)")
	}
	q := readRepoFile(t, "docs", "EPIC-Q-recovery-and-assurance.md")
	if !strings.Contains(q, "on a gateway profile") {
		t.Error("EPIC-Q's phase 1 exit gate still describes every deployment as having a shut gate")
	}
}

func read1163(t *testing.T, parts ...string) string { return readRepoFile(t, parts...) }

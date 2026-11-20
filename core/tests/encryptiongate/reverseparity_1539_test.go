package encryptiongate_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// N1.16's reverse direction (#1539): a terminal command with no way to do the
// same thing from the browser. The forward direction (every route has an echo,
// no gap names a shipped verb) has had guards since #993; this one was a sweep
// by hand, recorded in the matrix on 2026-11-09 and never repeated.
//
// What can be held by a test is the verb level, and it is held in two hops:
//
//   - CLI verb to API route: core/cliecho/routes.go builds the verb's command
//     for some route (`newCmd("encryption", "<verb>"`), so a route exists
//     whose echo is that verb.
//   - CLI verb to browser control: ui/shared/src/encryption/encryptionCommands.ts
//     produces `retnd encryption <verb>`. That file's own vitest
//     (encryptionCommands.test.ts, "no browser action without a command, and no
//     command without an action") binds each of its commands to a control, so
//     a command in that file is a control on a screen.
//
// A verb missing a hop is either declared below with its reason or it fails
// here. A declared exception has to be spent: a verb that gained its twin
// takes its entry with it, so the list cannot go stale and read as a gap that
// was closed.
//
// What this cannot see, and the matrix keeps as a hand check: a FLAG or a
// FINDING the terminal has and the screen does not (`provider test
// --managed-key`, `key confirm --digest`, what `catalog rebuild` says about
// an encrypted copy). Those are below the verb level.

var (
	encryptionVerbTable = regexp.MustCompile(`(?s)var encryptionVerbs = map\[string\]func\(args \[\]string\) int\{(.*?)\n\}`)
	verbKey             = regexp.MustCompile(`"([a-z-]+)":`)
	keySubverbRefusal   = regexp.MustCompile(`"encryption key: %q is not ([a-z, ]+?) or ([a-z]+)"`)
)

// encryptionCommands is every `encryption <verb>` the binary dispatches, and
// `encryption key <sub>` for each key sub-verb, read off encryption.go: the
// verb table, and the refusal that lists the key sub-verbs the switch accepts.
func encryptionCommands(t *testing.T, src string) []string {
	t.Helper()
	table := encryptionVerbTable.FindStringSubmatch(src)
	if table == nil {
		t.Fatal("could not find `var encryptionVerbs = map[string]func(args []string) int{...}` in core/cmd/retnd/encryption.go")
	}
	var out []string
	for _, m := range verbKey.FindAllStringSubmatch(table[1], -1) {
		if m[1] != "key" {
			out = append(out, m[1])
		}
	}
	subs := keySubverbRefusal.FindStringSubmatch(src)
	if subs == nil {
		t.Fatal("could not find the `encryption key: <sub-verb> is not ... or ...` refusal in core/cmd/retnd/encryption.go, which is where this test reads the key sub-verbs")
	}
	for _, s := range append(strings.Split(subs[1], ", "), subs[2]) {
		out = append(out, "key "+s)
	}
	sort.Strings(out)
	return out
}

// reverseGaps returns one line per command missing a twin it is not declared
// to lack, and per declared exception that is not spent.
func reverseGaps(commands []string, routes, ui string, noRoute, noControl map[string]string) []string {
	var out []string
	known := map[string]bool{}
	for _, c := range commands {
		known[c] = true
		quoted := `"` + strings.ReplaceAll(c, " ", `", "`) + `"`
		hasRoute := strings.Contains(routes, `newCmd("encryption", `+quoted)
		hasControl := regexp.MustCompile(`retnd encryption ` + regexp.QuoteMeta(c) + `\b`).MatchString(ui)
		_, declaredNoRoute := noRoute[c]
		_, declaredNoControl := noControl[c]
		switch {
		case !hasRoute && !declaredNoRoute:
			out = append(out, "`encryption "+c+"` has no API route (nothing in core/cliecho/routes.go echoes it) and is not declared terminal-only")
		case hasRoute && declaredNoRoute:
			out = append(out, "`encryption "+c+"` is declared to have no API route, and core/cliecho/routes.go echoes it; delete the declaration")
		}
		switch {
		case !hasControl && !declaredNoControl:
			out = append(out, "`encryption "+c+"` has no browser control (encryptionCommands.ts never produces it) and is not declared to lack one")
		case hasControl && declaredNoControl:
			out = append(out, "`encryption "+c+"` is declared to have no browser control, and encryptionCommands.ts produces it; delete the declaration")
		}
	}
	for _, m := range []map[string]string{noRoute, noControl} {
		for c := range m {
			if !known[c] {
				out = append(out, "`encryption "+c+"` is declared as a gap, and the binary has no such command")
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestEveryEncryptionVerbHasABrowserTwinOrSaysWhyNot_1539(t *testing.T) {
	root := repoRoot(t)
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		return string(b)
	}
	commands := encryptionCommands(t, read("core/cmd/retnd/encryption.go"))
	if len(commands) < 10 {
		t.Fatalf("read only %v out of encryption.go; an empty scan is not a clean one", commands)
	}
	keyMaterialIn := "it takes key material as input, and a request body is not a place for it"
	noRoute := map[string]string{
		"decrypt":    keyMaterialIn + "; it also has to work where no deployment exists (#1289)",
		"key import": keyMaterialIn + " (#1289)",
	}
	noControl := map[string]string{
		"decrypt":          noRoute["decrypt"],
		"key import":       noRoute["key import"],
		"encrypt-existing": "API and CLI only in 0.8.0 (#1291); encryptionCommands.test.ts lists startEncryptExisting as API_AND_CLI_ONLY",
	}
	for _, g := range reverseGaps(commands, read("core/cliecho/routes.go"), read("ui/shared/src/encryption/encryptionCommands.ts"), noRoute, noControl) {
		t.Error(g)
	}
}

// The positive control: a command with neither twin is reported twice, a
// declared exception that is not spent is reported, and a declaration naming
// a command that does not exist is reported.
func TestTheReverseParityCheckCanFail_1539(t *testing.T) {
	routes := `newCmd("encryption", "policy")` + "\n" + `newCmd("encryption", "key", "list")`
	ui := `"retnd encryption policy"; "retnd encryption key list"; "retnd encryption decrypt"`
	got := reverseGaps(
		[]string{"decrypt", "key list", "orphan", "policy"},
		routes, ui,
		map[string]string{"decrypt": "x", "ghost": "x"},
		map[string]string{"decrypt": "x"},
	)
	want := []string{
		"`encryption decrypt` is declared to have no browser control, and encryptionCommands.ts produces it; delete the declaration",
		"`encryption ghost` is declared as a gap, and the binary has no such command",
		"`encryption orphan` has no API route (nothing in core/cliecho/routes.go echoes it) and is not declared terminal-only",
		"`encryption orphan` has no browser control (encryptionCommands.ts never produces it) and is not declared to lack one",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	cmds := encryptionCommands(t, "var encryptionVerbs = map[string]func(args []string) int{\n\t\"policy\": a,\n\t\"key\": b,\n\n\t\"encrypt-existing\": c,\n}\n"+
		`return usageError("encryption key: %q is not list, create or import", sub)`)
	if strings.Join(cmds, ",") != "encrypt-existing,key create,key import,key list,policy" {
		t.Fatalf("the table reader read %v", cmds)
	}
}

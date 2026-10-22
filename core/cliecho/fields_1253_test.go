package cliecho

import (
	"sort"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// Issue #1253: a field added to a request schema could be left off the
// printed command without anything noticing.
//
// TestNoRequestFieldReachesTheArgvUnlessItIsEchoedOnPurpose (credentials_
// test.go) runs one way: a field that reaches the line has to be allowed
// to. Nothing ran the other way. A builder that never looked at a new
// field printed a line that looked complete and configured less than the
// click did, and an operator only finds that out by comparing the two.
// When #1253 was written every field left off a line was left off on
// purpose, so this is preventive, and the table below is where each of
// those decisions is now written down.
//
// It's driven by the same corpus as the credentials test (canaryBodies),
// so a schema added to the contract is visited without anybody adding it
// here. Each string field carries a canary naming its own path, and for
// each route with a builder, each field of the schema that route takes
// has to turn up somewhere in the argv of at least one body, or be in
// fieldsNotEchoed with a reason.
//
// "Somewhere in the argv" is deliberately loose. A field can arrive as a
// flag's value, an operand, or part of one (--field id=value,
// source:folder), and the question here is only whether the builder read
// it. Whether it was ALLOWED to put it there is the credentials test's
// job.
//
// Only string fields are checked. A number or a boolean has no canary to
// look for: a 7 or a true in the argv could have come from anywhere.

// fieldCanary is the marker each string field carries, with the field's
// path folded into it so a hit says which field it was.
const fieldCanary = "fieldCANARY"

func fieldCanaryFor(path string) string {
	var b strings.Builder
	b.WriteString(fieldCanary)
	for _, r := range path {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// stringFieldPaths lists every path canaryBodies gave a string to, for one
// schema, by building that schema's body again with a recording value
// function. Reading the paths off the generator rather than off the
// schema's struct is what keeps the two from disagreeing.
func stringFieldPaths(t *testing.T, schema string) []string {
	t.Helper()
	seen := map[string]bool{}
	canaryBodies(t, func(path string) string {
		if strings.HasPrefix(path, schema+".") {
			seen[strings.TrimPrefix(path, schema+".")] = true
		}
		return "x"
	})
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// requestSchemaByRoute is the contract's own answer to "which schema does
// this route take", keyed the way the routes table is.
func requestSchemaByRoute() map[string]string {
	out := map[string]string{}
	for _, e := range apicontract.Endpoints {
		if e.RequestSchema != "" {
			out[key(e.Method, e.Path)] = e.RequestSchema
		}
	}
	return out
}

// fieldEchoes is, for each route that built a command from its own
// schema's canary body, each string field of that schema and whether it
// reached the argv.
type fieldEchoes map[string]map[string]bool

// observeFieldEchoes drives every builder with its own schema's canary
// bodies and records which fields came out the other end.
func observeFieldEchoes(t *testing.T) fieldEchoes {
	t.Helper()
	bodies := canaryBodies(t, fieldCanaryFor)
	bySchema := map[string][]canaryBody{}
	for _, b := range bodies {
		bySchema[b.schema] = append(bySchema[b.schema], b)
	}
	params := map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar", "id": "offsite_s3", "op": "op-1", "grant": "g-1", "run": "r-1", "domain": "d-1"}

	out := fieldEchoes{}
	for route, schema := range requestSchemaByRoute() {
		e, ok := routes[route]
		if !ok || e.build == nil {
			continue
		}
		method, path, _ := strings.Cut(route, " ")
		paths := stringFieldPaths(t, schema)
		if len(paths) == 0 {
			continue
		}
		for _, body := range bySchema[schema] {
			line := Echo(Action{Method: method, Route: path, Params: params, Body: body.body})
			if len(line.Command) == 0 {
				// A body this builder refuses (a run_cycle on POST
				// /operations). The refusal is in the gap golden; there
				// is no line here to have dropped anything from.
				continue
			}
			if out[route] == nil {
				out[route] = map[string]bool{}
				for _, p := range paths {
					out[route][p] = false
				}
			}
			argv := strings.Join(line.Command, "\x00")
			for _, p := range paths {
				if strings.Contains(argv, fieldCanaryFor(schema+"."+p)) {
					out[route][p] = true
				}
			}
		}
	}
	return out
}

// droppedFields lists the fields that reached no argv and that the table
// does not explain, as "ROUTE field".
func droppedFields(seen fieldEchoes, table map[string]map[string]string) []string {
	var out []string
	for route, fields := range seen {
		for field, echoed := range fields {
			if echoed || strings.TrimSpace(table[route][field]) != "" {
				continue
			}
			out = append(out, route+" "+field)
		}
	}
	sort.Strings(out)
	return out
}

// staleFieldReasons lists the table's entries that no longer describe
// anything: a field that is echoed now, or a route or field the corpus
// doesn't reach.
func staleFieldReasons(seen fieldEchoes, table map[string]map[string]string) []string {
	var out []string
	for route, fields := range table {
		for field := range fields {
			echoed, known := seen[route][field]
			switch {
			case !known:
				out = append(out, route+" "+field+" (no builder for this route reads a field by that path)")
			case echoed:
				out = append(out, route+" "+field+" (it is on the line now)")
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestFields1253_EveryRequestFieldReachesTheLineOrIsExplained(t *testing.T) {
	seen := observeFieldEchoes(t)
	if len(seen) == 0 {
		t.Fatal("no builder printed a command from its own schema's canary body, so this test checked nothing")
	}

	// Per route. A builder its own schema's body never got a line out of
	// is a builder whose fields nothing here looked at, and the total
	// above would stay large while it happened.
	for route, schema := range requestSchemaByRoute() {
		if e, ok := routes[route]; !ok || e.build == nil || seen[route] != nil {
			continue
		}
		if len(stringFieldPaths(t, schema)) == 0 {
			continue
		}
		t.Errorf("%s takes %s and has a builder, and no canary body of that schema made it print a command, so nothing here checks which of its fields reach the line. Give the corpus a body it accepts (canary_test.go's bodySelectors).", route, schema)
	}

	for _, dropped := range droppedFields(seen, fieldsNotEchoed) {
		// A route has a space in it and a field path doesn't.
		at := strings.LastIndex(dropped, " ")
		route, field := dropped[:at], dropped[at+1:]
		t.Errorf("%s is a field of the request %s takes, and the command it echoes never carries it. An operator copying that line gets less than the click did. Print it, or add it to fieldsNotEchoed under %q with the reason it is left off.",
			field, route, route)
	}
	for _, stale := range staleFieldReasons(seen, fieldsNotEchoed) {
		t.Errorf("fieldsNotEchoed explains %s. Delete the line.", stale)
	}
}

// The mutation this exists for: a builder stops printing a field it used
// to print. Here that is done to the observation rather than to a
// builder, by flipping one field that does reach the line to one that
// doesn't, and it has to be reported.
func TestFields1253_MutationADroppedFieldIsCaught(t *testing.T) {
	seen := observeFieldEchoes(t)

	route, field := "", ""
	for _, r := range sortedKeys(seen) {
		for _, f := range sortedKeys(seen[r]) {
			if seen[r][f] {
				route, field = r, f
				break
			}
		}
		if route != "" {
			break
		}
	}
	if route == "" {
		t.Fatal("no route echoes any field at all, so there is nothing to drop")
	}
	seen[route][field] = false
	if got := droppedFields(seen, fieldsNotEchoed); !containsString(got, route+" "+field) {
		t.Errorf("%s stopped printing %s and nothing was reported; got %v", route, field, got)
	}

	// And a reason saying nothing is not a reason.
	blank := map[string]map[string]string{route: {field: "   "}}
	if got := droppedFields(seen, blank); !containsString(got, route+" "+field) {
		t.Errorf("a blank reason excused a dropped field; got %v", got)
	}

	// The control: a field that is on the line is not reported.
	seen[route][field] = true
	if got := droppedFields(seen, fieldsNotEchoed); containsString(got, route+" "+field) {
		t.Errorf("a field that reaches the line was reported as dropped")
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// fieldsNotEchoed is every request field a builder leaves off its line on
// purpose, by route and by the field's path inside that route's schema,
// with the reason. An entry is a decision a reviewer can read and argue
// with; a field missing from both the line and this table fails.
//
// Most of them fall into four kinds, and the reasons say which:
//
//   - material: a password, a key, a token or a value a credential can be
//     typed into. The verb reads it on stdin or the line names it with a
//     placeholder (credentials_test.go is the other half of that rule).
//   - a selector: a field whose value picks the verb or the flag's form
//     (action, target.kind), so the line says it by being that verb.
//   - a field that only matters to an arm that refuses (run_backup_set),
//     or to a mode the request isn't in.
//   - a browser handshake with no terminal counterpart: a revision token,
//     a plan id, a hash of a script the browser already holds.
var fieldsNotEchoed = map[string]map[string]string{
	"PATCH /auth/recovery": {
		"currentPassword": "material: the verb reads it on stdin (--current-password-stdin)",
		"smtp.password":   "material: the verb reads it on stdin",
		"recoveryEmail":   "printed as a placeholder: a recovery address is personal and the line is exportable (the routes.go comment on this builder)",
		"smtp.host":       "printed as a placeholder, for the same reason as the recovery address",
		"smtp.username":   "printed as a placeholder, for the same reason as the recovery address",
		"smtp.from":       "printed as a placeholder, for the same reason as the recovery address",
		"smtp.security":   "printed as a placeholder naming the three choices, with the rest of the SMTP block",
	},
	"POST /auth/password": {
		"currentPassword": "material: `auth password --password-stdin` reads both passwords on stdin",
		"newPassword":     "material: `auth password --password-stdin` reads both passwords on stdin",
	},
	"POST /auth/verify-email": {
		"token": "material: the emailed token is read on stdin (--token-stdin)",
	},
	"POST /ssh-keys": {
		"private_key_pem": "material: `ssh-key import --file` names the key file with a placeholder, never its contents",
		"passphrase":      "material: the verb reads it on stdin (--passphrase-stdin)",
	},
	"POST /storage-credentials": {
		"access_key_id":     "material: `medium import-credentials --stdin` reads the pair on stdin",
		"secret_access_key": "material: read on stdin",
		"session_token":     "material: read on stdin",
	},
	"POST /repositories": {
		"passphrase.command[0]": "the caller's own words, which can print a secret: named with a placeholder, never printed",
	},
	"POST /storage-mediums": {
		"credentials.command[0]": "the caller's own words, which can print a secret: named with a placeholder, never printed (#1252)",
	},
	"POST /storage-mediums/preflight": {
		"credentials.command[0]": "the caller's own words, which can print a secret: named with a placeholder, never printed (#1252)",
	},
	"PUT /storage-mediums/{id}": {
		"credentials.command[0]": "the caller's own words, which can print a secret: named with a placeholder, never printed (#1252)",
		"id":                     "the path's id is the one printed; a body naming a different destination is refused, so the two agree by the time anything is written",
	},
	"POST /storage-mediums/{id}/configuration/preflight": {
		"credentials.command[0]": "the caller's own words, which can print a secret: named with a placeholder, never printed (#1252)",
	},
	"PUT /storage-mediums/{id}/configuration": {
		"credentials.command[0]": "the caller's own words, which can print a secret: named with a placeholder, never printed (#1252)",
	},
	"PUT /settings/workflow/environment/{name}": {
		"value":             "nothing tells `--value production` from `--value hunter2`, so it is a placeholder (workflowEnvSetFlags)",
		"secret.command[0]": "the caller's own words, which can print a secret: named with a placeholder, never printed",
	},
	"PUT /backup-sets/{source}/{set}/workflow/environment/{name}": {
		"value":             "nothing tells `--value production` from `--value hunter2`, so it is a placeholder (workflowEnvSetFlags)",
		"secret.command[0]": "the caller's own words, which can print a secret: named with a placeholder, never printed",
	},
	"POST /assurance/validators": {
		"script": "the file the operator uploaded: --script is a placeholder naming its size and sha256, never its contents",
		"env":    "a closed set: minimal or workflow is printed as --env and anything else is refused by the engine, which is all a canary can be (validators_env_1221_test.go checks the echo)",
	},
	"PUT /assurance/validators/{name}": {
		"script":        "the file the operator uploaded: --script is a placeholder naming its size and sha256, never its contents",
		"script_sha256": "the browser's way of saying keep the stored script; on a terminal that is leaving --script off",
		"env":           "a closed set: minimal or workflow is printed as --env and anything else is refused by the engine, which is all a canary can be (validators_env_1221_test.go checks the echo)",
	},
	"PATCH /settings": {
		"retention.tiers[0].name":        "a tier chain is printed as --policy-file, a placeholder for the file holding the whole retention block, because the CLI refuses that flag beside the scalars",
		"retention.tiers[0].granularity": "part of the tier chain, which --policy-file carries",
		"retention.tiers[0].window_unit": "part of the tier chain, which --policy-file carries",
		"retention.tiers[0].medium":      "part of the tier chain, which --policy-file carries",
	},
	"PUT /backup-sets/{source}/{set}/retention": {
		"tiers[0].name":        "a tier chain is printed as --policy-file, a placeholder for the file holding the whole retention block, because the CLI refuses that flag beside the scalars",
		"tiers[0].granularity": "part of the tier chain, which --policy-file carries",
		"tiers[0].window_unit": "part of the tier chain, which --policy-file carries",
		"tiers[0].medium":      "part of the tier chain, which --policy-file carries",
	},
	"POST /backup-sets/{source}/{set}/retention/apply": {
		"plan_id": "over HTTP a plan is issued, shown and applied by id; on a terminal preview and apply are one invocation, so there is no plan to name (the routes.go comment on this builder)",
	},
	"POST /backup-sets/test-connection": {
		"host":             "candidate mode, which refuses (gapCandidateConnection); a request naming backup_set_id may not carry it, since the contract allows exactly one mode",
		"user":             "candidate mode, as host",
		"ssh_key_id":       "candidate mode, as host",
		"known_hosts_line": "candidate mode, as host",
		"remote_path":      "candidate mode, as host",
	},
	"POST /operations": {
		"action":          "a selector: it picks the verb, so the line says it by being `restore` or `snapshot ...`",
		"backup_set_id":   "read only by run_backup_set, which refuses (gapRunBackupSet); every other action names its set inside its own parameters",
		"config_revision": "the browser's guard against a stale form; a terminal reads the current configuration itself",
	},
	"POST /backups/{source}/{set}/{name}/recovery": {
		"target.kind":                     "a selector: it picks the form of --to (-, a name, medium:ID, source:FOLDER)",
		"target.acknowledged_destination": "the engine's description of the source host echoed back as a yes; a terminal says yes with --acknowledge-source-write",
	},
	"POST /recoveries": {
		"target.kind":                     "a selector: it picks the form of --to (a name, medium:ID, source:FOLDER)",
		"target.acknowledged_destination": "the engine's description of the source host echoed back as a yes; a terminal says yes with --acknowledge-source-write",
	},
}

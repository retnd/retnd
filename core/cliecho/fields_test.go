package cliecho

import (
	"encoding/json"
	"slices"
	"sort"
	"strconv"
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

// It ends in "_" so no canary is a prefix of another: "script" and
// "script_sha256" fold to fieldCANARYscript_ and fieldCANARYscriptsha256_,
// where without the terminator echoing the second would count as echoing
// the first.
func fieldCanaryFor(path string) string {
	var b strings.Builder
	b.WriteString(fieldCanary)
	for _, r := range path {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	b.WriteByte('_')
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
			}
			// Only the fields this variant carries: a mode or an action
			// leaves the others out, and a field that wasn't sent can't
			// have been dropped.
			argv := strings.Join(line.Command, "\x00")
			for _, p := range paths {
				canary := fieldCanaryFor(schema + "." + p)
				if !strings.Contains(string(body.body), canary) {
					continue
				}
				if strings.Contains(argv, canary) {
					out[route][p] = true
				} else if _, ok := out[route][p]; !ok {
					out[route][p] = false
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
				out = append(out, route+" "+field+" (no body the corpus sends that route carries it and gets a line back; a refused mode is pinned in the outcome golden instead)")
			case echoed:
				out = append(out, route+" "+field+" (it is on the line now)")
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestEveryRequestFieldReachesTheLineOrIsExplained(t *testing.T) {
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

// The mutation this exists for, against a real builder: snapshot restore
// stops printing --conflict. The builder is swapped for the length of the
// test and the whole observation runs, so this proves the pipeline sees
// it, not just that the comparison works.
func TestEveryRequestField_MutationADroppedFieldIsCaught(t *testing.T) {
	const route = "POST /operations"
	saved := routes[route]
	mutated := saved
	mutated.build = func(a Action) *cmd {
		c := saved.build(a)
		if c == nil {
			return nil
		}
		for i, arg := range c.argv {
			if arg == "--conflict" {
				c.argv = append(c.argv[:i:i], c.argv[i+2:]...)
				break
			}
		}
		return c
	}
	swapRoute(t, route, mutated)

	if got := droppedFields(observeFieldEchoes(t), fieldsNotEchoed); !containsString(got, route+" snapshot_restore.conflict") {
		t.Errorf("a builder that stopped printing --conflict was not reported; got %v", got)
	}

	// And a reason saying nothing is not a reason.
	seen := fieldEchoes{route: {"snapshot_restore.conflict": false}}
	blank := map[string]map[string]string{route: {"snapshot_restore.conflict": "   "}}
	if got := droppedFields(seen, blank); !containsString(got, route+" snapshot_restore.conflict") {
		t.Errorf("a blank reason excused a dropped field; got %v", got)
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
// Most of them fall into three kinds, and the reasons say which:
//
//   - material: a password, a key, a token or a value a credential can be
//     typed into. The verb reads it on stdin or the line names it with a
//     placeholder (credentials_test.go is the other half of that rule).
//   - carried another way: a placeholder for a file the operator holds
//     (a script, a tier chain's policy file) or a path parameter.
//   - a browser handshake with no terminal counterpart: a revision token,
//     a plan id, a destination fingerprint. Each of those reasons says
//     what the pasted line acknowledges instead.
//
// A field that only goes with another mode of the request (a candidate's
// connection details beside a saved set's id) is never here: the corpus
// sends each mode on its own (canary_test.go's bodyModes).
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
		"plan_id": "binds the deletion to the plan the operator was shown. The terminal has no flag for it: `retention apply --acknowledge` previews and deletes in one go, so the pasted line deletes whatever the plan is at paste time, which may not be what was on screen (the routes.go comment on this builder)",
	},
	"POST /operations": {
		"config_revision": "binds the act to the configuration the form was drawn from, and a stale one is refused. The in-process verbs read the current revision themselves, so the pasted line acts on whatever the configuration is at paste time, not on what the operator saw",
	},
	"POST /backups/{source}/{set}/{name}/recovery": {
		"target.acknowledged_destination": "a fingerprint of the exact host, account, path and folder the operator was shown, and the engine refuses it if anything changed. The twin's --acknowledge-source-write isn't tied to it: it acknowledges whatever the set resolves to when the line is run (recovery only ever writes into a new folder, so nothing there is replaced)",
	},
	"POST /recoveries": {
		"target.acknowledged_destination": "a fingerprint of the exact host, account, path and folder the operator was shown, and the engine refuses it if anything changed. The twin's --acknowledge-source-write isn't tied to it: it acknowledges whatever the set resolves to when the line is run (recovery only ever writes into a new folder, so nothing there is replaced)",
	},
}

// ------------------------------------------- booleans and numbers ---

// The other 77 fields (#1253's review). A boolean or a number has no
// canary to look for, since a true or a 7 in the argv could have come from
// anywhere, so it is checked by difference instead: echo the body, echo
// it again with that one field changed (a boolean flipped, a number set to
// 0 and to 4321), and the field reaches the line if the line changes.
// That covers a builder that prints a flag only when a value is set, and
// one that switches a branch on it.
//
// This is what found PATCH /settings/workflow dropping {"disabled":true}
// and skip_connection_check never becoming --no-verify: three requests
// whose lines did less than the click, all of them booleans.

// leafPaths lists the paths of every non-string leaf in a decoded body,
// in the wire's own names.
func leafPaths(node any, prefix string, out *[]string) {
	switch x := node.(type) {
	case map[string]any:
		for _, k := range sortedKeys(x) {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			leafPaths(x[k], p, out)
		}
	case []any:
		for i, e := range x {
			leafPaths(e, prefix+"["+strconv.Itoa(i)+"]", out)
		}
	case bool, float64:
		*out = append(*out, prefix)
	}
}

// withLeaf returns a copy of a decoded body with the leaf at path set to v.
func withLeaf(node any, path string, v any) any {
	out := deepCopy(node)
	cur := out
	steps := strings.Split(path, ".")
	for i, step := range steps {
		name, index, indexed := strings.Cut(step, "[")
		obj := cur.(map[string]any)
		last := i == len(steps)-1
		if !indexed {
			if last {
				obj[name] = v
				return out
			}
			cur = obj[name]
			continue
		}
		n, _ := strconv.Atoi(strings.TrimSuffix(index, "]"))
		arr := obj[name].([]any)
		if last {
			arr[n] = v
			return out
		}
		cur = arr[n]
	}
	return out
}

// booleansOff is a copy of a decoded body with every boolean false, so a
// branch that only runs when a flag is off is reached too. The corpus
// sets every boolean true, which is how the --all branch of a set
// recovery onto the source host was never walked by anything.
func booleansOff(node any) any {
	switch x := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = booleansOff(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = booleansOff(e)
		}
		return out
	case bool:
		return false
	default:
		return x
	}
}

// outcome is what one echo comes to, for comparing two of them.
func outcome(line Line) string {
	if len(line.Command) > 0 {
		return strings.Join(line.Command, "\x00")
	}
	return "gap: " + line.GapDetail
}

// observeNonStringEchoes drives every builder with its own schema's
// bodies, both as the corpus builds them and with every boolean off, and
// records for each non-string leaf whether changing it changes the line.
func observeNonStringEchoes(t *testing.T) fieldEchoes {
	t.Helper()
	params := map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar", "id": "offsite_s3", "op": "op-1", "grant": "g-1", "run": "r-1", "domain": "d-1"}
	bySchema := map[string][]canaryBody{}
	for _, b := range canaryBodies(t, fieldCanaryFor) {
		bySchema[b.schema] = append(bySchema[b.schema], b)
	}
	out := fieldEchoes{}
	for route, schema := range requestSchemaByRoute() {
		e, ok := routes[route]
		if !ok || e.build == nil {
			continue
		}
		method, path, _ := strings.Cut(route, " ")
		echo := func(body any) Line {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			return Echo(Action{Method: method, Route: path, Params: params, Body: raw})
		}
		for _, b := range bySchema[schema] {
			var decoded any
			if err := json.Unmarshal(b.body, &decoded); err != nil {
				t.Fatal(err)
			}
			for _, base := range []any{decoded, booleansOff(decoded)} {
				baseLine := echo(base)
				var leaves []string
				leafPaths(base, "", &leaves)
				for _, leaf := range leaves {
					var alternatives []any
					switch v := leafValue(base, leaf).(type) {
					case bool:
						alternatives = []any{!v}
					case float64:
						alternatives = []any{float64(0), float64(4321)}
					}
					changed, anyLine := false, len(baseLine.Command) > 0
					for _, alt := range alternatives {
						line := echo(withLeaf(base, leaf, alt))
						anyLine = anyLine || len(line.Command) > 0
						if outcome(line) != outcome(baseLine) {
							changed = true
						}
					}
					if !anyLine {
						// Refused either way: nothing here to have dropped.
						continue
					}
					if out[route] == nil {
						out[route] = map[string]bool{}
					}
					out[route][leaf] = out[route][leaf] || changed
				}
			}
		}
	}
	return out
}

// leafValue reads the leaf at path, which leafPaths produced.
func leafValue(node any, path string) any {
	cur := node
	for _, step := range strings.Split(path, ".") {
		name, index, indexed := strings.Cut(step, "[")
		cur = cur.(map[string]any)[name]
		if indexed {
			n, _ := strconv.Atoi(strings.TrimSuffix(index, "]"))
			cur = cur.([]any)[n]
		}
	}
	return cur
}

func TestEveryNonStringFieldReachesTheLineOrIsExplained(t *testing.T) {
	seen := observeNonStringEchoes(t)
	if len(seen) == 0 {
		t.Fatal("no builder took a body with a boolean or a number in it, so this checked nothing")
	}
	for _, dropped := range droppedFields(seen, nonStringFieldsNotEchoed) {
		at := strings.LastIndex(dropped, " ")
		t.Errorf("%s is a field of the request %s takes, and changing it doesn't change the line. An operator copying that line gets the same command whatever the click said. Print it, or add it to nonStringFieldsNotEchoed under %q with the reason.",
			dropped[at+1:], dropped[:at], dropped[:at])
	}
	for _, stale := range staleFieldReasons(seen, nonStringFieldsNotEchoed) {
		t.Errorf("nonStringFieldsNotEchoed explains %s. Delete the line.", stale)
	}
}

// The mutation, against a real builder: --sample-percent is dropped from
// snapshot verify, the exact one the review showed going unnoticed.
func TestEveryNonStringField_MutationADroppedNumberIsCaught(t *testing.T) {
	const route = "POST /operations"
	saved := routes[route]
	mutated := saved
	mutated.build = func(a Action) *cmd {
		c := saved.build(a)
		if c == nil {
			return nil
		}
		for i, arg := range c.argv {
			if arg == "--sample-percent" {
				c.argv = append(c.argv[:i:i], c.argv[i+2:]...)
				break
			}
		}
		return c
	}
	swapRoute(t, route, mutated)

	got := droppedFields(observeNonStringEchoes(t), nonStringFieldsNotEchoed)
	if !slices.Contains(got, route+" snapshot_verify.sample_percent") {
		t.Errorf("a builder that stopped printing --sample-percent was not reported; got %v", got)
	}
}

// nonStringFieldsNotEchoed is the boolean and number half of
// fieldsNotEchoed, with the same rule.
var nonStringFieldsNotEchoed = map[string]map[string]string{
	"PATCH /settings": {
		"retention.tiers[0].keep":        "part of the tier chain, which --policy-file carries",
		"retention.tiers[0].period_days": "part of the tier chain, which --policy-file carries",
	},
	"PUT /backup-sets/{source}/{set}/retention": {
		"tiers[0].keep":        "part of the tier chain, which --policy-file carries",
		"tiers[0].period_days": "part of the tier chain, which --policy-file carries",
	},
	"POST /operations": {
		"snapshot_restore.acknowledged":      "the HTTP destructive gate's yes to writing into target_path; `snapshot restore` runs in the operator's own process and takes no acknowledgement, so the pasted line writes there without one",
		"snapshot_hold_release.acknowledged": "the HTTP route's yes to letting retention prune the snapshot; `snapshot unhold` takes no acknowledgement, so the pasted line releases the hold without one",
	},
	"POST /storage-mediums/preflight": {
		"skip_connection_check": "a preflight IS the connection check, so there is nothing to skip; the field is on this body because it shares StorageMediumRequest with add and edit, which do echo it as --no-verify",
	},
}

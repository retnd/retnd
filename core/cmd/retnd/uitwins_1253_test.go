package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The browser's own command twins, fed through the real dispatcher (issue
// #1253).
//
// TestEveryEchoedCommandParses covers every line core/cliecho prints, and
// that is the terminal's half. The other half is printed by the browser
// before any request is made, beside the control, by four TypeScript
// modules (assuranceCommands.ts, snapshotCommands.ts,
// storageDestinationCommands.ts, destinationConfigureCommands.ts). Those
// were only ever compared with strings their own tests spelled out, so a
// flag the CLI never had could sit in both the builder and its test and
// be green: the destination wizard printed `medium add --backend` that way
// until #1252.
//
// ui/shared/src/test/cliTwins.1253.test.ts writes every line those
// modules print, for a fixed set of inputs, to testdata/ui_twins_1253.json
// and fails when the modules and the file disagree. This reads the file
// and runs each line the way the test above runs cliecho's: through `run`,
// with --config pointing at a file that won't load, so a line the parser
// accepts exits 1 and a line it refuses exits 2.
//
// A browser line is meant for a person, and it can carry two things a
// shell would read differently from the argv it stands for: a
// <placeholder> where the browser has no value yet (or withholds one),
// and a `> file` redirect where a download goes. A placeholder is swapped
// for a stand-in of the right shape and the redirect is dropped, because
// what this checks is the verb and its flags. Every placeholder needs a
// stand-in written down below, so a new one is a decision rather than a
// guess.

const uiTwins1253 = "testdata/ui_twins_1253.json"

const uiTwinsRepin1253 = "npx vitest run -u src/test/cliTwins.1253.test.ts (from ui/shared)"

type uiTwinFile struct {
	About string   `json:"about"`
	Twins []uiTwin `json:"twins"`
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
	"date":     "2026-10-01",
	"duration": "24h",
	"dir":      "/srv/restored",
	"id":       "offsite_s3",
	"name":     "restore",
	"the command that prints these credentials":                        "credential-helper",
	"the value, left out because it looks like it carries credentials": "https://s3.example.test",
}

// readTwinLine turns one browser line into the argv it stands for.
//
// It reads what the TypeScript shellQuote writes (bare words, and single
// quotes with '\” for an embedded quote) plus the two things a twin can
// carry that a cliecho line can't: an unquoted <placeholder>, which may
// have spaces in it and may sit inside a word (medium:<id>), and an
// unquoted `>` redirect, which is dropped along with its target. A
// placeholder with no stand-in is returned in unknown.
func readTwinLine(line string) (argv []string, unknown []string) {
	var (
		cur      strings.Builder
		inWord   bool
		quoted   bool
		redirect bool
		skipWord bool
	)
	flushWord := func() {
		if !inWord {
			return
		}
		if skipWord {
			skipWord = false
		} else {
			argv = append(argv, cur.String())
		}
		cur.Reset()
		inWord = false
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
			quoted, inWord = true, true
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			inWord = true
		case c == '<':
			end := strings.IndexByte(line[i:], '>')
			if end < 0 {
				cur.WriteByte(c)
				inWord = true
				continue
			}
			inner := line[i+1 : i+end]
			i += end
			inWord = true
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
		case c == '>' && !inWord:
			redirect = true
		case c == ' ' || c == '\t':
			flushWord()
			if redirect && !inWord {
				// The next word is the redirect's target.
				skipWord, redirect = true, false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	flushWord()
	return argv, unknown
}

// refusedTwins runs each twin and reports the ones the dispatcher refuses
// as a bad command line, along with any whose placeholder has no
// stand-in.
func refusedTwins(t *testing.T, twins []uiTwin, configPath string) []string {
	t.Helper()
	var out []string
	for _, tw := range twins {
		label := tw.Module + "." + tw.Fn + " (" + tw.Case + ")"
		argv, unknown := readTwinLine(tw.Line)
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

func TestEveryUITwinParses1253(t *testing.T) {
	clearTheRoute(t)
	raw, err := os.ReadFile(uiTwins1253)
	if err != nil {
		t.Fatalf("reading %s: %v\nIt is written by ui/shared's vitest suite. Re-pin with: %s", uiTwins1253, err, uiTwinsRepin1253)
	}
	var file uiTwinFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("%s is not the fixture this test reads: %v", uiTwins1253, err)
	}

	// Vacuity, per module: a fixture that lost one of the four would
	// otherwise leave that module unchecked and this test green.
	perModule := map[string]int{}
	for _, tw := range file.Twins {
		perModule[tw.Module]++
	}
	for _, m := range []string{"assuranceCommands", "snapshotCommands", "storageDestinationCommands", "destinationConfigureCommands"} {
		if perModule[m] == 0 {
			t.Errorf("%s has no line from %s, so nothing here checks what that module prints. Re-pin with: %s", uiTwins1253, m, uiTwinsRepin1253)
		}
	}

	for _, refused := range refusedTwins(t, file.Twins, unparseableConfig(t)) {
		t.Errorf("the browser prints a line this binary won't parse. An operator pastes it and gets exit 2, so fix the TypeScript twin (and re-pin with %s), or the CLI if the twin is right:\n%s", uiTwinsRepin1253, refused)
	}
}

// The mutation this exists for: a twin with a flag the CLI never had. It
// has to be refused, and the same line without it has to get through, so
// the refusal is about the flag and not about the reader.
func TestEveryUITwinParses1253_MutationAnUnknownFlagIsCaught(t *testing.T) {
	configPath := unparseableConfig(t)
	bad := []uiTwin{{Module: "storageDestinationCommands", Fn: "addCommand", Case: "mutation",
		Line: "retnd medium add offsite_s3 --backend s3 --bucket acme"}}
	if got := refusedTwins(t, bad, configPath); len(got) != 1 {
		t.Fatalf("a twin carrying --backend on `medium add`, the line #1252 fixed, was not refused; got %q", got)
	}

	good := []uiTwin{{Module: "storageDestinationCommands", Fn: "addCommand", Case: "control",
		Line: "retnd medium add offsite_s3 --type s3 --bucket acme"}}
	if got := refusedTwins(t, good, configPath); len(got) != 0 {
		t.Fatalf("the same line with a flag `medium add` has is refused too, so the case above proves nothing about the flag: %q", got)
	}

	unknown := []uiTwin{{Module: "assuranceCommands", Fn: "thawCommand", Case: "mutation",
		Line: "retnd restore x --medium <a placeholder nobody wrote down> --acknowledge"}}
	if got := refusedTwins(t, unknown, configPath); len(got) != 1 {
		t.Fatalf("a placeholder with no stand-in was not reported; got %q", got)
	}
}

// readTwinLine's own control. Every line above goes through it, so a
// reader that mangled a quote or kept a redirect would decide what the
// test sees.
func TestReadTwinLine1253(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"retnd scrub api-server/var-backups", []string{"retnd", "scrub", "api-server/var-backups"}},
		{"retnd snapshot hold s/a --reason 'it'\\''s evidence'", []string{"retnd", "snapshot", "hold", "s/a", "--reason", "it's evidence"}},
		{"retnd recover a/b/c --acknowledge-cost --to - > recovered-file", []string{"retnd", "recover", "a/b/c", "--acknowledge-cost", "--to", "-"}},
		{"retnd recover a/b/c --to - > 'a file'", []string{"retnd", "recover", "a/b/c", "--to", "-"}},
		{"retnd recover a/b/c --to medium:<id>", []string{"retnd", "recover", "a/b/c", "--to", "medium:offsite_s3"}},
		{"retnd medium add x --credentials-command <the command that prints these credentials>", []string{"retnd", "medium", "add", "x", "--credentials-command", "credential-helper"}},
		{"retnd medium configure c --backend s3 --field <endpoint=the value, left out because it looks like it carries credentials>", []string{"retnd", "medium", "configure", "c", "--backend", "s3", "--field", "endpoint=https://s3.example.test"}},
	} {
		got, unknown := readTwinLine(tc.line)
		if len(unknown) != 0 || strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
			t.Errorf("readTwinLine(%q) = %q (unknown %q), want %q", tc.line, got, unknown, tc.want)
		}
	}
}

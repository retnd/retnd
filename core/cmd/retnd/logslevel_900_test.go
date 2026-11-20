package main

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
	"github.com/retnd/retnd/core/internal/config"
)

// observability.logs.level, held in one shape on all four sides (EPIC O,
// FR-100, #900).
//
// The level is the one free-text value in the observability block, and it
// is spelled in four places that never read each other: the levels
// config.yaml accepts (core/internal/config), the flag the CLI parses, the
// line core/cliecho prints for PATCH /settings/observability {logs_level},
// and the line the browser prints beside the select (logsLevelCommand in
// ui/shared/src/observability/observabilityCommands.ts, one twin per entry
// of that module's LOG_LEVELS). TestEveryUITwinParses proves each browser
// line parses. It can't see a level the select offers that config refuses,
// a level config accepts that the select never offers, or a browser line
// that parses but isn't the one the terminal prints. This does.
//
// The accepted set is read from the validator's own refusal (it names every
// level it takes), so a fifth level added to config with its message
// updated turns this red until the browser offers it too.

const (
	logsLevelTwinModule = "observability/observabilityCommands"
	logsLevelTwinFn     = "logsLevelCommand"
)

// logsLevelRefused reports whether config's validator refuses level, and
// the validator's error text when it does.
func logsLevelRefused(level string) (bool, string) {
	c := &config.Config{ObservabilityBlock: &config.ObservabilityBlock{Logs: &config.ObservabilityLogsBlock{Level: level}}}
	err := c.Validate()
	if err == nil {
		return false, ""
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.Contains(line, "observability.logs.level") {
			return true, line
		}
	}
	return false, ""
}

var quotedWord = regexp.MustCompile(`"([^"]*)"`)

// acceptedLogLevels is every level config takes, as its refusal of a level
// it doesn't take lists them.
func acceptedLogLevels(t *testing.T) []string {
	t.Helper()
	const bogus = "not-a-level"
	refused, msg := logsLevelRefused(bogus)
	if !refused {
		t.Fatalf("config accepted observability.logs.level %q, so nothing below can tell an accepted level from a refused one", bogus)
	}
	_, list, ok := strings.Cut(msg, "is not one of")
	if !ok {
		t.Fatalf("the refusal no longer lists the levels it takes, which is where this test reads them from: %q", msg)
	}
	var out []string
	for _, m := range quotedWord.FindAllStringSubmatch(list, -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("no level could be read out of the refusal %q", msg)
	}
	return out
}

// logsLevelTwinProblems compares the browser's logs-level lines with the
// accepted levels and with what core/cliecho prints for the same request.
func logsLevelTwinProblems(twins []uiTwin, accepted []string) []string {
	var problems []string
	offered := map[string]bool{}
	for _, tw := range twins {
		if tw.Module != logsLevelTwinModule || tw.Fn != logsLevelTwinFn {
			continue
		}
		argv, unknown, bad := readTwinLine(tw.Line)
		if len(unknown) > 0 || len(bad) > 0 || len(argv) == 0 {
			problems = append(problems, "not one plain command: "+tw.Line)
			continue
		}
		level := argv[len(argv)-1]
		if offered[level] {
			problems = append(problems, "the level "+level+" is offered twice")
		}
		offered[level] = true
		if !slices.Contains(accepted, level) {
			problems = append(problems, "the browser offers "+level+", which config.yaml refuses (it takes "+strings.Join(accepted, ", ")+"):\n  "+tw.Line)
		}
		body, err := json.Marshal(apicontract.UpdateObservabilitySettingsRequest{LogsLevel: &level})
		if err != nil {
			problems = append(problems, "marshalling the request for "+level+": "+err.Error())
			continue
		}
		want := cliecho.Echo(cliecho.Action{Method: "PATCH", Route: "/settings/observability", Body: body}).Shell()
		if tw.Line != want {
			problems = append(problems, "for "+level+" the browser prints\n  "+tw.Line+"\nand the terminal prints\n  "+want)
		}
	}
	for _, level := range accepted {
		if !offered[level] {
			problems = append(problems, "config.yaml takes "+level+" and the browser has no line for it, so the select can't offer it either")
		}
	}
	return problems
}

func TestLogsLevelTwinIsTheSchema_900(t *testing.T) {
	accepted := acceptedLogLevels(t)
	for _, level := range accepted {
		if refused, msg := logsLevelRefused(level); refused {
			t.Errorf("the refusal lists %q as a level, and config refuses it: %s", level, msg)
		}
	}

	file := readUITwins(t)
	if !slices.ContainsFunc(file.Twins, func(tw uiTwin) bool { return tw.Module == logsLevelTwinModule && tw.Fn == logsLevelTwinFn }) {
		t.Fatalf("%s has no %s line, so the log level control echoes nothing the CLI checks. Re-pin with: %s", uiTwins, logsLevelTwinFn, uiTwinsRepin)
	}
	for _, p := range logsLevelTwinProblems(file.Twins, accepted) {
		t.Errorf("observability.logs.level has drifted between the browser, the terminal and config.yaml. Fix the side that is wrong and re-pin with %s:\n%s", uiTwinsRepin, p)
	}
}

// The drift shapes this exists for, each red, and the correct set green so
// the reds are about the drift and not about the reader.
func TestLogsLevelTwinIsTheSchema_900_MutationsAreCaught(t *testing.T) {
	accepted := acceptedLogLevels(t)
	line := func(level string) uiTwin {
		return uiTwin{Module: logsLevelTwinModule, Fn: logsLevelTwinFn, Case: level, Line: "retnd settings observability patch --logs-level " + level}
	}
	var good []uiTwin
	for _, l := range accepted {
		good = append(good, line(l))
	}
	if got := logsLevelTwinProblems(good, accepted); len(got) != 0 {
		t.Fatalf("the correct lines are refused, so the reds below prove nothing: %q", got)
	}

	renamed := slices.Clone(good)
	renamed[0].Line = strings.Replace(renamed[0].Line, "--logs-level", "--log-level", 1)

	extra := append(slices.Clone(good), line("trace"))

	missing := slices.Clone(good[:len(good)-1])

	field := slices.Clone(good)
	field[0].Line = strings.Replace(field[0].Line, "patch --logs-level", "patch --logs_level", 1)

	for name, tc := range map[string]struct {
		twins []uiTwin
		names string
	}{
		"the flag renamed in the browser":        {renamed, "--log-level"},
		"a level config refuses":                 {extra, "trace"},
		"a level config takes and nobody offers": {missing, accepted[len(accepted)-1]},
		"the wire field's spelling as the flag":  {field, "--logs_level"},
	} {
		t.Run(name, func(t *testing.T) {
			got := logsLevelTwinProblems(tc.twins, accepted)
			if len(got) == 0 {
				t.Fatalf("not caught")
			}
			if !strings.Contains(strings.Join(got, "\n"), tc.names) {
				t.Errorf("caught without naming %q: %q", tc.names, got)
			}
		})
	}

	if refused, _ := logsLevelRefused("trace"); !refused {
		t.Errorf("config accepted %q, so the reader of the accepted set can't say no", "trace")
	}
}

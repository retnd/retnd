package main

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/apicontract"
	"github.com/retnd/retnd/core/cliecho"
)

// The alert inputs' terminal lines, held to core/cliecho byte for byte (EPIC
// O, FR-101, #1604).
//
// The Observability card prints a line under its alert controls, one per
// input, from the builders in ui/shared/src/observability/observabilityCommands.ts.
// TestEveryUITwinParses proves each line parses. It can't see a line that
// parses and still isn't what the terminal prints for the same request: a
// duration written as 240s where cliecho writes 4m, a boolean passed as a
// following word where cliecho assigns it, a flag spelled the way the wire
// field is. This does, the way TestLogsLevelTwinIsTheSchema_900 does for the
// log level: it reads the value back off each browser line, builds the PATCH
// body that value stands for, and asks cliecho for its line.

// alertTwinRequests maps each alert builder to the PATCH it stands for, given
// the value its line carries.
var alertTwinRequests = map[string]func(value string) (apicontract.UpdateObservabilitySettingsRequest, error){
	"alertsDeliveryCommand": func(v string) (apicontract.UpdateObservabilitySettingsRequest, error) {
		return apicontract.UpdateObservabilitySettingsRequest{AlertsDelivery: &v}, nil
	},
	"webhookDialectCommand": func(v string) (apicontract.UpdateObservabilitySettingsRequest, error) {
		return apicontract.UpdateObservabilitySettingsRequest{WebhookDialect: &v}, nil
	},
	"webhookAllowInsecureHttpCommand": func(v string) (apicontract.UpdateObservabilitySettingsRequest, error) {
		b, err := strconv.ParseBool(v)
		return apicontract.UpdateObservabilitySettingsRequest{WebhookAllowInsecureHttp: &b}, err
	},
	"webhookRefreshIntervalCommand": func(v string) (apicontract.UpdateObservabilitySettingsRequest, error) {
		d, err := time.ParseDuration(v)
		s := int64(d / time.Second)
		return apicontract.UpdateObservabilitySettingsRequest{WebhookRefreshIntervalSeconds: &s}, err
	},
	"webhookResolveTtlCommand": func(v string) (apicontract.UpdateObservabilitySettingsRequest, error) {
		d, err := time.ParseDuration(v)
		s := int64(d / time.Second)
		return apicontract.UpdateObservabilitySettingsRequest{WebhookResolveTtlSeconds: &s}, err
	},
}

// alertTwinProblems compares each browser alert line with what core/cliecho
// prints for the request it stands for, and names every builder with no line.
func alertTwinProblems(twins []uiTwin) []string {
	var problems []string
	seen := map[string]bool{}
	for _, tw := range twins {
		request, ok := alertTwinRequests[tw.Fn]
		if tw.Module != logsLevelTwinModule || !ok {
			continue
		}
		seen[tw.Fn] = true
		argv, unknown, bad := readTwinLine(tw.Line)
		if len(unknown) > 0 || len(bad) > 0 || len(argv) == 0 {
			problems = append(problems, tw.Fn+" ("+tw.Case+") is not one plain command: "+tw.Line)
			continue
		}
		// The value is the last word, or what follows = in an assigned flag.
		value := argv[len(argv)-1]
		if strings.HasPrefix(value, "--") {
			_, value, _ = strings.Cut(value, "=")
		}
		req, err := request(value)
		if err != nil {
			problems = append(problems, tw.Fn+" ("+tw.Case+") carries a value its field can't hold, "+strconv.Quote(value)+": "+err.Error()+"\n  "+tw.Line)
			continue
		}
		body, err := json.Marshal(req)
		if err != nil {
			problems = append(problems, tw.Fn+" ("+tw.Case+"): marshalling the request: "+err.Error())
			continue
		}
		want := cliecho.Echo(cliecho.Action{Method: "PATCH", Route: "/settings/observability", Body: body}).Shell()
		if tw.Line != want {
			problems = append(problems, tw.Fn+" ("+tw.Case+"): the browser prints\n  "+tw.Line+"\nand the terminal prints\n  "+want)
		}
	}
	names := make([]string, 0, len(alertTwinRequests))
	for fn := range alertTwinRequests {
		names = append(names, fn)
	}
	slices.Sort(names)
	for _, fn := range names {
		if !seen[fn] {
			problems = append(problems, "the fixture has no "+fn+" line, so the alert input it echoes is checked by nothing")
		}
	}
	return problems
}

func TestAlertTwinsAreTheEcho_1604(t *testing.T) {
	file := readUITwins(t)
	for _, p := range alertTwinProblems(file.Twins) {
		t.Errorf("an alert input's terminal line has drifted from core/cliecho. Fix the side that is wrong and re-pin with %s:\n%s", uiTwinsRepin, p)
	}
}

// The drift shapes this exists for, each red and naming what drifted, and the
// correct lines green so the reds are about the drift and not about the reader.
func TestAlertTwinsAreTheEcho_1604_MutationsAreCaught(t *testing.T) {
	const patch = "retnd settings observability patch "
	line := func(fn, flags string) uiTwin {
		return uiTwin{Module: logsLevelTwinModule, Fn: fn, Case: flags, Line: patch + flags}
	}
	good := []uiTwin{
		line("alertsDeliveryCommand", "--alerts-delivery webhook"),
		line("alertsDeliveryCommand", "--alerts-delivery 'x; id|sh'"),
		line("webhookDialectCommand", "--webhook-dialect alertmanager"),
		line("webhookAllowInsecureHttpCommand", "--webhook-allow-insecure-http=true"),
		line("webhookAllowInsecureHttpCommand", "--webhook-allow-insecure-http=false"),
		line("webhookRefreshIntervalCommand", "--webhook-refresh-interval 4m"),
		line("webhookResolveTtlCommand", "--webhook-resolve-ttl 1h10s"),
	}
	if got := alertTwinProblems(good); len(got) != 0 {
		t.Fatalf("the correct lines are refused, so the reds below prove nothing: %q", got)
	}

	with := func(i int, flags string) []uiTwin {
		out := slices.Clone(good)
		out[i] = line(out[i].Fn, flags)
		return out
	}
	for name, tc := range map[string]struct {
		twins []uiTwin
		names string
	}{
		"a duration in seconds where cliecho writes minutes": {with(5, "--webhook-refresh-interval 240s"), "--webhook-refresh-interval 4m"},
		"a boolean as a following word":                      {with(3, "--webhook-allow-insecure-http true"), "--webhook-allow-insecure-http=true"},
		"the flag renamed in the browser":                    {with(0, "--alert-delivery webhook"), "--alert-delivery"},
		"the wire field's spelling as the flag":              {with(2, "--webhook_dialect alertmanager"), "--webhook_dialect"},
		"a value left unquoted":                              {with(1, "--alerts-delivery x; id|sh"), "not one plain command"},
		"a builder with no line":                             {slices.Delete(slices.Clone(good), 6, 7), "webhookResolveTtlCommand"},
	} {
		t.Run(name, func(t *testing.T) {
			got := alertTwinProblems(tc.twins)
			if len(got) == 0 {
				t.Fatalf("not caught")
			}
			if !strings.Contains(strings.Join(got, "\n"), tc.names) {
				t.Errorf("caught without naming %q: %q", tc.names, got)
			}
		})
	}
}

package cliecho

import (
	"strings"
	"testing"
)

// #1221: a validator's env travels in the request body, so the terminal line
// that stands for the request has to carry it as --env.

func TestValidators1221_APostEchoesTheEnvItOptsInTo(t *testing.T) {
	line := Echo(Action{Method: "POST", Route: "/assurance/validators",
		Body: []byte(`{"name":"dumps","pattern":"\\.sql$","env":"workflow","script":"#!/usr/bin/env bash\nexit 0\n"}`)}).Shell()
	if !strings.Contains(line, "assurance validators add dumps") || !strings.Contains(line, "--env workflow") {
		t.Errorf("echo = %q", line)
	}
	plain := Echo(Action{Method: "POST", Route: "/assurance/validators",
		Body: []byte(`{"name":"dumps","pattern":"\\.sql$","script":"#!/usr/bin/env bash\nexit 0\n"}`)}).Shell()
	if strings.Contains(plain, "--env") {
		t.Errorf("a request that leaves env out echoes %q: the default is not a flag", plain)
	}
}

func TestValidators1221_APutEchoesTheEnvItSaysAndNothingElse(t *testing.T) {
	put := func(body string) string {
		return Echo(Action{Method: "PUT", Route: "/assurance/validators/{name}", Params: map[string]string{"name": "manifest"}, Body: []byte(body)}).Shell()
	}
	if got := put(`{"pattern":"^M","env":"workflow","script":"#!/usr/bin/env bash\nexit 0\n"}`); !strings.Contains(got, "--env workflow") || !strings.Contains(got, "--global") {
		t.Errorf("a replacement that opts in echoes %q", got)
	}
	if got := put(`{"pattern":"^M","env":"minimal","script":"#!/usr/bin/env bash\nexit 0\n"}`); !strings.Contains(got, "--env minimal") {
		t.Errorf("a replacement that says minimal echoes %q", got)
	}
	if got := put(`{"pattern":"^M","script":"#!/usr/bin/env bash\nexit 0\n"}`); strings.Contains(got, "--env") {
		t.Errorf("a replacement that leaves env out echoes %q", got)
	}
}

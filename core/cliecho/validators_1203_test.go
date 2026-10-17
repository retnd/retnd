package cliecho

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// #1203 review: the noun is validators, an edit says where it goes, and the
// script placeholder says which script it stands for.

func TestValidators1203_TheNounIsPlural(t *testing.T) {
	for _, a := range []Action{
		{Method: "GET", Route: "/assurance/validators"},
		{Method: "DELETE", Route: "/assurance/validators/{name}", Params: map[string]string{"name": "manifest"}},
		{Method: "GET", Route: "/assurance/validators/{name}/script", Params: map[string]string{"name": "manifest"}},
		{Method: "GET", Route: "/assurance/validator-patterns"},
		{Method: "GET", Route: "/assurance/default-validator-script"},
	} {
		got := Echo(a).Shell()
		if !strings.Contains(got, " assurance validators ") {
			t.Errorf("%s %s echoes %q", a.Method, a.Route, got)
		}
	}
}

func TestValidators1203_APutEchoesWhereItGoesAndWhichScript(t *testing.T) {
	script := "#!/usr/bin/env bash\nexit 0\n"
	sum := sha256.Sum256([]byte(script))
	hash := hex.EncodeToString(sum[:])

	set := Echo(Action{Method: "PUT", Route: "/assurance/validators/{name}", Params: map[string]string{"name": "manifest"},
		Body: []byte(`{"pattern":"^M","backup_set_id":"api-server/var-backups","script_sha256":"` + hash + `"}`)}).Shell()
	if !strings.Contains(set, "assurance validators update manifest --pattern '^M' --backup-set api-server/var-backups") || strings.Contains(set, "--global") {
		t.Errorf("a set-scoped keep echoes %q", set)
	}
	if strings.Contains(set, "--script") {
		t.Errorf("keeping the script echoes a --script: %q", set)
	}

	global := Echo(Action{Method: "PUT", Route: "/assurance/validators/{name}", Params: map[string]string{"name": "manifest"},
		Body: []byte(`{"pattern":"^M","script":"#!/usr/bin/env bash\nexit 0\n"}`)})
	if !strings.Contains(global.Shell(), "--global") || strings.Contains(global.Shell(), "--backup-set") {
		t.Errorf("a global replacement echoes %q", global.Shell())
	}
	if !global.Placeholder || !strings.Contains(global.Shell(), "--script") || !strings.Contains(global.Shell(), "26 bytes") || !strings.Contains(global.Shell(), hash) {
		t.Errorf("the script placeholder does not say which script it is (size and sha256): %q", global.Shell())
	}
}

func TestValidators1203_APostPlaceholderCarriesTheScriptsSizeAndHash(t *testing.T) {
	script := "#!/usr/bin/env bash\nexit 0\n"
	sum := sha256.Sum256([]byte(script))
	hash := hex.EncodeToString(sum[:])
	line := Echo(Action{Method: "POST", Route: "/assurance/validators",
		Body: []byte(`{"name":"dumps","pattern":"\\.sql$","script":"#!/usr/bin/env bash\nexit 0\n"}`)})
	if !line.Placeholder || !strings.Contains(line.Shell(), "26 bytes") || !strings.Contains(line.Shell(), hash) {
		t.Errorf("echo = %q", line.Shell())
	}
	if strings.Contains(line.Shell(), "exit 0") {
		t.Errorf("the script's body reached the echo: %q", line.Shell())
	}
}

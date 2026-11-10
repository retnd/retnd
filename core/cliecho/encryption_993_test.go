package cliecho

import (
	"strings"
	"testing"
)

// #993: every encryption route has a command, and no command line the echo
// prints can carry what a key reference's command would have in its
// arguments.

func TestTheEncryptionRoutesNameTheirCommands_993(t *testing.T) {
	set := map[string]string{"source": "api-server", "set": "var-backups"}
	for _, tc := range []struct {
		name   string
		action Action
		want   string
	}{
		{"reading the policy", Action{Method: "GET", Route: "/encryption/policy"}, Binary + " encryption policy"},
		{"patching to required with the managed key", Action{Method: "PATCH", Route: "/encryption/policy",
			Body: []byte(`{"mode":"required","key_provider":{"managed":true}}`)}, Binary + " encryption policy patch --mode required --managed"},
		{"patching to an external file", Action{Method: "PATCH", Route: "/encryption/policy",
			Body: []byte(`{"key_provider":{"external":{"file":"/run/secrets/key"}}}`)}, Binary + " encryption policy patch --external-file /run/secrets/key"},
		{"resetting the policy", Action{Method: "POST", Route: "/encryption/policy/reset"}, Binary + " encryption policy reset"},
		{"a set's own block", Action{Method: "PUT", Route: "/backup-sets/{source}/{set}/encryption", Params: set,
			Body: []byte(`{"mode":"required","key_provider":{"managed":true}}`)}, Binary + " encryption backup-set api-server/var-backups --mode required --managed"},
		{"clearing a set's block", Action{Method: "DELETE", Route: "/backup-sets/{source}/{set}/encryption", Params: set},
			Binary + " encryption backup-set api-server/var-backups --reset"},
		{"listing keys", Action{Method: "GET", Route: "/encryption/keys"}, Binary + " encryption key list"},
		{"creating a key", Action{Method: "POST", Route: "/encryption/keys"}, Binary + " encryption key create"},
		{"rotating", Action{Method: "POST", Route: "/encryption/keys/rotate"}, Binary + " encryption key rotate"},
		{"retiring", Action{Method: "POST", Route: "/encryption/keys/{id}/retire", Params: map[string]string{"id": "k01"}}, Binary + " encryption key retire k01"},
		{"confirming the newest export", Action{Method: "POST", Route: "/encryption/keys/{id}/confirm", Params: map[string]string{"id": "k01"}},
			Binary + " encryption key confirm k01"},
		{"verifying a copy", Action{Method: "POST", Route: "/encryption/verify",
			Body: []byte(`{"artifact_id":"api-server/var-backups/a.dump","medium":"offsite","acknowledge_egress":true}`)},
			Binary + " encryption verify api-server/var-backups/a.dump --medium offsite --allow-egress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := Echo(tc.action)
			if line.Gap != "" {
				t.Fatalf("this route still says there is no equivalent: %s", line.Gap)
			}
			if got := line.Shell(); got != tc.want {
				t.Errorf("prints\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestAKeyReferencesCommandArgumentsAreNeverEchoed_993(t *testing.T) {
	const secretArg = "TOPSECRET-ARGUMENT"
	for _, tc := range []Action{
		{Method: "PATCH", Route: "/encryption/policy", Body: []byte(`{"key_provider":{"external":{"command":["vault","kv","get","` + secretArg + `"]}}}`)},
		{Method: "PUT", Route: "/backup-sets/{source}/{set}/encryption", Params: map[string]string{"source": "a", "set": "b"},
			Body: []byte(`{"mode":"required","key_provider":{"external":{"command":["vault","kv","get","` + secretArg + `"]}}}`)},
		{Method: "POST", Route: "/encryption/providers/test", Body: []byte(`{"external":{"command":["vault","kv","get","` + secretArg + `"]}}`)},
	} {
		line := Echo(tc)
		for _, w := range line.Command {
			if strings.Contains(w, secretArg) {
				t.Errorf("%s %s echoes the command's argument: %v", tc.Method, tc.Route, line.Command)
			}
		}
		if !line.Placeholder {
			t.Errorf("%s %s prints a line that looks runnable as printed, but it stands in for a command it cannot show", tc.Method, tc.Route)
		}
	}
}

func TestTheBundleExportEchoWritesToAFileAndPrintsNothingToSee_993(t *testing.T) {
	line := Echo(Action{Method: "POST", Route: "/encryption/keys/{id}/export", Params: map[string]string{"id": "k01"}})
	got := line.Shell()
	if !strings.HasPrefix(got, Binary+" encryption key export k01 --out <") {
		t.Errorf("prints %q; the export names a file to write to and has no way to print the bundle", got)
	}
	if !line.Placeholder {
		t.Error("the export's echo needs a path the operator supplies, so it is not runnable as printed")
	}
}

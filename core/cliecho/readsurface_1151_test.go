package cliecho

import (
	"strings"
	"testing"
)

// Issue #1151 (EPIC S, S2.1). Each verb or flag the issue adds is a
// command this package has to be able to print for the request that made
// the same change in the Web UI, or the line it prints is a claim about a
// terminal that no longer holds.

func TestTheReadOfOneBackupSetNamesShow_1151(t *testing.T) {
	line := Echo(Action{Method: "GET", Route: "/backup-sets/*",
		Params: map[string]string{"source": "api-server", "set": "var-backups"}})
	if line.Gap != "" {
		t.Fatalf("GET one backup set still says there is no equivalent: %s", line.Gap)
	}
	if got, want := line.Shell(), Binary+" backup-set show api-server/var-backups"; got != want {
		t.Errorf("prints\n  %s\nwant\n  %s", got, want)
	}
}

func TestAPollIntervalIsEchoedOnBothScopes_1151(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action Action
		want   string
	}{
		{
			name: "a backup set's own cadence",
			action: Action{Method: "PATCH", Route: "/backup-sets/{source}/{set}",
				Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body:   []byte(`{"poll_interval_seconds":900}`)},
			want: Binary + " backup-set patch api-server/var-backups --poll-interval 15m",
		},
		{
			// Zero is the request to inherit, and an echo that skipped it
			// would print a patch that names nothing.
			name: "a backup set returning to the deployment's cadence",
			action: Action{Method: "PATCH", Route: "/backup-sets/{source}/{set}",
				Params: map[string]string{"source": "api-server", "set": "var-backups"},
				Body:   []byte(`{"poll_interval_seconds":0}`)},
			want: Binary + " backup-set patch api-server/var-backups --poll-interval 0s",
		},
		{
			name: "the deployment's cadence",
			action: Action{Method: "PATCH", Route: "/settings",
				Body: []byte(`{"service":{"poll_interval_seconds":3600}}`)},
			want: Binary + " settings patch --poll-interval 1h",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := Echo(tc.action)
			if got := line.Shell(); got != tc.want {
				t.Errorf("prints\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestACreateEchoesTheLineageKeyAndTheMountPrefix_1151(t *testing.T) {
	line := Echo(Action{Method: "POST", Route: "/backup-sets", Body: []byte(
		`{"source_name":"api-server","name":"var-backups","host":"10.0.0.14","user":"backups",` +
			`"remote_path":"/srv/var-backups","ssh_key_id":"key_1","known_hosts_line":"10.0.0.14 ssh-ed25519 AAAAC3Nz",` +
			`"engine":"kopia","repository_domain":"production-vault",` +
			`"uuid":"7d1c5a6e-3b8f-4c2a-9e41-0f6b2d8a5c13","source_mount_prefix":"/srv"}`)})
	got := line.Shell()
	for _, want := range []string{
		"--uuid 7d1c5a6e-3b8f-4c2a-9e41-0f6b2d8a5c13",
		"--source-mount-prefix /srv",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the create does not echo %s, so the printed command mints a new lineage:\n  %s", want, got)
		}
	}
}

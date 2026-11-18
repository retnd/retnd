package cliecho

import "testing"

// #1291: the two encrypt-existing routes each name their command, so the
// browser's echo shows the exact line that does the same from a terminal, and
// neither route is a gap. Written by reading the echo table; not run.

func TestTheEncryptExistingRoutesNameTheirCommands_1291(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action Action
		want   string
	}{
		{"running it for a set", Action{Method: "POST", Route: "/encryption/encrypt-existing",
			Body: []byte(`{"backup_set_id":"api-server/var-backups","destination_medium":"offsite"}`)},
			Binary + " encryption encrypt-existing api-server/var-backups --to offsite"},
		{"running it for one backup", Action{Method: "POST", Route: "/encryption/encrypt-existing",
			Body: []byte(`{"backup_set_id":"api-server/var-backups","artifact_id":"api-server/var-backups/a.dump","destination_medium":"offsite"}`)},
			Binary + " encryption encrypt-existing api-server/var-backups --to offsite --artifact api-server/var-backups/a.dump"},
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

func TestTheEncryptExistingDryRunEchoesTheDryRunFlag_1291(t *testing.T) {
	q := map[string][]string{"set": {"api-server/var-backups"}, "destination": {"offsite"}}
	line := Echo(Action{Method: "GET", Route: "/encryption/encrypt-existing", Query: q})
	if line.Gap != "" {
		t.Fatalf("the dry run route says there is no equivalent: %s", line.Gap)
	}
	want := Binary + " encryption encrypt-existing api-server/var-backups --to offsite --dry-run"
	if got := line.Shell(); got != want {
		t.Errorf("prints\n  %s\nwant\n  %s", got, want)
	}
}

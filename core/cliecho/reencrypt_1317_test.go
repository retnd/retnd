package cliecho

import "testing"

// #1317: the two re-encrypt routes each name their command, so the browser's
// echo shows the exact line that does the same from a terminal, and neither
// route is a gap. Written by reading the echo table; not run.

func TestTheReencryptRoutesNameTheirCommands_1317(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action Action
		want   string
	}{
		{"by suite", Action{Method: "POST", Route: "/encryption/reencrypt",
			Body: []byte(`{"suite_id":"age/v1","destination_medium":"offsite"}`)},
			Binary + " encryption reencrypt --suite age/v1 --to offsite"},
		{"by key", Action{Method: "POST", Route: "/encryption/reencrypt",
			Body: []byte(`{"key_id":"k0123456789abcdef","destination_medium":"offsite"}`)},
			Binary + " encryption reencrypt --key k0123456789abcdef --to offsite"},
		{"by deprecation", Action{Method: "POST", Route: "/encryption/reencrypt",
			Body: []byte(`{"deprecated":true,"destination_medium":"offsite"}`)},
			Binary + " encryption reencrypt --deprecated --to offsite"},
		{"the dry run by suite, with no destination", Action{Method: "GET", Route: "/encryption/reencrypt",
			Query: map[string][]string{"suite": {"age/v1"}}},
			Binary + " encryption reencrypt --suite age/v1 --dry-run"},
		{"the dry run by deprecation onto a medium", Action{Method: "GET", Route: "/encryption/reencrypt",
			Query: map[string][]string{"deprecated": {"true"}, "destination": {"offsite"}}},
			Binary + " encryption reencrypt --deprecated --to offsite --dry-run"},
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

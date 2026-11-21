package cliecho

import "testing"

// #1545: the browser's echo for both re-encrypt routes names --from-local when
// the request asked for it, and only then.

func TestTheReencryptRoutesNameFromLocal_1545(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action Action
		want   string
	}{
		{"the run", Action{Method: "POST", Route: "/encryption/reencrypt",
			Body: []byte(`{"key_id":"k0123456789abcdef","destination_medium":"offsite","from_local":true}`)},
			Binary + " encryption reencrypt --key k0123456789abcdef --from-local --to offsite"},
		{"the run with it off", Action{Method: "POST", Route: "/encryption/reencrypt",
			Body: []byte(`{"key_id":"k0123456789abcdef","destination_medium":"offsite","from_local":false}`)},
			Binary + " encryption reencrypt --key k0123456789abcdef --to offsite"},
		{"the dry run", Action{Method: "GET", Route: "/encryption/reencrypt",
			Query: map[string][]string{"key": {"k0123456789abcdef"}, "from_local": {"true"}, "destination": {"offsite"}}},
			Binary + " encryption reencrypt --key k0123456789abcdef --from-local --to offsite --dry-run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := Echo(tc.action)
			if line.Gap != "" {
				t.Fatalf("this route says there is no equivalent: %s", line.Gap)
			}
			if got := line.Shell(); got != tc.want {
				t.Errorf("prints\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

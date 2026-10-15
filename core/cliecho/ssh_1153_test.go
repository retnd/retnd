package cliecho

import "testing"

// Issue #1153: the key store routes name their verbs instead of printing a
// gap, and no key material or passphrase reaches the printed line.
func TestSSH1153_RoutesWithAVerbNameIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action Action
		want   string
	}{
		{"listing the store", Action{Method: "GET", Route: "/ssh-keys"}, Binary + " ssh-key list"},
		{"scanning for keys", Action{Method: "GET", Route: "/ssh/key-candidates"}, Binary + " ssh-key discover"},
		{"importing a candidate", Action{Method: "POST", Route: "/ssh-keys/from-candidate",
			Body: []byte(`{"candidate_id":"cand_1"}`)}, Binary + " ssh-key import --candidate cand_1"},
		{"pasting a key", Action{Method: "POST", Route: "/ssh-keys",
			Body: []byte(`{"private_key_pem":"canaryPEM","passphrase":""}`)},
			Binary + " ssh-key import --file <the private key file you chose>"},
		{"pasting a protected key", Action{Method: "POST", Route: "/ssh-keys",
			Body: []byte(`{"private_key_pem":"canaryPEM","passphrase":"canaryPW"}`)},
			Binary + " ssh-key import --file <the private key file you chose> --passphrase-stdin"},
		{"probing a host key", Action{Method: "POST", Route: "/ssh/host-key-probe",
			Body: []byte(`{"host":"source.example.internal","port":2222}`)},
			Binary + " ssh host-key-probe source.example.internal --port 2222"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := Echo(tc.action)
			if got := line.Shell(); got != tc.want {
				t.Errorf("prints\n  %s\nwant\n  %s", got, tc.want)
			}
			if len(line.Command) == 0 {
				t.Errorf("still a gap: %s", line.GapDetail)
			}
		})
	}
}

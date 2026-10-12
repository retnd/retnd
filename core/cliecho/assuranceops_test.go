package cliecho

import "testing"

// EPIC Q, #980 and #1071: the three assurance routes name the verbs that do
// the same thing, and not a gap.
func TestTheAssuranceOperationRoutesNameTheirVerbs(t *testing.T) {
	for _, tc := range []struct {
		action Action
		want   string
	}{
		{Action{Method: "POST", Route: "/backup-sets/{source}/{set}/scrub", Params: map[string]string{"source": "api-server", "set": "var-backups"}},
			Binary + " scrub api-server/var-backups"},
		{Action{Method: "POST", Route: "/assurance/operations/{op}/cancel", Params: map[string]string{"op": "7b0c9a1e-0000-4000-8000-000000000001"}},
			Binary + " assurance cancel 7b0c9a1e-0000-4000-8000-000000000001"},
		{Action{Method: "GET", Route: "/backup-sets/{source}/{set}/recovery-test/output", Params: map[string]string{"source": "api-server", "set": "var-backups"}},
			Binary + " recovery-test api-server/var-backups --show-output"},
	} {
		line := Echo(tc.action)
		if line.Gap != "" || line.Shell() != tc.want {
			t.Errorf("%s %s prints %q (gap %q), want %q", tc.action.Method, tc.action.Route, line.Shell(), line.Gap, tc.want)
		}
	}
}

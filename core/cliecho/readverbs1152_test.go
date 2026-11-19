package cliecho

import "testing"

// #1152: the routes that gained a verb name it, and are not a gap any more.
func TestTheReadRoutesThatGainedAVerbNameIt(t *testing.T) {
	for _, tc := range []struct {
		action Action
		want   string
	}{
		{Action{Method: "GET", Route: "/system/version"}, Binary + " version --engine"},
		{Action{Method: "GET", Route: "/system/capabilities"}, Binary + " system capabilities"},
		{Action{Method: "GET", Route: "/system/first-run"}, Binary + " system first-run"},
		{Action{Method: "GET", Route: "/validators"}, Binary + " validators"},
		{Action{Method: "GET", Route: "/activity", Query: mustQuery("limit=50&before=120")}, Binary + " activity --limit 50 --before 120"},
		// A cursor the binary would refuse is not echoed: the route reads it
		// as no cursor, and a printed --before banana exits 2.
		{Action{Method: "GET", Route: "/activity", Query: mustQuery("before=banana")}, Binary + " activity"},
		{Action{Method: "GET", Route: "/activity", Query: mustQuery("before=0")}, Binary + " activity"},
	} {
		line := Echo(tc.action)
		if line.Gap != "" || line.Shell() != tc.want {
			t.Errorf("%s %s?%s prints %q (gap %q), want %q", tc.action.Method, tc.action.Route, tc.action.Query.Encode(), line.Shell(), line.Gap, tc.want)
		}
	}
}

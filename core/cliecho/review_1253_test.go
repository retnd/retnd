package cliecho

import (
	"slices"
	"strings"
	"testing"
)

// Echo bugs the review of #1253 found on main, each one a line that does
// less than the click it stands for. None of them was visible to the
// field check as it first landed, because it only looked at string
// fields, and these are all booleans.

// PATCH /settings/workflow {"disabled":true} turns workflows off. The line
// it echoed was a bare `settings workflow patch`, which changes nothing,
// and {"disabled":false} printed the identical line. The CLI has had
// --disable and --enable since #1246.
func TestReview1253_WorkflowSettingsEchoDisableAndEnable(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"disabled":true}`, Binary + " settings workflow patch --disable"},
		{`{"disabled":false}`, Binary + " settings workflow patch --enable"},
		{`{"disabled":false,"root":"/workflows"}`, Binary + " settings workflow patch --root /workflows --enable"},
	} {
		line := Echo(Action{Method: "PATCH", Route: "/settings/workflow", Body: []byte(tc.body)})
		if got := line.Shell(); got != tc.want {
			t.Errorf("%s echoes\n  %s\nwant\n  %s", tc.body, got, tc.want)
		}
	}

	// The control: a patch that doesn't mention disabled says nothing
	// about it.
	line := Echo(Action{Method: "PATCH", Route: "/settings/workflow", Body: []byte(`{"root":"/workflows"}`)})
	if strings.Contains(line.Shell(), "able") {
		t.Errorf("a patch that leaves workflows on or off as they are echoes a switch: %s", line.Shell())
	}
}

// skip_connection_check is the request to write a backup set without
// proving the connection first, and `backup-set create|patch --no-verify`
// is the CLI's spelling of it. The medium builders echo the same field on
// purpose (mediumSkipFlag); the backup-set ones never did, so the pasted
// line did a check the click had skipped, and on a host this machine
// can't reach the line failed where the click had worked.
func TestReview1253_SkipConnectionCheckEchoesNoVerify(t *testing.T) {
	set := map[string]string{"source": "api-server", "set": "var-backups"}
	for _, tc := range []struct {
		name   string
		action Action
	}{
		{"create", Action{Method: "POST", Route: "/backup-sets",
			Body: []byte(`{"source_name":"api-server","name":"var-backups","host":"10.0.0.14","skip_connection_check":true}`)}},
		{"patch", Action{Method: "PATCH", Route: "/backup-sets/{source}/{set}", Params: set,
			Body: []byte(`{"host":"10.0.0.15","skip_connection_check":true}`)}},
		{"first run", Action{Method: "POST", Route: "/system/first-run",
			Body: []byte(`{"source_name":"api-server","name":"var-backups","host":"10.0.0.14","skip_connection_check":true}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := Echo(tc.action)
			if !slices.Contains(line.Command, "--no-verify") {
				t.Errorf("skip_connection_check:true echoes no --no-verify:\n  %s", line.Shell())
			}
			// And without the field the check is not skipped.
			tc.action.Body = []byte(strings.Replace(string(tc.action.Body), `,"skip_connection_check":true`, "", 1))
			if line := Echo(tc.action); slices.Contains(line.Command, "--no-verify") {
				t.Errorf("a request that did not skip the check echoes --no-verify:\n  %s", line.Shell())
			}
		})
	}
}

// A recovery with no --to goes back where the backup came from: the
// production source host (core/cmd/retnd/recover.go). Both recover
// builders printed exactly that line for a body they couldn't read and
// for a target kind they didn't know, so the twin of a refused "archive"
// recovery was one acknowledgement away from writing onto production. It
// only failed closed because the CLI then asks for
// --acknowledge-source-write, and its refusal names that flag.
//
// A line with no explicit --to is never right here, so these refuse
// instead.
func TestReview1253_RecoverNeverEchoesALineWithoutTo(t *testing.T) {
	one := map[string]string{"source": "api-server", "set": "var-backups", "name": "dump.tar"}
	for _, tc := range []struct {
		name   string
		action Action
	}{
		{"one backup, a body that won't decode", Action{Method: "POST", Route: "/backups/{source}/{set}/{name}/recovery", Params: one,
			Body: []byte(`not json`)}},
		{"one backup, an unknown kind", Action{Method: "POST", Route: "/backups/{source}/{set}/{name}/recovery", Params: one,
			Body: []byte(`{"acknowledged":true,"target":{"kind":"archive"}}`)}},
		{"one backup, a kind in the wrong case", Action{Method: "POST", Route: "/backups/{source}/{set}/{name}/recovery", Params: one,
			Body: []byte(`{"target":{"kind":"Source","path":"retnd-recovered-1"}}`)}},
		{"one backup, no target at all", Action{Method: "POST", Route: "/backups/{source}/{set}/{name}/recovery", Params: one,
			Body: []byte(`{}`)}},
		{"several, a body that won't decode", Action{Method: "POST", Route: "/recoveries",
			Body: []byte(`not json`)}},
		{"several, an unknown kind", Action{Method: "POST", Route: "/recoveries",
			Body: []byte(`{"set":"api-server/var-backups","target":{"kind":"bogus"}}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := Echo(tc.action)
			if len(line.Command) > 0 {
				t.Fatalf("echoes a recovery with no --to, which the CLI reads as the production source host:\n  %s", line.Shell())
			}
			if strings.TrimSpace(line.GapDetail) == "" {
				t.Errorf("refuses without saying why")
			}
		})
	}

	// The control: every kind the contract has still prints its --to.
	for _, kind := range []string{"download", "staging", "storage", "source"} {
		line := Echo(Action{Method: "POST", Route: "/backups/{source}/{set}/{name}/recovery", Params: one,
			Body: []byte(`{"target":{"kind":"` + kind + `","path":"p","medium":"m"}}`)})
		if !slices.Contains(line.Command, "--to") {
			t.Errorf("kind %s no longer prints --to: %s %s", kind, line.Shell(), line.GapDetail)
		}
	}
}

package cliecho

import "testing"

// Issue #1251, the review of #1256: POST /recoveries takes until beside
// since, and the echo carries it as --until. A range onto the source host is
// a narrowing like since, so --all is not added to it.
func TestEcho1251_RecoveriesCarryUntil(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"set":"api-server/var-backups","since":"2026-10-01T00:00:00Z","until":"2026-10-07T23:59:59Z","target":{"kind":"staging","path":"restore/var-backups"}}`,
			"retnd recover --set api-server/var-backups --since 2026-10-01T00:00:00Z --until 2026-10-07T23:59:59Z --to restore/var-backups"},
		{`{"set":"api-server/var-backups","newest_only":true,"until":"2026-10-07T23:59:59Z","target":{"kind":"staging","path":"o"}}`,
			"retnd recover --set api-server/var-backups --newest-only --until 2026-10-07T23:59:59Z --to o"},
		{`{"set":"api-server/var-backups","until":"2026-10-07T23:59:59Z","target":{"kind":"source","path":"retnd-recovered-x","acknowledged_destination":"sh1-1"}}`,
			"retnd recover --set api-server/var-backups --until 2026-10-07T23:59:59Z --to source:retnd-recovered-x --acknowledge-source-write"},
	} {
		got := Echo(Action{Method: "POST", Route: "/recoveries", Body: []byte(tc.body)}).Shell()
		if got != tc.want {
			t.Errorf("%s\n  got  %s\n  want %s", tc.body, got, tc.want)
		}
	}
}

// The review of #1256: the echo prints --all when, and only when, the request
// said "all", so the line and the request agree. A whole set onto the source
// host with no all is refused by the service, and its echo is the command the
// terminal refuses the same way.
func TestEcho1251_AllIsWhatTheRequestSaid(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"set":"api-server/var-backups","all":true,"target":{"kind":"source","path":"retnd-recovered-x","acknowledged_destination":"sh1-1"}}`,
			"retnd recover --set api-server/var-backups --to source:retnd-recovered-x --acknowledge-source-write --all"},
		{`{"set":"api-server/var-backups","target":{"kind":"source","path":"retnd-recovered-x","acknowledged_destination":"sh1-1"}}`,
			"retnd recover --set api-server/var-backups --to source:retnd-recovered-x --acknowledge-source-write"},
		{`{"set":"api-server/var-backups","all":true,"target":{"kind":"staging","path":"o"}}`,
			"retnd recover --set api-server/var-backups --to o --all"},
	} {
		got := Echo(Action{Method: "POST", Route: "/recoveries", Body: []byte(tc.body)}).Shell()
		if got != tc.want {
			t.Errorf("%s\n  got  %s\n  want %s", tc.body, got, tc.want)
		}
	}
}


package cliecho

import (
	"strings"
	"testing"
)

// Issue #1155: the /auth routes that now have a terminal verb name it, and
// the ones that deliberately do not still say why. A secret never reaches
// the printed line: the stdin flag stands in for it.

func TestAuth1155_RoutesWithAVerbNameIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action Action
		want   string
	}{
		{"rotating the password", Action{Method: "POST", Route: "/auth/password",
			Body: []byte(`{"currentPassword":"canaryA","newPassword":"canaryB"}`)},
			Binary + " auth password --password-stdin"},
		{"reading the recovery settings", Action{Method: "GET", Route: "/auth/recovery"},
			Binary + " auth recovery show"},
		{"sending the recovery test message", Action{Method: "POST", Route: "/auth/recovery/test"},
			Binary + " auth recovery test"},
		{"redeeming a verification token", Action{Method: "POST", Route: "/auth/verify-email",
			Body: []byte(`{"token":"canaryT"}`)},
			Binary + " auth verify-email --token-stdin"},
		{"asking for another verification message", Action{Method: "POST", Route: "/auth/verify-email/resend"},
			Binary + " auth verify-email --resend"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := Echo(tc.action)
			if got := line.Shell(); got != tc.want {
				t.Errorf("prints\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestAuth1155_AnUpdateEchoesWhatItChangesAndNoSecret(t *testing.T) {
	line := Echo(Action{Method: "PATCH", Route: "/auth/recovery",
		Body: []byte(`{"currentPassword":"canaryPW","recoveryEmail":"ops@example.org","smtp":{"host":"smtp.example.org","port":2525,"security":"tls","username":"mailer","from":"r@example.org","password":"canarySMTP"}}`)})
	got := line.Shell()
	if !strings.HasPrefix(got, Binary+" auth recovery set --current-password-stdin") {
		t.Errorf("prints %q, want it to start with the set verb and the stdin flag", got)
	}
	for _, flag := range []string{"--recovery-email", "--smtp-host", "--smtp-password-stdin"} {
		if !strings.Contains(got, flag) {
			t.Errorf("%q does not carry %s", got, flag)
		}
	}
	for _, secret := range []string{"canaryPW", "canarySMTP"} {
		if strings.Contains(got, secret) {
			t.Errorf("a secret reached the printed command: %q", got)
		}
	}

	// The control: an address-only update has no smtp flags and no SMTP stdin line.
	plain := Echo(Action{Method: "PATCH", Route: "/auth/recovery",
		Body: []byte(`{"currentPassword":"canaryPW","recoveryEmail":"ops@example.org"}`)}).Shell()
	if strings.Contains(plain, "--smtp") {
		t.Errorf("an address-only update echoes an smtp flag: %q", plain)
	}
}

// Login and logout are the two left (#1253 gave enroll, the session read
// and the reset flow verbs of their own; auth_1253_test.go has them).
func TestAuth1155_TheRoutesWithoutAVerbStillSayWhy(t *testing.T) {
	for _, route := range []string{"POST /auth/login", "POST /auth/logout"} {
		method, path, _ := strings.Cut(route, " ")
		line := Echo(Action{Method: method, Route: path})
		if len(line.Command) != 0 || strings.TrimSpace(line.GapDetail) == "" {
			t.Errorf("%s: command %v, reason %q; want no command and a written reason", route, line.Command, line.GapDetail)
		}
	}
}

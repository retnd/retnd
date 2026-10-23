package cliecho

import (
	"strings"
	"testing"
)

// Issue #1253, the auth slice: four /auth routes that said "no verb" now
// print one. The enrollment page, the two reset pages and the session
// read a browser makes on load each have a terminal twin, and no secret
// (the bootstrap token, the reset token, any password) reaches the line:
// the stdin flag stands in for it.

func TestAuth1253_TheFourRoutesPrintTheirVerb(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action Action
		want   string
	}{
		{"enrolling the first administrator", Action{Method: "POST", Route: "/auth/enroll",
			Body: []byte(`{"username":"admin","password":"canaryP"}`)},
			Binary + " auth enroll --username admin --token-stdin --password-stdin"},
		{"asking for a reset link", Action{Method: "POST", Route: "/auth/forgot-password",
			Body: []byte(`{"username":"admin"}`)},
			Binary + " auth forgot-password --username admin"},
		{"redeeming a reset token", Action{Method: "POST", Route: "/auth/reset-password",
			Body: []byte(`{"token":"canaryT","newPassword":"canaryP"}`)},
			Binary + " auth reset-password --token-stdin --password-stdin"},
		{"reading the session", Action{Method: "GET", Route: "/auth/session"},
			Binary + " auth whoami"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := Echo(tc.action)
			if got := line.Shell(); got != tc.want {
				t.Errorf("prints\n  %s\nwant\n  %s\n(gap: %q)", got, tc.want, line.GapDetail)
			}
			for _, secret := range []string{"canaryP", "canaryT"} {
				if strings.Contains(line.Shell(), secret) {
					t.Errorf("a secret reached the printed command: %q", line.Shell())
				}
			}
		})
	}
}

func TestAuth1253_AnEnrollmentWithRecoveryEchoesTheSectionAndNoSecret(t *testing.T) {
	line := Echo(Action{Method: "POST", Route: "/auth/enroll",
		Body: []byte(`{"username":"admin","password":"canaryP","recoveryEmail":"ops@example.org","smtp":{"host":"smtp.example.org","port":2525,"security":"tls","username":"mailer","from":"r@example.org","password":"canarySMTP"}}`)})
	got := line.Shell()
	if !strings.HasPrefix(got, Binary+" auth enroll --username admin --token-stdin --password-stdin") {
		t.Errorf("prints %q, want it to start with the enroll verb and both stdin flags", got)
	}
	for _, flag := range []string{"--recovery-email", "--smtp-host", "--smtp-port 2525", "--smtp-security", "--smtp-username", "--smtp-from", "--smtp-password-stdin"} {
		if !strings.Contains(got, flag) {
			t.Errorf("%q does not carry %s", got, flag)
		}
	}
	for _, secret := range []string{"canaryP", "canarySMTP"} {
		if strings.Contains(got, secret) {
			t.Errorf("a secret reached the printed command: %q", got)
		}
	}
	// The control: credentials alone carry no recovery flag.
	plain := Echo(Action{Method: "POST", Route: "/auth/enroll", Body: []byte(`{"username":"admin","password":"canaryP"}`)}).Shell()
	if strings.Contains(plain, "--recovery-email") || strings.Contains(plain, "--smtp") {
		t.Errorf("an enrollment without recovery echoes a recovery flag: %q", plain)
	}
}

// A body that will not decode has nothing to put on the line, and the
// reason says so rather than printing a half command.
func TestAuth1253_AnUndecodableBodyIsAGapWithAReason(t *testing.T) {
	for _, route := range []string{"/auth/enroll", "/auth/forgot-password"} {
		line := Echo(Action{Method: "POST", Route: route, Body: []byte(`{not json`)})
		if len(line.Command) != 0 || strings.TrimSpace(line.GapDetail) == "" {
			t.Errorf("POST %s with a bad body: command %v, reason %q; want no command and a reason", route, line.Command, line.GapDetail)
		}
	}
}

// Login and logout are the two that stay deliberate gaps, and their
// reasons still stand.
func TestAuth1253_LoginAndLogoutStillSayWhy(t *testing.T) {
	for _, route := range []string{"POST /auth/login", "POST /auth/logout"} {
		method, path, _ := strings.Cut(route, " ")
		line := Echo(Action{Method: method, Route: path})
		if len(line.Command) != 0 || strings.TrimSpace(line.GapDetail) == "" {
			t.Errorf("%s: command %v, reason %q; want no command and a written reason", route, line.Command, line.GapDetail)
		}
	}
}

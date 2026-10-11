package local

import (
	"net/http"
	"testing"
)

// retnd#1049: the recovery section of enrolment is optional, so a fresh
// install is never stopped for want of a mail server.

func enrollWith(t *testing.T, body any) (*Service, *http.Response) {
	t.Helper()
	svc, server, client := testServer(t)
	seedCSRFCookie(t, client, server)
	csrf := csrfTokenFromJar(t, client, server)
	token := currentBootstrapToken(t, svc)
	resp := postJSON(t, client, server.URL+"/api/v1/auth/enroll", body,
		map[string]string{CSRFHeaderName: csrf, BootstrapTokenHeader: token})
	t.Cleanup(func() { resp.Body.Close() })
	return svc, resp
}

func TestEnroll_CredentialsAloneCreateTheAdministratorWithoutAMailServer(t *testing.T) {
	svc, resp := enrollWith(t, map[string]string{"username": "bm-admin", "password": testAdminPassword})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("enroll with credentials alone = %d, want 204", resp.StatusCode)
	}
	if !svc.AdministratorCreated() {
		t.Errorf("no administrator after a successful enrolment")
	}
	if svc.EmailConfigured() {
		t.Errorf("a mail server is reported configured though none was supplied")
	}
	admin, err := svc.store.Admin()
	if err != nil || admin == nil {
		t.Fatalf("Admin() = %v, %v", admin, err)
	}
	// Nothing was mailed, so nothing can lapse: the record must not carry
	// a verification deadline the reaper would delete it at.
	if admin.VerificationDeadline != nil || admin.VerificationTokenHash != "" {
		t.Errorf("an enrolment with no recovery carries a verification challenge: %+v", admin)
	}
}

func TestEnroll_HalfARecoverySectionIsStillRefused(t *testing.T) {
	_, resp := enrollWith(t, map[string]string{
		"username": "bm-admin", "password": testAdminPassword, "recoveryEmail": testRecoveryEmail,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("enroll with an address and no mail server = %d, want 400", resp.StatusCode)
	}
}

func TestSetupSkips_SurviveAStoreReopen(t *testing.T) {
	svc, resp := enrollWith(t, map[string]string{"username": "bm-admin", "password": testAdminPassword})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("enroll = %d", resp.StatusCode)
	}
	if err := svc.SkipSetupStep("email"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SkipSetupStep("email"); err != nil {
		t.Fatalf("skipping twice must be idempotent: %v", err)
	}
	reopened := NewStore(svc.store.path)
	skips, err := reopened.SetupSkips()
	if err != nil || len(skips) != 1 || skips[0] != "email" {
		t.Errorf("skips after reopening = %v, %v, want [email]", skips, err)
	}
	if admin, _ := reopened.Admin(); admin == nil {
		t.Errorf("recording a skip lost the administrator")
	}
}

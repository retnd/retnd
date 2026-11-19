package local

import (
	"context"
	"net/http"
	"time"

	"github.com/retnd/retnd/apps/common/platform/capabilities"
)

// The read half of a session: given a request, is somebody signed in, and
// who.
//
// It is separate from session.go because the two answer to different
// callers with different shapes. session.go serves this package's own HTTP
// handlers, which hold a *http.Request; this file serves
// apps/common/webhost's authMiddleware through capabilities.Authenticator,
// which was deliberately given only headers and a remote address so that
// the contract does not drag net/http's whole request type across the
// provider seam. The cost of that narrowing is paid here, once, in
// sessionTokenFromCookieHeader.
//
// Nothing in this file can create or extend a session, only recognise one.
// That is what lets webhost consult it on every single /api/v1 request
// without any risk of a read silently refreshing a credential.

// sessionAuthenticator adapts sessionManager's cookie-based session
// lookup to capabilities.Authenticator, the seam apps/common/webhost's
// authMiddleware consults for every /api/v1 request (see that package's
// auth.go). It never checks a username/password itself - only whether
// the caller's session cookie currently names a live session; Login and
// Enroll (handler.go) are what establish one in the first place.
type sessionAuthenticator struct {
	sessions *sessionManager
}

// Authenticate implements capabilities.Authenticator.
func (a sessionAuthenticator) Authenticate(_ context.Context, r capabilities.AuthRequest) (capabilities.AuthContext, error) {
	token := sessionTokenFromCookieHeader(r.Headers.Get("Cookie"))
	rec, ok := a.sessions.live(token)
	if !ok {
		return capabilities.AuthContext{}, nil
	}
	return capabilities.AuthContext{
		Authenticated: true,
		Username:      rec.username,
		Mode:          capabilities.AuthModeLocalAccount,
		// The sessions here have a fixed lifetime, so this is the same
		// value on every request of one session. Anything bound to the
		// session (a recovery grant) compares it exactly, hence the
		// nanoseconds.
		SessionExpiresAt: rec.expiresAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

// sessionTokenFromCookieHeader parses this package's session token out of
// a raw Cookie header string. capabilities.AuthRequest carries only
// http.Header (not a *http.Request), so this can't call
// (*http.Request).Cookie directly; building a throwaway *http.Request
// around just that one header lets this reuse net/http's own
// cookie-parsing logic (quoting, multiple cookies, etc.) instead of
// re-implementing it - and lets the actual name selection stay in
// tokenFromRequest (session.go), so this read path and the handlers'
// accept the same names, including the legacy one, without two lists to
// keep in step.
func sessionTokenFromCookieHeader(cookieHeader string) string {
	header := http.Header{}
	if cookieHeader != "" {
		header.Set("Cookie", cookieHeader)
	}
	return tokenFromRequest(&http.Request{Header: header})
}

package local

import (
	"strings"
	"testing"
	"time"
)

// The mail this package sends names the product as retnd, with the bare
// token fallback (no base URL) spelling out the page to paste it into. The
// former name is built from pieces so this file does not spell it.
func TestEveryMessageNamesTheProductAsRetnd(t *testing.T) {
	t.Parallel()

	former := strings.ToLower(strings.Join([]string{"BACKUP", "D"}, ""))
	deadline := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)

	for name, body := range map[string]string{
		"reset, with a link":        resetMessage("a@b.test", "admin", "tok", "https://x.test").Body,
		"reset, bare token":         resetMessage("a@b.test", "admin", "tok", "").Body,
		"test message":              testMessage("a@b.test").Body,
		"verify, bare, deadline":    verifyMessage("a@b.test", "admin", "tok", "", &deadline).Body,
		"verify, link, no deadline": verifyMessage("a@b.test", "admin", "tok", "https://x.test", nil).Body,
	} {
		if strings.Contains(strings.ToLower(body), former) {
			t.Errorf("%s: the body names the former product:\n%s", name, body)
		}
		if name != "reset, with a link" && !strings.Contains(body, "retnd") {
			t.Errorf("%s: the body never says retnd:\n%s", name, body)
		}
	}
}

package cliecho

import (
	"strings"
	"testing"
)

// #1111: the browser's PATCH of builtin_check prints the flag a terminal user
// would type, on both the deployment-wide and the per-set route.
func TestIssue1111_TheBuiltinCheckPatchesNameTheirFlag(t *testing.T) {
	for _, tc := range []struct {
		action Action
		want   string
	}{
		{Action{Method: "PATCH", Route: "/settings/assurance", Body: []byte(`{"recovery_test_builtin_check":"off"}`)},
			Binary + " settings assurance patch --recovery-test-builtin-check off"},
		{Action{Method: "PATCH", Route: "/backup-sets/{source}/{set}/assurance", Params: map[string]string{"source": "api-server", "set": "var-backups"},
			Body: []byte(`{"recovery_test_builtin_check":"structure"}`)},
			Binary + " backup-set assurance api-server/var-backups --recovery-test-builtin-check structure"},
		{Action{Method: "PATCH", Route: "/settings/assurance", Body: []byte(`{"recovery_test_mode":"builtin"}`)},
			Binary + " settings assurance patch --recovery-test-mode builtin"},
	} {
		line := Echo(tc.action)
		if line.Gap != "" || !strings.Contains(line.Shell(), strings.TrimPrefix(tc.want, Binary+" ")) {
			t.Errorf("%s %s prints %q (gap %q), want it to contain %q", tc.action.Method, tc.action.Route, line.Shell(), line.Gap, tc.want)
		}
	}
}

package spk

import (
	"strings"
	"testing"
)

// Issue #1163: apps/common/webhost/serve's topology test builds this
// package's deployment as "the engine on loopback, forwarded headers not
// trusted, serve-ui in front proxying to it", and checks the insecure-transport
// rule against that shape. This pins the scripts to that shape, so the row
// there cannot quietly stop describing what DSM actually starts.
func TestTopology1163_TheEngineListensOnLoopbackBehindServeUIWithoutTrustedForwarding(t *testing.T) {
	scripts, err := LifecycleScripts()
	if err != nil {
		t.Fatalf("LifecycleScripts: %v", err)
	}
	body := scripts["start-stop-status"]
	shared, err := SharedScript()
	if err != nil {
		t.Fatalf("SharedScript: %v", err)
	}
	if !strings.Contains(shared, `ENGINE_ADDR="127.0.0.1:`) {
		t.Error("common.sh no longer binds the engine to loopback; the serve topology row for the .spk describes a loopback engine")
	}
	if !strings.Contains(body, `--listen "${ENGINE_ADDR}"`) {
		t.Error("start-stop-status does not start the engine on ENGINE_ADDR")
	}
	if !strings.Contains(body, `--upstream "http://${ENGINE_ADDR}"`) {
		t.Error("start-stop-status does not put serve-ui in front of the engine")
	}
	for _, line := range strings.Split(body, "\n") {
		if code := strings.TrimSpace(line); !strings.HasPrefix(code, "#") && strings.Contains(code, "--trust-forwarded-headers") {
			t.Error("start-stop-status now trusts forwarded headers; update the .spk row in apps/common/webhost/serve/topology_1163_test.go")
		}
	}
}

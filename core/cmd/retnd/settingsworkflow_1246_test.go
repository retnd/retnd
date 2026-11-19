package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/service"
)

// #1246 review: a root an operator cleared has a way back to the default, and
// `retnd check` reports the root the engine will use.

func TestSettingsWorkflowPatchDisablesAndEnables(t *testing.T) {
	configPath := writeTestConfig(t)
	root := filepath.Join(filepath.Dir(configPath), "workflows")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append(raw, []byte("workflows:\n  root: "+root+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	var code int
	off := captureStdout(t, func() {
		code = run([]string{"settings", "workflow", "patch", "--disable", "--config", configPath})
	})
	if code != 0 {
		t.Fatalf("--disable = %d:\n%s", code, off)
	}
	if !strings.Contains(off, "workflows.disabled") {
		t.Errorf("--disable does not say workflows are turned off:\n%s", off)
	}
	if !strings.Contains(off, "older") {
		t.Errorf("--disable writes a key an older release refuses and does not say so:\n%s", off)
	}
	if b, _ := os.ReadFile(configPath); !strings.Contains(string(b), "disabled: true") || strings.Contains(string(b), "root: ") {
		t.Errorf("config.yaml after --disable:\n%s", b)
	}

	on := captureStdout(t, func() {
		code = run([]string{"settings", "workflow", "patch", "--enable", "--config", configPath})
	})
	if code != 0 {
		t.Fatalf("--enable = %d:\n%s", code, on)
	}
	if strings.Contains(on, "turned off") {
		t.Errorf("--enable left workflows turned off:\n%s", on)
	}
	if b, _ := os.ReadFile(configPath); strings.Contains(string(b), "workflows:") {
		t.Errorf("config.yaml still says disabled after --enable:\n%s", b)
	}
}

func TestSettingsWorkflowPatchRefusesDisableWithARoot(t *testing.T) {
	configPath := writeTestConfig(t)
	var code int
	_ = captureStdout(t, func() {
		code = run([]string{"settings", "workflow", "patch", "--disable", "--root", "/srv/hooks", "--config", configPath})
	})
	if code == 0 {
		t.Error("--disable with --root was accepted")
	}
}

func TestCheckReportsTheWorkflowRootItWouldUse(t *testing.T) {
	mounted := service.WorkflowRootDefault{Dir: "/workflows", Present: func(string) bool { return true }}
	for _, c := range []struct {
		name string
		cfg  config.Config
		def  service.WorkflowRootDefault
		want string
		not  string
	}{
		{"default", config.Config{}, mounted, "workflow root: /workflows (the default", ""},
		{"configured", config.Config{Workflows: config.Workflows{Root: "/srv/hooks"}}, mounted, "workflow root: /srv/hooks\n", "default"},
		{"turned off", config.Config{Workflows: config.Workflows{Disabled: true}}, mounted, "turned off", ""},
		// Silent, so a deployment with no workflows at all reads exactly as
		// it did before (FR-35's compat capture).
		{"none", config.Config{}, service.WorkflowRootDefault{Dir: "/workflows", Present: func(string) bool { return false }}, "", "workflow root"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			printCheckWorkflowRoot(&buf, &c.cfg, c.def)
			if !strings.Contains(buf.String(), c.want) {
				t.Errorf("got %q, want it to contain %q", buf.String(), c.want)
			}
			if c.not != "" && strings.Contains(buf.String(), c.not) {
				t.Errorf("got %q, which says %q", buf.String(), c.not)
			}
		})
	}
}

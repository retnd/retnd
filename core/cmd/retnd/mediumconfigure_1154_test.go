package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/apicontract"
)

// #1154 (EPIC S, audit rows 66 to 68): `medium configure`, `medium
// configure --preflight` and `medium backends`.
//
// Until now a local_volume destination could not be configured from a
// terminal at all, because `medium add` and `medium edit` carry one flag per
// S3 field and none of them is a path. These drive run() end to end over a
// real configuration file and a real directory, because what the issue adds
// is operator-visible: a flag an operator types, a file that changes and an
// exit code a script branches on.

func configBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return raw
}

func TestIssue1154_ConfigureWritesALocalVolumeInItsBackendsOwnFieldNames(t *testing.T) {
	cfg := writeTestConfig(t)
	dir := t.TempDir()

	var code int
	out := captureStdout(t, func() {
		code = run([]string{"medium", "configure", "usb_disk", "--backend", "local_volume",
			"--field", "path=" + dir, "--field", "prefix=retnd", "--config", cfg})
	})
	if code != 0 {
		t.Fatalf("medium configure = %d, want 0; it printed:\n%s", code, out)
	}
	if !strings.Contains(out, "usb_disk") {
		t.Errorf("the output does not name the destination it configured:\n%s", out)
	}
	written := string(configBytes(t, cfg))
	for _, want := range []string{"id: usb_disk", "type: local_volume", dir, "prefix: retnd"} {
		if !strings.Contains(written, want) {
			t.Errorf("the configuration does not carry %q after configure:\n%s", want, written)
		}
	}

	// And it is readable back through the same CLI, which is the half of
	// "configurable at all" an operator checks next.
	shown := captureStdout(t, func() {
		if got := run([]string{"medium", "show", "usb_disk", "--config", cfg}); got != 0 {
			t.Errorf("medium show usb_disk = %d, want 0", got)
		}
	})
	if !strings.Contains(shown, dir) {
		t.Errorf("medium show does not report the directory configure wrote:\n%s", shown)
	}
}

func TestIssue1154_ConfigureReplacesTheWholeFieldSetAndRefusesAnOmittedRequiredField(t *testing.T) {
	cfg := writeTestConfig(t)
	dir := t.TempDir()
	if got := run([]string{"medium", "configure", "usb_disk", "--backend", "local_volume", "--field", "path=" + dir, "--config", cfg}); got != 0 {
		t.Fatalf("the first configure = %d, want 0", got)
	}
	before := configBytes(t, cfg)

	// A replace that leaves out `path` is a replace that unsets it, and the
	// manifest says path is required: refused, and nothing written.
	var code int
	captureStdout(t, func() {
		code = run([]string{"medium", "configure", "usb_disk", "--backend", "local_volume", "--field", "prefix=elsewhere", "--config", cfg})
	})
	if code == 0 {
		t.Error("a configure that omits the required `path` exited 0")
	}
	if after := configBytes(t, cfg); !bytes.Equal(before, after) {
		t.Errorf("a refused configure changed the configuration file:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestIssue1154_ConfigureNeedsABackendWhenTheDestinationIsNotDeclaredYet(t *testing.T) {
	cfg := writeTestConfig(t)
	before := configBytes(t, cfg)
	var code int
	stderr := captureStderr(t, func() {
		captureStdout(t, func() {
			code = run([]string{"medium", "configure", "usb_disk", "--field", "path=" + t.TempDir(), "--config", cfg})
		})
	})
	if code == 0 {
		t.Error("configuring an undeclared destination with no --backend exited 0")
	}
	if !strings.Contains(stderr, "name the backend it is an instance of") {
		t.Errorf("the refusal does not say a backend is what is missing:\n%s", stderr)
	}
	if !bytes.Equal(before, configBytes(t, cfg)) {
		t.Error("a refused configure changed the configuration file")
	}
}

func TestIssue1154_ConfigurePreflightProvesTheConfigurationAndWritesNothing(t *testing.T) {
	cfg := writeTestConfig(t)
	before := configBytes(t, cfg)
	dir := t.TempDir()

	var code int
	out := captureStdout(t, func() {
		code = run([]string{"medium", "configure", "usb_disk", "--backend", "local_volume", "--field", "path=" + dir, "--preflight", "--config", cfg})
	})
	if code != 0 {
		t.Fatalf("medium configure --preflight on a writable directory = %d, want 0:\n%s", code, out)
	}
	// The directory's own steps: the free-space and distinct-volume checks a
	// bucket has no answer to, which the by-id check on a declared
	// local_volume has always run.
	for _, step := range []string{"reach", "space", "distinct_volume", "write", "read_back", "delete"} {
		if !strings.Contains(out, step) {
			t.Errorf("the report does not show the %q step:\n%s", step, out)
		}
	}
	if !bytes.Equal(before, configBytes(t, cfg)) {
		t.Errorf("--preflight wrote the configuration:\n%s", configBytes(t, cfg))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the probe left %d entries behind in the directory it tested", len(entries))
	}
}

func TestIssue1154_ConfigurePreflightExitsNonZeroWhenTheDestinationDoesNotWork(t *testing.T) {
	cfg := writeTestConfig(t)
	// A path beneath a regular file can never be written into.
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := configBytes(t, cfg)

	var code int
	out := captureStdout(t, func() {
		code = run([]string{"medium", "configure", "usb_disk", "--backend", "local_volume", "--field", "path=" + filepath.Join(file, "sub"), "--preflight", "--config", cfg})
	})
	if code != 1 {
		t.Errorf("medium configure --preflight on a path nothing can write to = %d, want 1:\n%s", code, out)
	}
	if !strings.Contains(out, "NOT ready") {
		t.Errorf("the report does not say the destination is not ready:\n%s", out)
	}
	if !bytes.Equal(before, configBytes(t, cfg)) {
		t.Error("a failing --preflight changed the configuration file")
	}
}

func TestIssue1154_ConfigureRefusesAMalformedFieldBeforeAnythingIsOpened(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-config.yaml")
	for name, tc := range map[string]struct {
		args []string
		// want is what the refusal has to say, so a command refused for
		// being an unknown verb does not pass as one refused for its flag.
		want string
	}{
		"no equals sign":     {[]string{"medium", "configure", "usb", "--backend", "local_volume", "--field", "path"}, "--field"},
		"an empty value":     {[]string{"medium", "configure", "usb", "--backend", "local_volume", "--field", "path="}, "--field"},
		"an empty field id":  {[]string{"medium", "configure", "usb", "--backend", "local_volume", "--field", "=x"}, "--field"},
		"a repeated field":   {[]string{"medium", "configure", "usb", "--backend", "local_volume", "--field", "path=/a", "--field", "path=/b"}, "--field"},
		"no medium id":       {[]string{"medium", "configure", "--backend", "local_volume", "--field", "path=/a"}, "configure"},
		"--field on list":    {[]string{"medium", "list", "--field", "path=/a"}, "medium configure"},
		"--backend on show":  {[]string{"medium", "show", "usb", "--backend", "s3"}, "medium configure"},
		"--preflight on add": {[]string{"medium", "add", "usb", "--bucket", "b", "--preflight"}, "medium configure"},
	} {
		t.Run(name, func(t *testing.T) {
			var code int
			stderr := captureStderr(t, func() {
				captureStdout(t, func() { code = run(append(tc.args, "--config", missing)) })
			})
			if code != exitUsage {
				t.Errorf("%v = %d, want %d (a usage error, before the configuration is even read)", tc.args, code, exitUsage)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("%v was refused without saying %q:\n%s", tc.args, tc.want, stderr)
			}
		})
	}
}

func TestIssue1154_ConfigureJSONIsTheWireObjectTheAPIReturns(t *testing.T) {
	cfg := writeTestConfig(t)
	dir := t.TempDir()
	var code int
	out := captureStdout(t, func() {
		code = run([]string{"medium", "configure", "usb_disk", "--backend", "local_volume", "--field", "path=" + dir, "--json", "--config", cfg})
	})
	if code != 0 {
		t.Fatalf("medium configure --json = %d:\n%s", code, out)
	}
	var got apicontract.StorageMediumSummary
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("--json is not the contract's StorageMediumSummary: %v\n%s", err, out)
	}
	if got.ID != "usb_disk" || got.Type != "local_volume" || got.Path != dir {
		t.Errorf("--json = %+v, want the destination as persisted (id usb_disk, type local_volume, path %s)", got, dir)
	}
	var keys map[string]any
	if err := json.Unmarshal([]byte(out), &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "type", "path", "is_default", "is_local", "reads_require_restore", "storage_class", "upload_verification"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("--json is missing the API's %q field:\n%s", k, out)
		}
	}
}

func TestIssue1154_ConfigurePreflightJSONIsTheWireObjectTheAPIReturns(t *testing.T) {
	cfg := writeTestConfig(t)
	var code int
	out := captureStdout(t, func() {
		code = run([]string{"medium", "configure", "usb_disk", "--backend", "local_volume", "--field", "path=" + t.TempDir(), "--preflight", "--json", "--config", cfg})
	})
	if code != 0 {
		t.Fatalf("medium configure --preflight --json = %d:\n%s", code, out)
	}
	var got apicontract.MediumPreflightResponse
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("--json is not the contract's MediumPreflightResponse: %v\n%s", err, out)
	}
	if got.Medium != "usb_disk" || !got.OK || len(got.Checks) == 0 {
		t.Errorf("--json = %+v, want a passing report for usb_disk that lists its steps", got)
	}
}

func TestIssue1154_BackendsListsTheRegistryAsText(t *testing.T) {
	cfg := writeTestConfig(t)
	var code int
	out := captureStdout(t, func() { code = run([]string{"medium", "backends", "--config", cfg}) })
	if code != 0 {
		t.Fatalf("medium backends = %d:\n%s", code, out)
	}
	for _, want := range []string{"local_volume", "s3", "sftp", "path", "required", "reserved_instance_id", "instance_id_pattern"} {
		if !strings.Contains(out, want) {
			t.Errorf("medium backends does not print %q:\n%s", want, out)
		}
	}
	// sftp is registered and not configurable in this build; saying so is
	// the whole reason a row for it is served.
	if !strings.Contains(out, "configurable: false") {
		t.Errorf("medium backends does not say which backend cannot be configured:\n%s", out)
	}
}

func TestIssue1154_BackendsJSONIsTheContractsListBackendsResponse(t *testing.T) {
	var code int
	out := captureStdout(t, func() { code = run([]string{"medium", "backends", "--json", "--config", writeTestConfig(t)}) })
	if code != 0 {
		t.Fatalf("medium backends --json = %d:\n%s", code, out)
	}
	var got apicontract.ListBackendsResponse
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("--json is not the contract's ListBackendsResponse: %v\n%s", err, out)
	}
	ids := map[string]apicontract.BackendManifest{}
	for _, b := range got.Backends {
		ids[b.ID] = b
	}
	for _, id := range []string{"local_volume", "s3", "sftp"} {
		if _, ok := ids[id]; !ok {
			t.Errorf("--json lists no %q backend: %v", id, got.Backends)
		}
	}
	var pathRequired bool
	for _, f := range ids["local_volume"].Fields {
		if f.ID == "path" {
			pathRequired = f.Required
		}
	}
	if !pathRequired {
		t.Error("--json does not carry local_volume's required `path` field, which is the thing configure needs")
	}
	if got.InstanceIDPattern == "" || got.ReservedInstanceID == "" {
		t.Errorf("--json drops the naming rules: %q, %q", got.InstanceIDPattern, got.ReservedInstanceID)
	}
}

func TestIssue1154_BackendsTakesNoArgumentAndNeedsNoConfiguration(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-config.yaml")
	var code int
	captureStderr(t, func() {
		captureStdout(t, func() { code = run([]string{"medium", "backends", "extra", "--config", missing}) })
	})
	if code != exitUsage {
		t.Errorf("medium backends extra = %d, want %d", code, exitUsage)
	}
	// The registry is embedded in the binary, so asking for it must not
	// depend on a configuration that loads.
	captureStdout(t, func() { code = run([]string{"medium", "backends", "--config", missing}) })
	if code != 0 {
		t.Errorf("medium backends against a configuration that does not exist = %d, want 0: the catalogue is built into the binary", code)
	}
}

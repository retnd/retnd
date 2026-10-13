package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/recoverytest"
	"github.com/retnd/retnd/core/internal/state"
)

// #1111 as the deployment sees it: the manual test falls back to the built-in
// structure check, the scheduler runs it only under mode builtin, and the
// stored output is the four records.

func gzBytes(t *testing.T, payload string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if _, err := zw.Write([]byte(strings.Repeat(payload, 200))); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// seedBytesArtifact journals a COMPLETE local artifact whose file holds content.
func seedBytesArtifact(t *testing.T, ctx context.Context, journal *state.Journal, bs config.BackupSet, name string, content []byte, at time.Time) model.ArtifactID {
	t.Helper()
	artifact, err := model.NewArtifactID(bs.ID, name)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bs.LocalPath, name)
	mustWriteFile(t, path, string(content))
	if _, err := journal.RecordTransition(ctx, state.Transition{
		Artifact: artifact, Key: "discover-" + name, From: "", To: string(lifecycle.Discovered), OccurredAt: at, RemotePath: "/backups/" + name,
	}); err != nil {
		t.Fatal(err)
	}
	size := int64(len(content))
	sum := sha256.Sum256(content)
	lp := path
	if _, err := journal.RecordTransition(ctx, state.Transition{
		Artifact: artifact, Key: "complete-" + name, From: string(lifecycle.Discovered), To: string(lifecycle.Complete), OccurredAt: at, LocalPath: &lp,
		Transfer: &state.TransferResult{BytesTransferred: size, Checksummed: true},
		Placement: &state.PlacementUpdate{
			Medium: state.MediumLocal, Location: path, Size: &size, Hash: hex.EncodeToString(sum[:]), HashAlg: "sha256",
			VerificationClass: state.VerificationContent, Status: state.PlacementActive,
		},
	}); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func builtinService(t *testing.T, mode, builtinCheck string, stages *validatorStages) (*Service, *state.Journal, config.BackupSet) {
	t.Helper()
	svc, journal, _ := recoveryService(t, stages)
	rt := &svc.Config.Sources[0].BackupSets[0].Assurance.RecoveryTest
	rt.Mode, rt.BuiltinCheck = mode, builtinCheck
	return svc, journal, svc.Config.Sources[0].BackupSets[0]
}

func TestIssue1111_AManualTestOfASetWithNoValidatorReadsTheFileAsItsFormat(t *testing.T) {
	ctx := context.Background()
	svc, journal, bs := builtinService(t, config.RecoveryTestOff, "", &validatorStages{configured: false})
	a := seedBytesArtifact(t, ctx, journal, bs, "nightly.sql.gz", gzBytes(t, "row\n"), retentionTestNow.Add(-time.Hour))

	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.State != state.RecoveryPassed || res.Result.Check != "builtin" {
		t.Fatalf("state/check = %q/%q (%s)", res.Result.State, res.Result.Check, res.Result.Reason)
	}
	p := localCopyOf(t, journal, a)
	if p.RecoveryValidatorID != recoverytest.BuiltinValidatorID || p.RecoveryValidatorHash != recoverytest.BuiltinValidatorHash() {
		t.Errorf("identity = %q / %q", p.RecoveryValidatorID, p.RecoveryValidatorHash)
	}
	out, ok, err := journal.RecoveryOutputOf(ctx, bs.ID)
	if err != nil || !ok {
		t.Fatalf("stored output: %v %v", ok, err)
	}
	if len(out.Records) != 4 || !strings.HasPrefix(out.Records[0].Text, "built-in structure check v1") || !strings.HasPrefix(out.Records[3].Text, "this proves the file decodes") {
		t.Errorf("records = %+v", out.Records)
	}
}

func TestIssue1111_AnUnrecognisedTypeOrAnOffSwitchRefusesAndReadsNothing(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		file, check, want string
	}{
		"a .dump":           {"db.dump", "structure", "(.dump)"},
		"builtin_check off": {"db.sql.gz", "off", "the built-in check is off"},
	} {
		t.Run(name, func(t *testing.T) {
			svc, journal, bs := builtinService(t, config.RecoveryTestOff, tc.check, &validatorStages{configured: false})
			a := seedBytesArtifact(t, ctx, journal, bs, tc.file, gzBytes(t, "x"), retentionTestNow.Add(-time.Hour))
			res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
			if err != nil {
				t.Fatal(err)
			}
			if res.Result.State != state.RecoveryNotConfigured || !strings.Contains(res.Result.Reason, tc.want) {
				t.Errorf("state/reason = %q / %q", res.Result.State, res.Result.Reason)
			}
			if p := localCopyOf(t, journal, a); p.RecoveryBytesRead != nil && *p.RecoveryBytesRead > 0 {
				t.Errorf("something was read: %d", *p.RecoveryBytesRead)
			}
		})
	}
}

func TestIssue1111_ADeclaredValidatorStillRunsInsteadOfTheBuiltin(t *testing.T) {
	ctx := context.Background()
	stages := accepts()
	svc, journal, bs := builtinService(t, config.RecoveryTestRequired, "", stages)
	a := seedBytesArtifact(t, ctx, journal, bs, "nightly.sql.gz", gzBytes(t, "row\n"), retentionTestNow.Add(-time.Hour))
	res, err := svc.RecoveryTest(ctx, RecoveryTestRequest{Set: bs.ID})
	if err != nil {
		t.Fatal(err)
	}
	if res.Result.State != state.RecoveryPassed || res.Result.Check != "" || len(stages.validated) != 1 {
		t.Errorf("state/check/validated = %q/%q/%d", res.Result.State, res.Result.Check, len(stages.validated))
	}
	if p := localCopyOf(t, journal, a); recoverytest.IsBuiltin(p.RecoveryValidatorID) {
		t.Errorf("the record carries the built-in identity %q", p.RecoveryValidatorID)
	}
}

// --- the schedule ---

func TestIssue1111_TheTickRunsTheBuiltinForAModeBuiltinSetOnItsCadence(t *testing.T) {
	ctx := context.Background()
	svc, journal, bs := builtinService(t, config.RecoveryTestBuiltin, "structure", nil)
	svc.Stages = nil // the built-in needs no script runner
	a := seedBytesArtifact(t, ctx, journal, bs, "nightly.sql.gz", gzBytes(t, "row\n"), retentionTestNow.Add(-time.Hour))

	svc.RecoveryTestTick(ctx)
	p := localCopyOf(t, journal, a)
	if p.RecoveryState != state.RecoveryPassed || p.RecoveryValidatorID != recoverytest.BuiltinValidatorID {
		t.Fatalf("after the tick: %q / %q", p.RecoveryState, p.RecoveryValidatorID)
	}
	first := *p.RecoveryTestedAt

	svc.Now = fixedNow(retentionTestNow.Add(time.Hour))
	svc.RecoveryTestTick(ctx)
	if got := localCopyOf(t, journal, a).RecoveryTestedAt; !got.Equal(first) {
		t.Errorf("a copy that was not due was tested again")
	}
	svc.Now = fixedNow(retentionTestNow.Add(config.DefaultRecoveryTestCadence + time.Hour))
	svc.RecoveryTestTick(ctx)
	if got := localCopyOf(t, journal, a); !got.RecoveryTestedAt.After(first) {
		t.Errorf("past the cadence the built-in was not run again")
	}
}

func TestIssue1111_TheTickRunsNothingForAModeOffSetEvenWithARecognisedArtifact(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{config.RecoveryTestOff, config.RecoveryTestInherit, ""} {
		t.Run("mode "+mode, func(t *testing.T) {
			svc, journal, bs := builtinService(t, mode, "structure", &validatorStages{configured: false})
			a := seedBytesArtifact(t, ctx, journal, bs, "nightly.sql.gz", gzBytes(t, "row\n"), retentionTestNow.Add(-time.Hour))
			svc.RecoveryTestTick(ctx)
			if p := localCopyOf(t, journal, a); p.RecoveryState != state.RecoveryUnknown {
				t.Errorf("the tick started a run under mode %q: %q", mode, p.RecoveryState)
			}
		})
	}
}

// --- the answer a button is built on ---

func TestIssue1111_EffectiveCheckSaysWhatARunWouldDo(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		stages    *validatorStages
		check     string
		file      string
		wantCheck string
		reasonIn  string
	}{
		"a declared validator":      {accepts(), "structure", "a.gz", "validator", ""},
		"nothing declared, a .gz":   {&validatorStages{configured: false}, "structure", "a.gz", "builtin", ""},
		"nothing declared, a .dump": {&validatorStages{configured: false}, "structure", "a.dump", "none", "(.dump)"},
		"nothing declared, off":     {&validatorStages{configured: false}, "off", "a.gz", "none", "the built-in check is off"},
		"a declared validator, off": {accepts(), "off", "a.gz", "validator", ""},
	} {
		t.Run(name, func(t *testing.T) {
			svc, journal, bs := builtinService(t, config.RecoveryTestOff, tc.check, tc.stages)
			seedBytesArtifact(t, ctx, journal, bs, tc.file, gzBytes(t, "x"), retentionTestNow.Add(-time.Hour))
			got, reason, err := svc.RecoveryTestEffectiveCheck(ctx, bs.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.wantCheck || (tc.reasonIn != "" && !strings.Contains(reason, tc.reasonIn)) || (tc.wantCheck != "none" && reason != "") {
				t.Errorf("effective = %q (%q), want %q / %q", got, reason, tc.wantCheck, tc.reasonIn)
			}
		})
	}
}

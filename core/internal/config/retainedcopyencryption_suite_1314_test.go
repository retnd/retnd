package config

import (
	"strings"
	"testing"

	"github.com/retnd/retnd/core/internal/copycrypt"
)

// EPIC T, T1.4 (#1314): the deployment-wide `suite` policy. A pinned id is
// honoured exactly or refused; `default` and writing nothing keep the
// deployment on the suite of its active managed key, which config cannot
// see, so they resolve to RetainedCopyEncryptionSuiteDefault and the engine
// finishes the job (owner decision, 2026-11-10).

func suiteYAML(suite string) string {
	line := ""
	if suite != "" {
		line = "  suite: " + suite + "\n"
	}
	return minimalYAML("retained_copy_encryption:\n  mode: required\n"+line+"  key_provider:\n    managed: true\n", "")
}

func loadValidated(t *testing.T, body string) (*Config, error) {
	t.Helper()
	return LoadAndValidate(writeTempConfig(t, body))
}

func TestSuiteDefaultResolvesToTheBuildDefault(t *testing.T) {
	for _, declared := range []string{"", "default"} {
		c, err := loadValidated(t, suiteYAML(declared))
		if err != nil {
			t.Fatalf("suite %q: %v", declared, err)
		}
		if c.RetainedCopyEncryption.Suite != RetainedCopyEncryptionSuiteDefault {
			t.Errorf("suite %q resolved to %q, want %q (the engine resolves it from the active key, else copycrypt.Default())",
				declared, c.RetainedCopyEncryption.Suite, RetainedCopyEncryptionSuiteDefault)
		}
		if _, pinned := c.RetainedCopyEncryption.PinnedSuite(); pinned {
			t.Errorf("suite %q reads as pinned", declared)
		}
		if got := c.Sources[0].BackupSets[0].RetainedCopyEncryption.Suite; got != RetainedCopyEncryptionSuiteDefault {
			t.Errorf("suite %q: the set inherited %q", declared, got)
		}
	}
}

func TestAPinnedSuiteIsHonouredAndInherited(t *testing.T) {
	for _, s := range copycrypt.Suites() {
		c, err := loadValidated(t, suiteYAML(s.ID()))
		if err != nil {
			t.Fatalf("suite %s: %v", s.ID(), err)
		}
		if got, pinned := c.Sources[0].BackupSets[0].RetainedCopyEncryption.PinnedSuite(); !pinned || got != s.ID() {
			t.Errorf("suite %s: the set resolved to %q, %v", s.ID(), got, pinned)
		}
	}
}

func TestAnUnknownSuiteFailsLoad(t *testing.T) {
	for _, bad := range []string{"nonsense", "age", "AGE/V1", "aes-256-military-grade"} {
		_, err := loadValidated(t, suiteYAML(bad))
		if err == nil {
			t.Fatalf("suite %q loaded", bad)
		}
		if !strings.Contains(err.Error(), "retained_copy_encryption.suite") || !strings.Contains(err.Error(), bad) {
			t.Errorf("suite %q: the refusal %q does not name the key and the id", bad, err)
		}
	}
}

// fakeSuite is a registry entry with a chosen status, for the lifecycle
// paths no shipped suite exercises yet. It is only ever handed to Validate
// through lookupSuite.
type fakeSuite struct {
	copycrypt.AgeX25519
	id     string
	status copycrypt.Status
}

func (f fakeSuite) ID() string               { return f.id }
func (f fakeSuite) Status() copycrypt.Status { return f.status }

func withLookup(t *testing.T, extra fakeSuite) {
	t.Helper()
	prev := lookupSuite
	lookupSuite = func(id string) (copycrypt.Suite, bool) {
		if id == extra.id {
			return extra, true
		}
		return prev(id)
	}
	t.Cleanup(func() { lookupSuite = prev })
}

func TestARetiredSuiteFailsLoad(t *testing.T) {
	withLookup(t, fakeSuite{id: "old-test-suite/v1", status: copycrypt.StatusRetired})
	_, err := loadValidated(t, suiteYAML("old-test-suite/v1"))
	if err == nil {
		t.Fatal("a retired suite loaded")
	}
	if !strings.Contains(err.Error(), "old-test-suite/v1") || !strings.Contains(err.Error(), "retired") {
		t.Errorf("the refusal %q does not name the id and say it is retired", err)
	}
}

func TestADeprecatedSuiteLoadsWithAWarning(t *testing.T) {
	withLookup(t, fakeSuite{id: "aging-test-suite/v1", status: copycrypt.StatusDeprecated})
	c, err := loadValidated(t, suiteYAML("aging-test-suite/v1"))
	if err != nil {
		t.Fatalf("a deprecated suite was refused: %v", err)
	}
	if !c.RetainedCopyEncryption.SuiteDeprecated || !c.Sources[0].BackupSets[0].RetainedCopyEncryption.SuiteDeprecated {
		t.Errorf("a deprecated suite loaded without the flag health reads: %+v", c.RetainedCopyEncryption)
	}
}

func TestSuiteIsReResolvedAfterAGlobalMutation(t *testing.T) {
	c := validConfig()
	c.RetainedCopyEncryptionBlock = &RetainedCopyEncryptionBlock{Mode: RetainedCopyEncryptionRequired, KeyProvider: managed()}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := c.Sources[0].BackupSets[0].RetainedCopyEncryption.Suite; got != RetainedCopyEncryptionSuiteDefault {
		t.Fatalf("before the mutation the set resolved to %q", got)
	}
	c.RetainedCopyEncryptionBlock.Suite = copycrypt.AgeX25519ID
	// Not re-resolved yet: the resolved field is a snapshot on purpose.
	if got := c.Sources[0].BackupSets[0].RetainedCopyEncryption.Suite; got != RetainedCopyEncryptionSuiteDefault {
		t.Fatalf("the set moved to %q without a re-resolve", got)
	}
	if err := c.ResolveRetainedCopyEncryption(); err != nil {
		t.Fatal(err)
	}
	if got, pinned := c.Sources[0].BackupSets[0].RetainedCopyEncryption.PinnedSuite(); !pinned || got != copycrypt.AgeX25519ID {
		t.Errorf("after the re-resolve the set reads %q, %v; want age/v1 pinned", got, pinned)
	}
}

// A per-set suite is not a field: the set inherits and has no say.
func TestThereIsNoPerSetSuite(t *testing.T) {
	path := writeTempConfig(t, minimalYAML("", "        retained_copy_encryption:\n          mode: required\n          suite: age/v1\n"))
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "field suite not found") {
		t.Errorf("a per-set suite was not refused at Load: %v", err)
	}
}

// A file that never wrote the key re-marshals without it.
func TestAnUnwrittenSuiteIsNotWrittenBack(t *testing.T) {
	c, err := loadValidated(t, suiteYAML(""))
	if err != nil {
		t.Fatal(err)
	}
	b, ok := c.DeclaredRetainedCopyEncryption()
	if !ok || b.Suite != "" {
		t.Errorf("the declared block carries suite %q; nothing was written", b.Suite)
	}
}

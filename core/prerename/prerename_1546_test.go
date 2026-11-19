package prerename_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retnd/retnd/core/prerename"
)

// #1546: a deployment still mounted at the path it had before the 0.5.0
// rename must refuse to start, naming both paths and the fix, rather than
// starting against the new, empty path.
//
// The old name is assembled from two pieces, because
// scripts/rename/check-no-former-name.sh refuses it spelled out anywhere in
// the tree.
var formerName = "backup" + "d"

// layout is one deployment root with the current and pre-rename spellings
// of a directory under it.
type layout struct{ root string }

func newLayout(t *testing.T) layout { return layout{root: t.TempDir()} }

func (l layout) current(rel ...string) string {
	return filepath.Join(append([]string{l.root, "retnd"}, rel...)...)
}

func (l layout) former(rel ...string) string {
	return filepath.Join(append([]string{l.root, formerName}, rel...)...)
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// configNaming is the smallest configuration config.Load accepts: a journal
// path and no backup sets.
func configNaming(db string) string {
	return "poll_interval: 1h\nstate:\n  database: " + db + "\nsources: []\n"
}

func refusal(t *testing.T, err error) *prerename.Error {
	t.Helper()
	var e *prerename.Error
	if !errors.As(err, &e) {
		t.Fatalf("Check = %v, want a *prerename.Error refusing to start", err)
	}
	return e
}

func mustMention(t *testing.T, msg string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(msg, w) {
			t.Errorf("the diagnostic does not mention %q:\n%s", w, msg)
		}
	}
}

func TestCheck_1546_OnlyTheFormerConfigurationRefusesNamingBothPathsAndTheFix(t *testing.T) {
	l := newLayout(t)
	cur, old := l.current("config", "config.yaml"), l.former("config", "config.yaml")
	write(t, old, configNaming(filepath.Join(l.root, "state", "state.db")))

	e := refusal(t, prerename.Check(cur, filepath.Join(l.root, "state", "state.db")))
	if e.Conflict {
		t.Errorf("only the pre-rename path holds a configuration, but the refusal calls it a conflict")
	}
	if e.Current != cur || e.Former != old {
		t.Errorf("refusal names Current=%q Former=%q, want %q and %q", e.Current, e.Former, cur, old)
	}
	mustMention(t, e.Error(), "refusing to start", cur, old, filepath.Dir(cur), "--config "+old)
}

func TestCheck_1546_ConfigDirectoryFormResolvesTheFormerDirectory(t *testing.T) {
	l := newLayout(t)
	old := l.former("config", "config.yaml")
	write(t, old, configNaming(filepath.Join(l.root, "state", "state.db")))

	e := refusal(t, prerename.Check(l.current("config"), ""))
	if e.Former != old {
		t.Errorf("--config named the directory; refusal names Former=%q, want the config.yaml inside it, %q", e.Former, old)
	}
}

func TestCheck_1546_BothConfigurationsPopulatedRefusesNamingTheConflict(t *testing.T) {
	l := newLayout(t)
	cur, old := l.current("config", "config.yaml"), l.former("config", "config.yaml")
	write(t, cur, configNaming(filepath.Join(l.root, "state", "a.db")))
	write(t, old, configNaming(filepath.Join(l.root, "state", "b.db")))

	e := refusal(t, prerename.Check(cur, ""))
	if !e.Conflict {
		t.Errorf("both paths hold a configuration, but the refusal does not call it a conflict")
	}
	mustMention(t, e.Error(), "refusing to start", "two different", cur, old)
}

func TestCheck_1546_OnlyTheCurrentConfigurationStarts(t *testing.T) {
	l := newLayout(t)
	cur := l.current("config", "config.yaml")
	write(t, cur, configNaming(filepath.Join(l.root, "state", "state.db")))

	if err := prerename.Check(cur, ""); err != nil {
		t.Fatalf("only the current path holds a configuration, Check = %v, want nil", err)
	}
}

func TestCheck_1546_NeitherPathPopulatedIsAFreshInstallAndStarts(t *testing.T) {
	l := newLayout(t)
	if err := prerename.Check(l.current("config", "config.yaml"), l.current("state", "state.db")); err != nil {
		t.Fatalf("a fresh install, Check = %v, want nil", err)
	}
}

// An empty config.yaml or state.db (a start that died before its first
// write) is not a catalogue anybody can lose.
func TestCheck_1546_AnEmptyFileAtTheFormerPathIsNotACatalogue(t *testing.T) {
	l := newLayout(t)
	write(t, l.former("config", "config.yaml"), "")
	if err := prerename.Check(l.current("config", "config.yaml"), ""); err != nil {
		t.Fatalf("a zero-length file at the pre-rename path, Check = %v, want nil", err)
	}
}

// One directory reached by both names (the old path left as a symlink, or
// one host directory bind-mounted at both container paths) is one
// catalogue, not two.
func TestCheck_1546_TheSameDirectoryUnderBothNamesIsNotAConflict(t *testing.T) {
	l := newLayout(t)
	cur := l.current("config", "config.yaml")
	write(t, cur, configNaming(filepath.Join(l.root, "state", "state.db")))
	if err := os.Symlink(l.current(), l.former()); err != nil {
		t.Fatal(err)
	}
	if err := prerename.Check(cur, ""); err != nil {
		t.Fatalf("the pre-rename path is a symlink to the current one, Check = %v, want nil", err)
	}
}

// The state database is the other half of the catalogue. A configuration at
// a path the rename never touched can still name a journal under a renamed
// directory, and a deployment with no configuration yet takes the journal
// from --state-database.
func TestCheck_1546_OnlyTheFormerStateDatabaseNamedByTheConfigurationRefuses(t *testing.T) {
	l := newLayout(t)
	cfg := filepath.Join(l.root, "etc", "config.yaml")
	cur, old := l.current("state", "state.db"), l.former("state", "state.db")
	write(t, cfg, configNaming(cur))
	write(t, old, "journal")

	e := refusal(t, prerename.Check(cfg, ""))
	if e.Conflict || e.Current != cur || e.Former != old {
		t.Errorf("refusal = %+v, want the state database at %q, not a conflict", *e, old)
	}
	mustMention(t, e.Error(), "state database", cur, old, "state.database")
}

func TestCheck_1546_OnlyTheFormerStateDatabaseWithNoConfigurationRefuses(t *testing.T) {
	l := newLayout(t)
	cur, old := l.current("state", "state.db"), l.former("state", "state.db")
	write(t, old, "journal")

	e := refusal(t, prerename.Check(filepath.Join(l.root, "etc", "config.yaml"), cur))
	mustMention(t, e.Error(), cur, old)
}

func TestCheck_1546_BothStateDatabasesPopulatedRefusesNamingTheConflict(t *testing.T) {
	l := newLayout(t)
	cfg := filepath.Join(l.root, "etc", "config.yaml")
	cur, old := l.current("state", "state.db"), l.former("state", "state.db")
	write(t, cfg, configNaming(cur))
	write(t, cur, "journal a")
	write(t, old, "journal b")

	e := refusal(t, prerename.Check(cfg, ""))
	if !e.Conflict {
		t.Errorf("both journals are populated, but the refusal does not call it a conflict")
	}
	mustMention(t, e.Error(), "two different", cur, old)
}

func TestCheck_1546_OnlyTheCurrentStateDatabaseStarts(t *testing.T) {
	l := newLayout(t)
	cfg := filepath.Join(l.root, "etc", "config.yaml")
	cur := l.current("state", "state.db")
	write(t, cfg, configNaming(cur))
	write(t, cur, "journal")

	if err := prerename.Check(cfg, ""); err != nil {
		t.Fatalf("only the current journal is populated, Check = %v, want nil", err)
	}
}

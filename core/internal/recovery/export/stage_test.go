package export

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStaging(t *testing.T, c *fakeClock) *Staging {
	t.Helper()
	return &Staging{
		Root:       t.TempDir(),
		PartialTTL: time.Hour,
		OutputTTL:  24 * time.Hour,
		Now:        c.Now,
		FreeBytes:  func(string) (int64, error) { return 1 << 40, nil },
	}
}

func TestStagingBeginGivesUniqueNamesCreatedExclusively(t *testing.T) {
	c := newClock()
	s := newStaging(t, c)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		f, err := s.Begin(10)
		if err != nil {
			t.Fatal(err)
		}
		if seen[f.Name()] {
			t.Fatalf("name %q handed out twice", f.Name())
		}
		seen[f.Name()] = true
		if filepath.Dir(f.Name()) != s.Root {
			t.Fatalf("partial %q is not directly under the root", f.Name())
		}
		if !strings.HasSuffix(f.Name(), ".partial") {
			t.Fatalf("partial %q must say it is one", f.Name())
		}
		f.Close()
	}
}

func TestStagingRefusesBelowTheReserveBeforeCreatingAnything(t *testing.T) {
	c := newClock()
	s := newStaging(t, c)
	s.Reserve = 1000
	s.FreeBytes = func(string) (int64, error) { return 1500, nil }
	if _, err := s.Begin(600); !errors.Is(err, ErrBelowReserve) {
		t.Fatalf("1500 free - 600 needed < 1000 reserve: want ErrBelowReserve, got %v", err)
	}
	if e, _ := s.Entries(); len(e) != 0 {
		t.Fatalf("a refusal left %v behind", e)
	}
	if f, err := s.Begin(400); err != nil {
		t.Fatalf("exactly at the reserve must be allowed: %v", err)
	} else {
		f.Close()
	}
}

func TestStagingUnknownFreeSpaceRefuses(t *testing.T) {
	c := newClock()
	s := newStaging(t, c)
	s.FreeBytes = func(string) (int64, error) { return 0, errors.New("statfs failed") }
	if _, err := s.Begin(1); err == nil {
		t.Fatal("when free space cannot be measured the safe answer is to refuse")
	}
}

func TestStagingSweepRemovesOnlyOwnedEntriesPastTheirTTL(t *testing.T) {
	c := newClock()
	s := newStaging(t, c)

	oldPartial := filepath.Join(s.Root, ".rcv-aaaa.partial")
	freshPartial := filepath.Join(s.Root, ".rcv-bbbb.partial")
	oldOutput := filepath.Join(s.Root, "rcv-cccc-backup.tar")
	freshOutput := filepath.Join(s.Root, "rcv-dddd-backup.tar")
	foreign := filepath.Join(s.Root, "operators-own-file.tar")
	foreignPartial := filepath.Join(s.Root, "upload.partial")
	for _, p := range []string{oldPartial, freshPartial, oldOutput, freshOutput, foreign, foreignPartial} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	age := func(p string, d time.Duration) {
		at := c.Now().Add(-d)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	age(oldPartial, 2*time.Hour)
	age(freshPartial, time.Minute)
	age(oldOutput, 48*time.Hour)
	age(freshOutput, 2*time.Hour)
	age(foreign, 1000*time.Hour)
	age(foreignPartial, 1000*time.Hour)

	rep, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 2 || len(rep.Failed) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	for _, gone := range []string{oldPartial, oldOutput} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s should have been swept", gone)
		}
	}
	for _, kept := range []string{freshPartial, freshOutput, foreign, foreignPartial} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("%s must survive: %v", kept, err)
		}
	}
}

func TestStagingReconcileRemovesEveryPartialAndReportsThem(t *testing.T) {
	c := newClock()
	s := newStaging(t, c)
	for i := 0; i < 3; i++ {
		p := filepath.Join(s.Root, fmt.Sprintf(".rcv-%04d.partial", i))
		if err := os.WriteFile(p, []byte("half a file"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(s.Root, "rcv-ffff-done.tar")
	foreign := filepath.Join(s.Root, "not-ours.partial")
	if err := os.WriteFile(output, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := s.Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 3 {
		t.Fatalf("a fresh start owns no in-flight work, so all three partials go: %+v", rep)
	}
	for _, p := range []string{output, foreign} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("reconcile removed %s: %v", p, err)
		}
	}
}

func TestStagingCleanupFailureIsReportedSeparatelyFromSuccess(t *testing.T) {
	c := newClock()
	s := newStaging(t, c)
	p := filepath.Join(s.Root, ".rcv-stuck.partial")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Remove = func(string) error { return errors.New("device busy") }
	rep, err := s.Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Failed) != 1 || rep.Failed[0].Path != p || len(rep.Removed) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestStagingPublishPlacesAtomicallyUnderTheRootWithoutClobbering(t *testing.T) {
	c := newClock()
	s := newStaging(t, c)
	f, _ := s.Begin(5)
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	f.Close()

	final, err := s.Publish(f.Name(), "restored/backup.tar")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(final)) != s.Root && filepath.Dir(final) != s.Root {
		t.Fatalf("published outside the root: %s", final)
	}
	got, _ := os.ReadFile(final)
	if string(got) != "hello" {
		t.Fatalf("content = %q", got)
	}
	if _, err := os.Stat(f.Name()); !os.IsNotExist(err) {
		t.Fatal("the partial must be gone once published")
	}
	if !strings.HasPrefix(filepath.Base(final), "rcv-") {
		t.Fatalf("published name %q is not an owned, uniquely named output", final)
	}

	g, _ := s.Begin(5)
	if _, err := g.Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	g.Close()
	second, err := s.Publish(g.Name(), "restored/backup.tar")
	if err != nil {
		t.Fatal(err)
	}
	if second == final {
		t.Fatal("two recoveries to the same requested name must not collide")
	}
}

func TestStagingPublishRefusesEscapes(t *testing.T) {
	c := newClock()
	s := newStaging(t, c)
	outside := t.TempDir()
	link := filepath.Join(s.Root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../x", "/etc/passwd", "a/../../x", "link/x", "", ".", "a\x00b"} {
		f, _ := s.Begin(1)
		if _, err := f.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		f.Close()
		if _, err := s.Publish(f.Name(), rel); !errors.Is(err, ErrTargetOutsideRoot) {
			t.Fatalf("rel %q: want ErrTargetOutsideRoot, got %v", rel, err)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("something was written through the symlink: %v", entries)
	}
}

func TestStagingPublishRefusesToPublishAForeignFile(t *testing.T) {
	c := newClock()
	s := newStaging(t, c)
	foreign := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(foreign, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(foreign, "x"); err == nil {
		t.Fatal("Publish must only move files this staging area created")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("the foreign file was moved: %v", err)
	}
}

func TestStagingCountsWhatIsAlreadySpokenForSoTwoCopiesCannotBothFit(t *testing.T) {
	c := newClock()
	s := newStaging(t, c)
	s.Reserve = 500
	s.FreeBytes = func(string) (int64, error) { return 1500, nil }

	first, err := s.Begin(600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	// Neither file has been written yet, so the disk still reports 1500
	// free. Two concurrent recoveries each checking that alone would both
	// pass; together they would leave 300, under the reserve.
	if _, err := s.Begin(600); !errors.Is(err, ErrBelowReserve) {
		t.Fatalf("a second copy that cannot fit beside the first was admitted: %v", err)
	}
	s.Release(600)
	second, err := s.Begin(600)
	if err != nil {
		t.Fatalf("after the first released its claim there is room again: %v", err)
	}
	second.Close()
}

func TestStageGivesBackItsClaimOnSuccessAndOnFailure(t *testing.T) {
	e, st := newEngine(t)
	st.Reserve = 500
	st.FreeBytes = func(string) (int64, error) { return 1500, nil }
	data := sample(600)
	for i := 0; i < 3; i++ {
		src := goodSource(data)
		if i == 1 {
			src = &bytesSource{data: data, flipAt: 3}
		}
		s, err := e.Stage(context.Background(), src, expectFor(data, OriginLocal))
		if i != 1 && err != nil {
			t.Fatalf("stage %d: %v (the previous one kept its claim)", i, err)
		}
		if s != nil {
			_ = s.Discard()
		}
	}
}

func TestATargetNameHasALengthBound(t *testing.T) {
	c := newClock()
	s := newStaging(t, c)
	if err := s.ValidateName(strings.Repeat("a/", 300) + "x"); !errors.Is(err, ErrTargetTooLong) {
		t.Fatalf("want ErrTargetTooLong, got %v", err)
	}
	if err := s.ValidateName("restored/dump.tar"); err != nil {
		t.Fatalf("an ordinary name was refused: %v", err)
	}
}

func TestAMintTimeNameCheckRefusesEveryEscapeToo(t *testing.T) {
	// ValidateName is what a mint calls, before any grant exists. Publish
	// checks again, which is defence in depth, and means a Publish-only test
	// cannot tell whether the first line of defence works at all.
	c := newClock()
	s := newStaging(t, c)
	link := filepath.Join(s.Root, "link")
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../x", "/etc/passwd", "a/../../x", "link/x", "", ".", "a\x00b", "a\\b"} {
		if err := s.ValidateName(rel); !errors.Is(err, ErrTargetOutsideRoot) {
			t.Errorf("ValidateName(%q): want ErrTargetOutsideRoot, got %v", rel, err)
		}
	}
}

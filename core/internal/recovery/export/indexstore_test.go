package export

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTheIndexStoreRoundTripsAndTreatsAnythingWrongAsAbsent(t *testing.T) {
	dir := t.TempDir()
	s := &IndexStore{Dir: dir}
	data := payload(testChunk*3 + 1)
	ix := buildIdx(t, data)
	exp := idxExpect(data)
	key := IndexKey("p/s/a", exp.Hash, testChunk)
	if _, ok := s.Load(key, exp); ok {
		t.Fatal("an empty store produced an index")
	}
	if err := s.Save(key, ix); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Load(key, exp); !ok || got.Size != ix.Size {
		t.Fatal("a saved index did not load")
	}
	// The journal now records different bytes: the stored index is about
	// something else and must not be believed.
	other := idxExpect(payload(testChunk))
	if _, ok := s.Load(key, other); ok {
		t.Fatal("an index about other bytes was accepted")
	}
	// A damaged file is a miss, not a failure.
	p := filepath.Join(dir, key+indexSuffix)
	raw, _ := os.ReadFile(p)
	raw[len(raw)/2] ^= 1
	_ = os.WriteFile(p, raw, 0o600)
	if _, ok := s.Load(key, exp); ok {
		t.Fatal("a damaged index was accepted")
	}
}

func TestSavingLeavesNoTemporaryFileBehind(t *testing.T) {
	dir := t.TempDir()
	s := &IndexStore{Dir: dir}
	data := payload(100)
	if err := s.Save("k", buildIdx(t, data)); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "k"+indexSuffix {
		t.Fatalf("directory holds %v", ents)
	}
}

func TestSweepBoundsTheStoreByAgeAndByCount(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s := &IndexStore{Dir: dir, MaxFiles: 2, MaxAge: 24 * time.Hour, Now: func() time.Time { return now }}
	ix := buildIdx(t, payload(100))
	age := func(name string, ago time.Duration) {
		if err := s.Save(name, ix); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-ago)
		_ = os.Chtimes(filepath.Join(dir, name+indexSuffix), at, at)
	}
	age("old", 48*time.Hour)
	age("a", 3*time.Hour)
	age("b", 2*time.Hour)
	age("c", time.Hour)
	// A torn temporary file from a crashed save.
	torn := filepath.Join(dir, indexTmp+"x")
	_ = os.WriteFile(torn, []byte("x"), 0o600)
	_ = os.Chtimes(torn, now.Add(-2*time.Hour), now.Add(-2*time.Hour))

	removed, err := s.Sweep()
	if err != nil || removed != 3 {
		t.Fatalf("removed %d, %v; want old, the oldest over the count, and the torn file", removed, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Fatalf("kept %v", ents)
	}
	for _, e := range ents {
		if e.Name() == "old"+indexSuffix || e.Name() == "a"+indexSuffix {
			t.Fatalf("kept %s", e.Name())
		}
	}
}

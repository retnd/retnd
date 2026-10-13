package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/lifecycle"
	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/state"
)

// Recovering an entire backup set, and several backups to one destination
// (#1106, #1107). Every test drives the real service against the same
// doubles the single-recovery tests use, and asserts what was LEFT on the
// destination as well as what was reported.

// manyFile is one backup the tests seed into the fixture's set.
type manyFile struct {
	name   string
	data   []byte
	age    time.Duration // how long ago it was updated
	state  lifecycle.State
	medium string // "local" (default), "warm" or "cold"
}

func (f *recoveryFixture) seedMany(t *testing.T, files ...manyFile) []string {
	t.Helper()
	ctx := context.Background()
	set, _ := model.NewBackupSetID("production", "postgres")
	var ids []string
	for i, mf := range files {
		if mf.state == "" {
			mf.state = lifecycle.Complete
		}
		if mf.medium == "" {
			mf.medium = state.MediumLocal
		}
		a, err := model.NewArtifactID(set, mf.name)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.String())
		at := time.Now().UTC().Add(-mf.age)
		if _, err := f.b.journal.RecordTransition(ctx, state.Transition{
			Artifact: a, Key: "m-d-" + mf.name, To: string(lifecycle.Discovered), OccurredAt: at.Add(-time.Minute), RemotePath: "/r/" + mf.name,
		}); err != nil {
			t.Fatal(err)
		}
		tr := state.Transition{
			Artifact: a, Key: "m-c-" + mf.name, From: string(lifecycle.Discovered), To: string(mf.state), OccurredAt: at,
		}
		switch mf.state {
		case lifecycle.Quarantined, lifecycle.QuarantinedLost, lifecycle.Failed:
			// No copy: the row is what the test is about.
		default:
			size := int64(len(mf.data))
			tr.Hashes = &state.HashUpdate{Hash: sha(mf.data), Alg: "sha256"}
			tr.Transfer = &state.TransferResult{BytesTransferred: size, Checksummed: true}
			verified := at
			pu := state.PlacementUpdate{Medium: mf.medium, Size: &size, Hash: sha(mf.data), HashAlg: "sha256", VerificationClass: state.VerificationContent, VerifiedAt: &verified, Status: state.PlacementActive}
			if mf.medium == state.MediumLocal {
				if err := os.MkdirAll(f.localDir, 0o755); err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(f.localDir, mf.name)
				if err := os.WriteFile(p, mf.data, 0o600); err != nil {
					t.Fatal(err)
				}
				pu.Location = p
				tr.LocalPath = &p
			} else {
				key := "prefix/" + mf.name
				f.store.mu.Lock()
				f.store.objects[key] = mf.data
				f.store.mu.Unlock()
				pu.Location = key
			}
			tr.Placement = &pu
		}
		if _, err := f.b.journal.RecordTransition(ctx, tr); err != nil {
			t.Fatalf("seeding %s (#%d): %v", mf.name, i, err)
		}
	}
	return ids
}

func manyData(seed int, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + seed*31)
	}
	return b
}

func (f *recoveryFixture) stagingRoot() string {
	return filepath.Join(f.dir, "state", "recovery-staging")
}

// manifestFile mirrors what the manifest on disk says.
type manifestFile struct {
	ArtifactID string `json:"artifact_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Copy       string `json:"copy"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256"`
	File       string `json:"file"`
	Code       string `json:"code"`
}

type manifestDoc struct {
	Version     int            `json:"version"`
	OperationID string         `json:"operation_id"`
	Set         string         `json:"set"`
	Outcome     string         `json:"outcome"`
	Files       []manifestFile `json:"files"`
}

func readManifest(t *testing.T, path string) manifestDoc {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("manifest not readable: %v", err)
	}
	var m manifestDoc
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest is not JSON: %v\n%s", err, raw)
	}
	return m
}

func itemByName(t *testing.T, items []RecoverManyItem, name string) RecoverManyItem {
	t.Helper()
	for _, it := range items {
		if it.Name == name {
			return it
		}
	}
	t.Fatalf("no item named %s in %+v", name, items)
	return RecoverManyItem{}
}

func setReq(to string) RecoverManyRequest {
	return RecoverManyRequest{Set: "production/postgres", Target: stagingTo(to)}
}

// leftovers is every regular file under the staging root that is not a
// finished output, i.e. a half-written one.
func (f *recoveryFixture) partials(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, p := range f.stagingEntries(t) {
		if strings.HasSuffix(p, ".partial") {
			out = append(out, p)
		}
	}
	return out
}

func TestRecoverSetBringsBackEveryKnownGoodFileAndListsTheRest(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	d1, d2, d3 := manyData(1, 70_000), manyData(2, 90_000), manyData(3, 50_000)
	f.seedMany(t,
		manyFile{name: "a.zst", data: d1, age: 72 * time.Hour},
		manyFile{name: "b.zst", data: d2, age: 48 * time.Hour, state: lifecycle.Committed},
		manyFile{name: "c.zst", data: d3, age: 24 * time.Hour, state: lifecycle.RemoteRetained},
		manyFile{name: "q.zst", age: 20 * time.Hour, state: lifecycle.Quarantined},
		manyFile{name: "lost.zst", age: 19 * time.Hour, state: lifecycle.QuarantinedLost},
		manyFile{name: "bad.zst", age: 18 * time.Hour, state: lifecycle.Failed},
	)
	res, err := f.b.RecoverMany(context.Background(), f.session(), setReq("restore/run1"), nil)
	if err != nil {
		t.Fatalf("RecoverMany: %v", err)
	}
	if res.Totals.Recovered != 3 || res.Totals.Quarantined != 1 || res.Totals.Unrecoverable != 1 || res.Totals.Skipped != 2 || res.Totals.Failed != 0 {
		t.Fatalf("totals = %+v", res.Totals)
	}
	for name, want := range map[string][]byte{"a.zst": d1, "b.zst": d2, "c.zst": d3} {
		it := itemByName(t, res.Items, name)
		if it.Status != RecoverManyRecovered || it.SHA256 != sha(want) || it.Bytes != int64(len(want)) {
			t.Fatalf("%s = %+v", name, it)
		}
		got, rerr := os.ReadFile(it.Path)
		if rerr != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s on disk wrong: %v", name, rerr)
		}
		// One per-set folder under the one destination name.
		rel, _ := filepath.Rel(f.stagingRoot(), it.Path)
		if !strings.HasPrefix(filepath.ToSlash(rel), "restore/run1/production/postgres/") {
			t.Fatalf("%s landed at %s, want it under restore/run1/production/postgres/", name, rel)
		}
	}
	if q := itemByName(t, res.Items, "q.zst"); q.Status != RecoverManyQuarantined || q.Path != "" {
		t.Fatalf("quarantined row = %+v", q)
	}
	if l := itemByName(t, res.Items, "lost.zst"); l.Status != RecoverManyUnrecoverable {
		t.Fatalf("lost row = %+v", l)
	}
	// The fixture's own dump.zst is still transferring, and bad.zst failed:
	// neither is a restore point, both are listed and neither is read.
	for _, name := range []string{"bad.zst", "dump.zst"} {
		if s := itemByName(t, res.Items, name); s.Status != RecoverManySkipped || s.Path != "" {
			t.Fatalf("%s = %+v", name, s)
		}
	}
	// Exactly the three recovered files and the manifest exist: nothing for
	// the rows that were listed and not included.
	if n := len(f.stagingEntries(t)); n != 4 {
		t.Fatalf("staging holds %d files (%v), want 3 recovered + 1 manifest", n, f.stagingEntries(t))
	}
}

func TestRecoverSetManifestAgreesWithTheResultAndTheDisk(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	d1, d2 := manyData(4, 40_000), manyData(5, 60_000)
	f.seedMany(t,
		manyFile{name: "a.zst", data: d1, age: 3 * time.Hour},
		manyFile{name: "b.zst", data: d2, age: 2 * time.Hour},
		manyFile{name: "q.zst", age: time.Hour, state: lifecycle.Quarantined},
	)
	res, err := f.b.RecoverMany(context.Background(), f.session(), setReq("out"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ManifestPath == "" {
		t.Fatal("no manifest path in the result")
	}
	m := readManifest(t, res.ManifestPath)
	if m.Version != 1 || m.Set != "production/postgres" || m.Outcome != "completed" || m.OperationID != res.OperationID || m.OperationID == "" {
		t.Fatalf("manifest header = %+v", m)
	}
	if len(m.Files) != len(res.Items) {
		t.Fatalf("manifest lists %d files, the result %d", len(m.Files), len(res.Items))
	}
	for _, it := range res.Items {
		var mf *manifestFile
		for i := range m.Files {
			if m.Files[i].ArtifactID == it.ArtifactID {
				mf = &m.Files[i]
			}
		}
		if mf == nil {
			t.Fatalf("%s missing from the manifest", it.ArtifactID)
		}
		if mf.Status != it.Status || mf.SHA256 != it.SHA256 || mf.Bytes != it.Bytes || mf.Code != it.Code {
			t.Fatalf("manifest %+v disagrees with result %+v", *mf, it)
		}
		if it.Status != RecoverManyRecovered {
			if mf.File != "" {
				t.Fatalf("manifest names a file for %s which was not recovered: %+v", it.Name, *mf)
			}
			continue
		}
		onDisk, rerr := os.ReadFile(filepath.Join(f.stagingRoot(), filepath.FromSlash(mf.File)))
		if rerr != nil {
			t.Fatalf("manifest names %s, which is not on disk: %v", mf.File, rerr)
		}
		if sha(onDisk) != mf.SHA256 {
			t.Fatalf("%s on disk hashes to %s, the manifest says %s", mf.File, sha(onDisk), mf.SHA256)
		}
	}
}

func TestRecoverSetNewestOnlyAndSinceNarrowTheSet(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	f.seedMany(t,
		manyFile{name: "old.zst", data: manyData(1, 1000), age: 10 * 24 * time.Hour},
		manyFile{name: "mid.zst", data: manyData(2, 1000), age: 3 * 24 * time.Hour},
		manyFile{name: "new.zst", data: manyData(3, 1000), age: time.Hour},
		manyFile{name: "q.zst", age: 30 * time.Minute, state: lifecycle.Quarantined},
	)
	req := setReq("n")
	req.NewestOnly = true
	res, err := f.b.RecoverMany(context.Background(), f.session(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Totals.Recovered != 1 || itemByName(t, res.Items, "new.zst").Status != RecoverManyRecovered {
		t.Fatalf("newest-only recovered %+v", res.Totals)
	}
	for _, it := range res.Items {
		if it.Name == "old.zst" || it.Name == "mid.zst" {
			t.Fatalf("newest-only listed %s", it.Name)
		}
	}

	f2 := newRecoveryFixture(t, fixtureOpts{})
	f2.seedMany(t,
		manyFile{name: "old.zst", data: manyData(1, 1000), age: 10 * 24 * time.Hour},
		manyFile{name: "mid.zst", data: manyData(2, 1000), age: 3 * 24 * time.Hour},
		manyFile{name: "new.zst", data: manyData(3, 1000), age: time.Hour},
	)
	req = setReq("s")
	req.Since = time.Now().Add(-5 * 24 * time.Hour)
	res, err = f2.b.RecoverMany(context.Background(), f2.session(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Totals.Recovered != 2 {
		t.Fatalf("since recovered %+v", res.Totals)
	}
}

func TestRecoverManyByIdMatchesRecoveringEachOneAlone(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	ids := f.seedMany(t,
		manyFile{name: "a.zst", data: manyData(1, 33_333), age: 3 * time.Hour},
		manyFile{name: "b.zst", data: manyData(2, 44_444), age: 2 * time.Hour},
	)
	res, err := f.b.RecoverMany(context.Background(), f.session(), RecoverManyRequest{Artifacts: ids, Target: stagingTo("sel")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Totals.Recovered != 2 {
		t.Fatalf("totals = %+v", res.Totals)
	}
	for _, id := range ids {
		one, _, oerr := f.b.RecoverNow(context.Background(), f.session(), RecoveryRequest{ArtifactID: id, Target: stagingTo("alone")}, nil)
		if oerr != nil {
			t.Fatal(oerr)
		}
		var got RecoverManyItem
		for _, it := range res.Items {
			if it.ArtifactID == id {
				got = it
			}
		}
		if got.SHA256 != one.SHA256 || got.Bytes != one.Bytes || got.Copy != one.Copy {
			t.Fatalf("%s: many = %+v, alone = %+v", id, got, one)
		}
	}
}

func TestRecoverManyOneCorruptCopyFailsAloneAndLeavesNoFile(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	d1, d2, d3 := manyData(1, 64_000), manyData(2, 64_000), manyData(3, 64_000)
	f.seedMany(t,
		manyFile{name: "a.zst", data: d1, age: 3 * time.Hour},
		manyFile{name: "b.zst", data: d2, age: 2 * time.Hour},
		manyFile{name: "c.zst", data: d3, age: time.Hour},
	)
	// Flip one byte of b's local copy after the journal recorded its digest.
	bp := filepath.Join(f.localDir, "b.zst")
	raw, _ := os.ReadFile(bp)
	raw[1000] ^= 0xff
	if err := os.WriteFile(bp, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := f.b.RecoverMany(context.Background(), f.session(), setReq("c"), nil)
	if err != nil {
		t.Fatalf("one bad copy must not fail the run: %v", err)
	}
	if res.Totals.Recovered != 2 || res.Totals.Failed != 1 {
		t.Fatalf("totals = %+v", res.Totals)
	}
	bad := itemByName(t, res.Items, "b.zst")
	if bad.Status != RecoverManyFailed || bad.Code != RecoveryLocalCopyCorrupt || bad.Path != "" {
		t.Fatalf("corrupt copy = %+v", bad)
	}
	for _, p := range f.stagingEntries(t) {
		if strings.HasSuffix(p, "-b.zst") {
			t.Fatalf("a file for the corrupt copy reached the destination: %s", p)
		}
		if got, _ := os.ReadFile(p); bytes.Equal(got, raw) {
			t.Fatalf("the corrupt bytes were delivered at %s", p)
		}
	}
	if p := f.partials(t); len(p) != 0 {
		t.Fatalf("half-written files left: %v", p)
	}
	m := readManifest(t, res.ManifestPath)
	for _, mf := range m.Files {
		if mf.Name == "b.zst" && (mf.Status != RecoverManyFailed || mf.File != "") {
			t.Fatalf("manifest entry for the corrupt copy = %+v", mf)
		}
	}
}

func TestRecoverSetPreflightRefusesWithTheNumbersAndReadsNothing(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	f.seedMany(t,
		manyFile{name: "a.zst", data: manyData(1, 100_000), age: 3 * time.Hour, medium: "warm"},
		manyFile{name: "b.zst", data: manyData(2, 100_000), age: 2 * time.Hour, medium: "warm"},
	)
	h := f.b.recoveryHost()
	h.staging.FreeBytes = func(string) (int64, error) { return 150_000, nil }
	req := setReq("tight")
	req.AcknowledgeCost = true
	_, err := f.b.RecoverMany(context.Background(), f.session(), req, nil)
	if err == nil {
		t.Fatal("a set that cannot fit was accepted")
	}
	code := codeOf(t, err)
	if code != RecoveryNoSpace && code != RecoveryBelowReserve {
		t.Fatalf("code = %s", code)
	}
	for _, want := range []string{"200000", "150000"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not carry %s", err.Error(), want)
		}
	}
	if n := f.store.opens.Load(); n != 0 {
		t.Fatalf("the refusal came after %d reads", n)
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("staging not empty after a refusal: %v", e)
	}
	plan, perr := f.b.PlanRecoverMany(context.Background(), req)
	if perr == nil || plan.Totals.Ready != 2 || plan.Totals.Bytes != 200_000 || plan.FreeBytes == nil || *plan.FreeBytes != 150_000 {
		t.Fatalf("plan alongside the refusal = %+v, %v", plan, perr)
	}
}

func TestRecoverSetShowsTheWholeCostBeforeByteOneAndNeedsOneAcknowledgement(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	f.seedMany(t,
		manyFile{name: "a.zst", data: manyData(1, 10_000), age: 3 * time.Hour, medium: "warm"},
		manyFile{name: "b.zst", data: manyData(2, 20_000), age: 2 * time.Hour, medium: "warm"},
	)
	plan, err := f.b.PlanRecoverMany(context.Background(), setReq("x"))
	if re, ok := AsRecoveryError(err); !ok || re.Code != RecoveryCostNotAcknowledged {
		t.Fatalf("plan error = %v", err)
	}
	if plan.ChargeableCount != 2 || plan.ChargeableBytes != 30_000 {
		t.Fatalf("plan cost = %d files, %d bytes", plan.ChargeableCount, plan.ChargeableBytes)
	}
	_, err = f.b.RecoverMany(context.Background(), f.session(), setReq("x"), nil)
	if got := codeOf(t, err); got != RecoveryCostNotAcknowledged {
		t.Fatalf("code = %s", got)
	}
	if !strings.Contains(err.Error(), "2 ") || !strings.Contains(err.Error(), "30000") {
		t.Fatalf("the refusal does not state the whole set's cost: %v", err)
	}
	if f.store.opens.Load() != 0 || len(f.stagingEntries(t)) != 0 {
		t.Fatal("something was read or written before the cost was acknowledged")
	}
	req := setReq("x")
	req.AcknowledgeCost = true
	res, err := f.b.RecoverMany(context.Background(), f.session(), req, nil)
	if err != nil || res.Totals.Recovered != 2 {
		t.Fatalf("acknowledged run: %v, %+v", err, res.Totals)
	}
}

func TestRecoverSetOnAnArchiveClassStartsThawsAndRecoversNothing(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	f.seedMany(t,
		manyFile{name: "a.zst", data: manyData(1, 10_000), age: 3 * time.Hour, medium: "cold"},
		manyFile{name: "b.zst", data: manyData(2, 10_000), age: 2 * time.Hour, medium: "cold"},
	)
	var thaws atomic.Int32
	f.b.recoveryHost().thaw = func(_ context.Context, _, id, medium string) (RecoveryThaw, error) {
		thaws.Add(1)
		return RecoveryThaw{OperationID: "op-" + id, Wait: "hours", Billing: "billed"}, nil
	}
	req := setReq("cold")
	req.AcknowledgeCost = true
	res, err := f.b.RecoverMany(context.Background(), f.session(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Totals.NeedsThaw != 2 || res.Totals.Recovered != 0 || thaws.Load() != 2 {
		t.Fatalf("totals = %+v, thaws = %d", res.Totals, thaws.Load())
	}
	if it := itemByName(t, res.Items, "a.zst"); it.Status != RecoverManyNeedsThaw || it.Thaw == nil {
		t.Fatalf("item = %+v", it)
	}
	// Without the acknowledgement no thaw starts at all.
	f2 := newRecoveryFixture(t, fixtureOpts{})
	f2.seedMany(t, manyFile{name: "a.zst", data: manyData(1, 10_000), medium: "cold"})
	var thaws2 atomic.Int32
	f2.b.recoveryHost().thaw = func(context.Context, string, string, string) (RecoveryThaw, error) {
		thaws2.Add(1)
		return RecoveryThaw{}, nil
	}
	if _, err := f2.b.RecoverMany(context.Background(), f2.session(), setReq("cold"), nil); codeOf(t, err) != RecoveryCostNotAcknowledged || thaws2.Load() != 0 {
		t.Fatalf("an unacknowledged archive recovery: %v, thaws = %d", err, thaws2.Load())
	}
}

func TestRecoverManyCancelStopsTheRunAndLeavesNothingHalfWritten(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	f.seedMany(t,
		manyFile{name: "a.zst", data: manyData(1, 40_000), age: 4 * time.Hour},
		manyFile{name: "b.zst", data: manyData(2, 40_000), age: 3 * time.Hour},
		manyFile{name: "c.zst", data: manyData(3, 40_000), age: 2 * time.Hour},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seen []string
	res, err := f.b.RecoverMany(ctx, f.session(), setReq("cx"), func(p RecoverManyProgress) {
		seen = append(seen, p.Kind+":"+p.Item.Name)
		if p.Kind == "item" && p.Item.Name == "a.zst" {
			cancel()
		}
	})
	mustBeInterrupted(t, "the run", err)
	if !res.Cancelled || res.Totals.Recovered != 1 {
		t.Fatalf("result = %+v (events %v)", res.Totals, seen)
	}
	if p := f.partials(t); len(p) != 0 {
		t.Fatalf("a half-written file was left: %v", p)
	}
	for _, name := range []string{"b.zst", "c.zst"} {
		if it := itemByName(t, res.Items, name); it.Status != RecoverManySkipped || it.Code != RecoveryInterrupted || it.Path != "" {
			t.Fatalf("%s = %+v", name, it)
		}
	}
	// The manifest still says what happened and agrees with the disk.
	m := readManifest(t, res.ManifestPath)
	if m.Outcome != "cancelled" {
		t.Fatalf("outcome = %q", m.Outcome)
	}
	recovered := 0
	for _, mf := range m.Files {
		if mf.Status == RecoverManyRecovered {
			recovered++
		}
	}
	if recovered != 1 || len(f.stagingEntries(t)) != 2 {
		t.Fatalf("manifest says %d recovered, disk holds %v", recovered, f.stagingEntries(t))
	}
}

func TestRecoverManyCancelDuringARemoteReadLeavesNoFile(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	f.seedMany(t, manyFile{name: "a.zst", data: manyData(1, 40_000), medium: "warm"})
	f.store.openGate = make(chan struct{}) // the read is in flight until cancelled
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	req := setReq("rx")
	req.AcknowledgeCost = true
	res, err := f.b.RecoverMany(ctx, f.session(), req, nil)
	mustBeInterrupted(t, "the remote read", err)
	if res.Totals.Recovered != 0 {
		t.Fatalf("totals = %+v", res.Totals)
	}
	if p := f.partials(t); len(p) != 0 {
		t.Fatalf("partial left: %v", p)
	}
	for _, p := range f.stagingEntries(t) {
		if !strings.Contains(p, "manifest") {
			t.Fatalf("a file that looks finished exists after a cancel: %s", p)
		}
	}
}

func TestRecoverManyRefusals(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	ids := f.seedMany(t, manyFile{name: "a.zst", data: manyData(1, 1000)})
	ctx := context.Background()

	_, err := f.b.RecoverMany(ctx, f.session(), RecoverManyRequest{Set: "production/postgres", Target: RecoveryTarget{Kind: "download"}}, nil)
	if codeOf(t, err) != RecoveryTargetInvalid || !strings.Contains(err.Error(), "staging") {
		t.Fatalf("download target: %v", err)
	}
	_, err = f.b.RecoverMany(ctx, f.session(), RecoverManyRequest{Set: "production/postgres", Artifacts: ids, Target: stagingTo("x")}, nil)
	if err == nil {
		t.Fatal("both a set and a list were accepted")
	}
	_, err = f.b.RecoverMany(ctx, f.session(), RecoverManyRequest{Target: stagingTo("x")}, nil)
	if err == nil {
		t.Fatal("neither a set nor a list was accepted")
	}
	_, err = f.b.RecoverMany(ctx, f.session(), RecoverManyRequest{Set: "production/postgres", Target: stagingTo("../escape")}, nil)
	if codeOf(t, err) != RecoveryTargetInvalid {
		t.Fatalf("escaping target: %v", err)
	}
	_, err = f.b.RecoverMany(ctx, f.session(), RecoverManyRequest{Artifacts: []string{"production/postgres/nope.zst"}, Target: stagingTo("x")}, nil)
	if !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("unknown artifact: %v", err)
	}
	_, err = f.b.RecoverMany(ctx, f.session(), RecoverManyRequest{Set: "production/missing", Target: stagingTo("x")}, nil)
	if !errors.Is(err, ErrBackupSetNotFound) {
		t.Fatalf("unknown set: %v", err)
	}
	if e := f.stagingEntries(t); len(e) != 0 {
		t.Fatalf("a refusal left files: %v", e)
	}
}

func TestRecoverSetOnAnIncrementalEngineSetNamesSnapshotRestore(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{engine: "kopia"})
	_, err := f.b.RecoverMany(context.Background(), f.session(), setReq("k"), nil)
	if codeOf(t, err) != RecoveryUseSnapshotRestore || !strings.Contains(err.Error(), "snapshot restore") {
		t.Fatalf("kopia set: %v", err)
	}
}

func TestRecoverManyToAStorageDestinationWritesFilesAndManifestUnderThePrefix(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	d1, d2 := manyData(1, 20_000), manyData(2, 30_000)
	f.seedMany(t,
		manyFile{name: "a.zst", data: d1, age: 3 * time.Hour},
		manyFile{name: "b.zst", data: d2, age: 2 * time.Hour},
	)
	req := RecoverManyRequest{Set: "production/postgres", Target: RecoveryTarget{Kind: "storage", Medium: "other", Key: "back/run1"}}
	res, err := f.b.RecoverMany(context.Background(), f.session(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Totals.Recovered != 2 || res.ManifestKey == "" {
		t.Fatalf("result = %+v", res)
	}
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if got := f.store.objects["back/run1/production/postgres/a.zst"]; !bytes.Equal(got, d1) {
		t.Fatalf("a.zst object wrong (%d bytes); keys = %v", len(got), keysOf(f.store.objects))
	}
	if got := f.store.objects["back/run1/production/postgres/b.zst"]; !bytes.Equal(got, d2) {
		t.Fatalf("b.zst object wrong")
	}
	if _, ok := f.store.objects[res.ManifestKey]; !ok || !strings.HasPrefix(res.ManifestKey, "back/run1/production/postgres/") {
		t.Fatalf("manifest key %q not among %v", res.ManifestKey, keysOf(f.store.objects))
	}
}

func keysOf(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestRecoverManyReportsOneEventPerFileAndARunningTotal(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	f.seedMany(t,
		manyFile{name: "a.zst", data: manyData(1, 10_000), age: 3 * time.Hour},
		manyFile{name: "b.zst", data: manyData(2, 25_000), age: 2 * time.Hour},
	)
	var events []RecoverManyProgress
	_, err := f.b.RecoverMany(context.Background(), f.session(), setReq("p"), func(p RecoverManyProgress) { events = append(events, p) })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Kind != "plan" || events[0].Plan == nil || events[0].Plan.Totals.Ready != 2 {
		t.Fatalf("first event = %+v", events)
	}
	var items []RecoverManyProgress
	for _, e := range events {
		if e.Kind == "item" {
			items = append(items, e)
		}
	}
	if len(items) != 2 || items[0].Index != 1 || items[1].Index != 2 || items[1].Total != 2 {
		t.Fatalf("item events = %+v", items)
	}
	if items[0].DoneBytes != 10_000 || items[1].DoneBytes != 35_000 || items[1].TotalBytes != 35_000 {
		t.Fatalf("running totals = %d, %d of %d", items[0].DoneBytes, items[1].DoneBytes, items[1].TotalBytes)
	}
}

func TestRecoverManyWritesOneAuditRowNamingTheActorAndTheTotals(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	f.seedMany(t, manyFile{name: "a.zst", data: manyData(1, 5000)})
	if _, err := f.b.RecoverMany(context.Background(), f.session(), setReq("au"), nil); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, r := range f.rows(t) {
		if r["operation"] == "recovery many" {
			found++
			if r["actor"] != "alice" || r["route"] != "POST /api/v1/recoveries" || r["outcome"] != "ok" {
				t.Fatalf("row = %v", r)
			}
		}
	}
	if found != 1 {
		t.Fatalf("%d summary rows", found)
	}
}

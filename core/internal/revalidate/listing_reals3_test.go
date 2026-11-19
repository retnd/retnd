package revalidate

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/retnd/retnd/core/internal/state"
	"github.com/retnd/retnd/core/internal/transport"
	"github.com/retnd/retnd/core/internal/transport/rclone"
	"github.com/retnd/retnd/core/tests/reals3"
)

// EPIC Q, QC-04 on #978, against REAL AWS S3. Everything else in this package
// (and in miniointegration) uses a fake or MinIO; none of it can say whether
// real S3 spells keys, pages listings and reports a missing bucket the way
// listing.go assumes. These cases use the real adapter the product uses
// (rclone.New(), the s3 backend) against a real bucket.
//
// They skip unless RETND_TEST_S3_REAL=1 and AWS credentials are in the
// environment (see core/tests/reals3), so CI and a normal run never reach AWS.

// realMediums resolves the one medium "offsite" to a real bucket.
type realMediums struct{ medium transport.Medium }

func (m realMediums) MediumFor(id string) (transport.Medium, bool) {
	if id != m.medium.ID {
		return transport.Medium{}, false
	}
	return m.medium, true
}

func runRealListing(t *testing.T, med transport.Medium, content []byte, name string) (Report, func() state.Placement) {
	t.Helper()
	j, a := onMedium(t, name, content)
	rep, err := Run(context.Background(), Deps{Journal: j, Store: rclone.New(), Mediums: realMediums{medium: med}}, a.Set, scrubCfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep, func() state.Placement { return mediumCopy(t, j, a) }
}

// The key the journal holds for onMedium's artifact is
// retnd/production/postgres-primary/<name>; with the medium prefix "retnd"
// that is exactly what lives in the bucket, rooted at the bucket (no leading
// slash, no bucket name, no prefix doubled).
func realKnownKey(name string) string { return setNamespace + "/" + name }

func TestRealS3_ListingKeysAreBucketRootedAndTheNamespacePrefixScopesThem(t *testing.T) {
	env := reals3.Require(t)
	bucket := env.NewBucket(t)
	content := []byte("the bytes that really moved to AWS")
	const name = "kept.dump"

	bucket.Put(t, realKnownKey(name), content)
	odd := setNamespace + "/odd name+plus%20pct é/ünï.bin" // space, +, %, non-ASCII, nested
	strays := []string{
		setNamespace + "/stray.bin",
		setNamespace + "/nested/deeper/stray2.bin",
		odd,
	}
	for _, k := range strays {
		bucket.Put(t, k, []byte("stray"))
	}
	bucket.Put(t, setNamespace+"/dir-marker/", nil)                          // a console-made "folder": not an object to report
	bucket.Put(t, "retnd/production/other-set/x.db", []byte("o"))            // another set
	bucket.Put(t, "retnd/production/postgres-primary-2/y.db", []byte("p"))   // shares the set's name as a string prefix only
	bucket.Put(t, "elsewhere/production/postgres-primary/z.db", []byte("q")) // outside the medium prefix

	// Positive control for the dir-marker object: the bucket really holds it,
	// and the real adapter does not hand it to the scrub as an object (it
	// presents it as a directory), so listing.go's trailing-slash guard is
	// belt and braces against real S3 rather than the thing that hides it.
	marker := setNamespace + "/dir-marker/"
	if !slices.Contains(bucket.Keys(t), marker) {
		t.Fatalf("the bucket does not hold the folder marker %q: the control is void", marker)
	}
	listed, err := rclone.New().ListObjects(context.Background(), bucket.Medium("offsite", "retnd"), setNamespace)
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	for _, o := range listed {
		if strings.HasSuffix(o.Key, "/") {
			t.Errorf("the adapter listed the folder marker %q as an object", o.Key)
		}
		if !strings.HasPrefix(o.Key, setNamespace+"/") {
			t.Errorf("the adapter listed %q, which is not bucket-rooted under %q", o.Key, setNamespace)
		}
	}

	rep, _ := runRealListing(t, bucket.Medium("offsite", "retnd"), content, name)

	want := make([]string, 0, len(strays))
	for _, k := range strays {
		want = append(want, "offsite|"+k)
	}
	sort.Strings(want)
	if got := unknownKeys(rep); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Unknown =\n%s\nwant exactly\n%s\n(bucket holds %q)", strings.Join(got, "\n"), strings.Join(want, "\n"), bucket.Keys(t))
	}
	for _, u := range rep.Unknown {
		if u.Size != 5 {
			t.Errorf("%q reported with size %d, want 5", u.Key, u.Size)
		}
	}
	if len(rep.Unlisted) != 0 || len(rep.Errors) != 0 {
		t.Errorf("Unlisted %+v, Errors %+v: a healthy bucket lists cleanly", rep.Unlisted, rep.Errors)
	}
	if len(rep.Findings) != 1 || !rep.Findings[0].Passed {
		t.Errorf("Findings = %+v: the journal's own copy is in the bucket at its key and must pass", rep.Findings)
	}
}

// A bucket with more than one page of keys (S3 pages at 1000). The listing
// must see every stray, not the first page.
func TestRealS3_ListingFollowsPaginationPastTheFirstThousandKeys(t *testing.T) {
	env := reals3.Require(t)
	bucket := env.NewBucket(t)
	content := []byte("paged")
	const name = "paged.dump"
	bucket.Put(t, realKnownKey(name), content)

	const strays = 1100
	var wg sync.WaitGroup
	sem := make(chan struct{}, 24)
	for i := 0; i < strays; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := bucket.TryPut(fmt.Sprintf("%s/stray-%04d.bin", setNamespace, i), []byte("s")); err != nil {
				t.Errorf("putting stray %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	rep, _ := runRealListing(t, bucket.Medium("offsite", "retnd"), content, name)
	if len(rep.Unknown) != strays {
		t.Fatalf("Unknown has %d entries, want %d: the listing stopped at a page boundary", len(rep.Unknown), strays)
	}
	if len(rep.Unlisted) != 0 {
		t.Errorf("Unlisted = %+v", rep.Unlisted)
	}
}

func TestRealS3_AnAbsentKnownCopyIsACountedFailureAndAStrayCannotStandInForIt(t *testing.T) {
	env := reals3.Require(t)
	bucket := env.NewBucket(t)
	content := []byte("gone from AWS")
	const name = "vanished.dump"
	// Right name and size, wrong key.
	bucket.Put(t, setNamespace+"/stray/"+name, content)

	rep, copyNow := runRealListing(t, bucket.Medium("offsite", "retnd"), content, name)

	if got := unknownKeys(rep); len(got) != 1 || got[0] != "offsite|"+setNamespace+"/stray/"+name {
		t.Fatalf("Unknown = %v, want the stray reported", got)
	}
	failed := 0
	for _, f := range rep.Findings {
		if f.Passed {
			t.Errorf("Finding %+v passed on the strength of an object at an unknown key", f)
		}
		for _, c := range f.Copies {
			if c.Verdict == state.ScrubFailed {
				failed++
			}
		}
	}
	if failed != 1 {
		t.Errorf("failed copies = %d, want 1 (report %+v)", failed, rep)
	}
	if p := copyNow(); p.AssuranceState != state.AssuranceFailed {
		t.Errorf("the journal records %q for the absent copy, want failed", p.AssuranceState)
	}
}

func TestRealS3_AListingErrorLandsInUnlistedAndIsNotACopyFailure(t *testing.T) {
	env := reals3.Require(t)
	// A bucket name this run never created. Nothing is created here, so there
	// is nothing to tear down; the 8-hex run id makes a collision with a real
	// bucket of someone else's all but impossible.
	missing := transport.Medium{
		ID: "offsite", Type: transport.MediumTypeS3, Region: reals3.Region, Prefix: "retnd",
		Bucket:      fmt.Sprintf("retnd-test-%s-absent", reals3.RunID()),
		Credentials: transport.MediumCredentials{Env: reals3.CredentialsEnv},
	}
	_ = env

	rep, _ := runRealListing(t, missing, []byte("nowhere"), "nowhere.dump")

	if len(rep.Unlisted) != 1 || rep.Unlisted[0].Medium != "offsite" || rep.Unlisted[0].Reason == "" {
		t.Fatalf("Unlisted = %+v, want offsite with a reason", rep.Unlisted)
	}
	t.Logf("real S3 reason for a missing bucket: %q", rep.Unlisted[0].Reason)
	if len(rep.Unknown) != 0 {
		t.Errorf("Unknown = %v: a listing that failed proves nothing about what is there", unknownKeys(rep))
	}
}

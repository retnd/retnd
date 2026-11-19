package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/obs"
	"github.com/retnd/retnd/core/internal/state"
)

// EPIC T, T1.4 (#1314): the build default moving is announced once, on the
// first cycle under the new build, and never again on a steady one. It
// rotates nothing (ADR 0033 D4).
func TestTheDefaultSuiteChangeEventFiresOnceAndOnlyOnAChange(t *testing.T) {
	ctx := context.Background()
	j := openJournal(t)
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	// An EPIC N deployment: it has held a key, under EPIC N's implicit
	// default, before this build first ran.
	if err := j.AddEncryptionKey(ctx, state.EncryptionKey{ID: "k1", Provider: state.KeyProviderManaged, CreatedAt: at, SuiteID: copycrypt.AgeX25519ID}, ""); err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	svc := &Service{Journal: j, Logger: obs.New(&stream, obs.LevelInfo)}

	count := func() int { return strings.Count(stream.String(), obs.EventEncryptionDefaultSuiteChanged) }

	svc.noteDefaultSuite(ctx, at)
	if count() != 1 {
		t.Fatalf("the first cycle under a build whose default is %s logged the change %d time(s), want once:\n%s", copycrypt.Default().ID(), count(), stream.String())
	}
	for _, want := range []string{`"previous_suite":"age/v1"`, `"suite":"` + copycrypt.Default().ID() + `"`} {
		if !strings.Contains(stream.String(), want) {
			t.Errorf("the event does not carry %s:\n%s", want, stream.String())
		}
	}
	for i := 0; i < 3; i++ {
		svc.noteDefaultSuite(ctx, at.Add(time.Duration(i+1)*time.Hour))
	}
	if count() != 1 {
		t.Fatalf("a steady build logged the change %d time(s) in total, want exactly the first one", count())
	}
	// Nothing was rotated by it.
	keys, err := j.EncryptionKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0].Status != state.EncryptionKeyActive {
		t.Fatalf("keys after the announcement = %+v, %v; the default moving mints and supersedes nothing", keys, err)
	}
}

// A deployment that never held a key has nothing to be told.
func TestAFreshDeploymentIsNotToldTheDefaultMoved(t *testing.T) {
	var stream bytes.Buffer
	svc := &Service{Journal: openJournal(t), Logger: obs.New(&stream, obs.LevelInfo)}
	svc.noteDefaultSuite(context.Background(), time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC))
	if strings.Contains(stream.String(), obs.EventEncryptionDefaultSuiteChanged) {
		t.Fatalf("a deployment with no key history was told the default moved:\n%s", stream.String())
	}
}

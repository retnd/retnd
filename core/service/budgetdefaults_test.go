package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/retnd/retnd/core/internal/config"
)

// Panel finding C10, row Q1.15: the budget tests set the limits by hand
// (f.b.recovery.limits = ...), which proves a gate refuses past a line it was
// given and says nothing about the line a deployment that never touched
// `assurance:` is given. These leave the limits alone: the fixture is a
// default configuration, the ceilings are whatever the product derives from
// it, and a real recovery is refused at the numbers Settings prints (two full
// reads at once, one per destination).
func TestADefaultDeploymentRefusesARecoveryPastTheCeilingsSettingsPrints(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{remoteClass: "warm"})
	h := f.b.recoveryHost()
	if h.unavailable {
		t.Fatalf("recovery is unavailable on a default deployment: %s", h.unavailableWhy)
	}
	ctx := context.Background()
	recover := func() error {
		_, _, err := f.b.RecoverNow(ctx, f.session(),
			RecoveryRequest{ArtifactID: f.artifact, Target: stagingTo("x"), AcknowledgeCost: true}, nil)
		return err
	}

	// Fill every global slot with reads of other destinations.
	var holds []func()
	for i := 0; i < config.DefaultMaxConcurrentReads; i++ {
		hold, err := h.reads.Acquire(fmt.Sprintf("other-%d", i))
		if err != nil {
			t.Fatalf("read %d was refused under the default ceiling of %d: %v", i+1, config.DefaultMaxConcurrentReads, err)
		}
		holds = append(holds, hold)
	}
	if code := codeOf(t, recover()); code != RecoveryReadsBusy {
		t.Errorf("a recovery past the default global ceiling = %s, want %s", code, RecoveryReadsBusy)
	}
	if f.store.opens.Load() != 0 {
		t.Error("the default ceiling refused the recovery after the copy was already read")
	}
	for _, release := range holds {
		release()
	}

	// And the per-destination ceiling, which is the smaller number.
	hold, err := h.reads.Acquire("warm")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	if code := codeOf(t, recover()); code != RecoveryDestinationBusy {
		t.Errorf("a second read of one destination = %s, want %s (the default is %d per destination)", code, RecoveryDestinationBusy, config.DefaultMaxPerDestination)
	}
}

package service

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/retnd/retnd/core/internal/keycustody"
)

// #993: the custodian service.New wires into app.Service.Keys. Off is
// byte-identical, so reading it creates nothing, and an engine that is
// wired can find the active key a copy is encrypted under.

func TestAnEngineBuiltByNewHasKeyCustodyWired_993(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	if f.b.state.Load().inner.Keys == nil {
		t.Fatal("app.Service.Keys is nil: an encrypted copy would be refused at the copy phase for want of a custodian")
	}
	if f.b.state.Load().inner.Keys != any(f.b.keys) {
		t.Error("the engine and the encryption routes hold different custodians")
	}
}

func TestAConfigReloadKeepsKeyCustody_993(t *testing.T) {
	svc, _ := openTestService(t)
	before := svc.state.Load().inner.Keys
	if before == nil {
		t.Fatal("a service from Open has no key custody")
	}
	if _, err := svc.ResetEncryptionPolicy(context.Background()); err != nil {
		t.Fatalf("a write that reloads the configuration: %v", err)
	}
	if svc.state.Load().inner.Keys != before {
		t.Error("a hot reload dropped key custody, so every encrypted copy would be refused until a restart")
	}
}

func TestReadingKeyCustodyOfADeploymentThatEncryptsNothingCreatesNothing_993(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	dir := fileBacked(t, f)
	ctx := context.Background()

	if _, ok, err := f.b.keys.Active(ctx); err != nil || ok {
		t.Fatalf("Active() = %v, %v on a deployment with no key", ok, err)
	}
	h, err := f.b.keys.Health(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if h.Enabled || h.ActiveKeyID != "" || h.Degraded() {
		t.Errorf("the section for a deployment that encrypts nothing is %+v, want the zero value", h)
	}
	if _, err := os.Stat(keycustody.DirBeside(f.b.configPath)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reading custody created the key directory beside the config (%v); off must write what it always wrote", err)
	}
	_ = dir
}

func TestAKeyWithoutAConfirmedBundleIsADurableDegradedStateForARequiredSet_993(t *testing.T) {
	f := newRecoveryFixture(t, fixtureOpts{})
	fileBacked(t, f)
	ctx := context.Background()
	k, err := f.b.CreateEncryptionKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.b.keys.Health(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Degraded() || h.ActiveKeyID != k.ID {
		t.Errorf("a required deployment with an unconfirmed key reads %+v, want degraded", h)
	}
	if _, err := f.b.ExportRecoveryBundleBytes(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.ConfirmRecoveryBundle(ctx, k.ID, ""); err != nil {
		t.Fatal(err)
	}
	if h, _ = f.b.keys.Health(ctx, true); h.Degraded() {
		t.Errorf("after confirming, the section still reads %+v", h)
	}
}

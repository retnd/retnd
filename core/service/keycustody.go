package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/config"
	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/keycustody"
	"github.com/retnd/retnd/core/internal/secretref"
	"github.com/retnd/retnd/core/internal/state"
)

// KeyCustody is what app.Service.Keys holds in a real deployment (#993).
//
// Before this, nothing assigned that field outside a test, so an encrypted
// copy was refused at the copy phase for want of a custodian, and `retnd
// scrub --content` could not decrypt-verify anything. Both doors build their
// own app.Service (service.New for the engine and its hot reloads, the CLI's
// openService for run, daemon and scrub), so this is a type both can hand it
// rather than a line in one constructor.
//
// It opens the managed key directory on first use and not before. Opening
// creates the directory, and a deployment that never mentions
// retained_copy_encryption must keep writing exactly what it wrote before
// this epic (the "off is byte identical" invariant), which includes not
// growing a directory beside config.yaml, and must keep working on a
// configuration mount that is read-only. The reads that need no key file
// (the registry, the active key, health) go to the registry and open nothing.
//
// The path is a func because BackupService learns its configuration path
// after New returns.
type KeyCustody struct {
	path    func() string
	journal *state.Journal
	format  keycustody.Format

	// config is the running configuration, read on every resolution so a hot
	// reload is seen. It is how an external key's registry row (id and
	// recipient only) becomes material again: the references are config's.
	// Nil means none are declared.
	config func() *config.Config

	mu    sync.Mutex
	store *keycustody.Store

	// probed is the last external key probe and probeAt when it ran (#1304).
	// probedRefs is the declared references that probe saw, so a reload that
	// drops or adds one is not answered from a probe of the old list.
	probeMu    sync.Mutex
	probed     []health.ExternalKeyAtRisk
	probeAt    time.Time
	probedRefs string
}

// UseConfig tells custody where to find the declared external references
// (#1292). It must be set before the first key is resolved.
func (c *KeyCustody) UseConfig(fn func() *config.Config) { c.config = fn }

// externalRefs is every external reference the running configuration
// declares: the global provider and each set's resolved one. A reference
// that is dropped from config stops resolving, and with it every copy
// recorded under the key it yielded, which is why the docs say to keep a
// superseded key's reference declared.
func (c *KeyCustody) externalRefs() []secretref.Ref {
	if c.config == nil {
		return nil
	}
	cfg := c.config()
	if cfg == nil {
		return nil
	}
	var refs []secretref.Ref
	add := func(p config.RetainedCopyKeyProvider) {
		if r := p.ExternalRef(); !r.IsZero() {
			refs = append(refs, r)
		}
	}
	add(cfg.RetainedCopyEncryption.KeyProvider)
	for _, src := range cfg.Sources {
		for _, bs := range src.BackupSets {
			add(bs.RetainedCopyEncryption.KeyProvider)
		}
	}
	return refs
}

// LastExternalKeySuite is the suite of the key ref yielded the last time
// custody resolved it (the alert tick's probe, a registration, a policy
// patch), and false when it has not been resolved yet. It resolves nothing
// and opens nothing: a store that is not open yet has resolved nothing.
func (c *KeyCustody) LastExternalKeySuite(ref secretref.Ref) (string, bool) {
	c.mu.Lock()
	s := c.store
	c.mu.Unlock()
	if s == nil {
		return "", false
	}
	return s.LastExternalKeySuite(ref)
}

// ExternalKeySuite is the suite of the key ref yields, resolved and
// registered nowhere (EPIC T, #1314). It is for an explicit write, the
// policy patch; a read surface uses LastExternalKeySuite. It opens the key
// directory like any call that needs custody, and writes nothing.
func (c *KeyCustody) ExternalKeySuite(ctx context.Context, ref secretref.Ref) (string, error) {
	s, err := c.Store(ctx)
	if err != nil {
		return "", err
	}
	return s.ExternalKeySuite(ctx, ref)
}

// RegisterExternal is placement.ExternalKeys: it resolves ref and returns the
// key's registry row, adding it the first time. It opens the key directory
// like any call that needs custody does, because that is where the store
// lives, but writes no file into it.
func (c *KeyCustody) RegisterExternal(ctx context.Context, ref secretref.Ref, at time.Time) (keycustody.KeyRecord, error) {
	s, err := c.Store(ctx)
	if err != nil {
		return keycustody.KeyRecord{}, err
	}
	return s.RegisterExternal(ctx, ref, at)
}

// NewKeyCustody returns the custodian for the deployment whose configuration
// file is path(), recording keys in journal.
//
// A deployment's first managed key is minted under the build's default
// suite (copycrypt.Default(), age-pq/v1 since #1313). A deployment that
// already holds an active key keeps that key's suite: a rotation with no
// suite named stays in it, and nothing here rotates by itself (ADR 0033 D4).
func NewKeyCustody(path func() string, journal *state.Journal) *KeyCustody {
	return &KeyCustody{path: path, journal: journal, format: copycrypt.Default()}
}

// registry is the journal-backed registry; it holds no state of its own.
func (c *KeyCustody) registry() keycustody.Registry {
	return keycustody.NewJournalRegistry(c.journal)
}

func (c *KeyCustody) dir() (string, error) {
	p := c.path()
	if p == "" {
		return "", ErrConfigNotFileBacked
	}
	return keycustody.DirBeside(p), nil
}

// Store opens the managed key store, creating its directory 0700 the first
// time. Every caller that needs key material or mints a key comes through
// here.
func (c *KeyCustody) Store(_ context.Context) (*keycustody.Store, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store != nil {
		return c.store, nil
	}
	dir, err := c.dir()
	if err != nil {
		return nil, err
	}
	s, err := keycustody.Open(dir, c.registry(), c.format)
	if err != nil {
		return nil, err
	}
	s.UseExternalRefs(c.externalRefs)
	c.store = s
	return s, nil
}

// Keys returns every managed key's record, never its material. It reads the
// registry and opens nothing.
func (c *KeyCustody) Keys(ctx context.Context) ([]keycustody.KeyRecord, error) {
	return c.registry().Keys(ctx)
}

// Active returns the key new copies are encrypted under, if there is one.
func (c *KeyCustody) Active(ctx context.Context) (keycustody.KeyRecord, bool, error) {
	keys, err := c.Keys(ctx)
	if err != nil {
		return keycustody.KeyRecord{}, false, err
	}
	for _, k := range keys {
		if k.Status == keycustody.StatusActive && k.Provider != keycustody.ProviderExternal {
			return k, true, nil
		}
	}
	return keycustody.KeyRecord{}, false, nil
}

// WithKey hands the material of managed key id to fn and zeroes it after.
func (c *KeyCustody) WithKey(ctx context.Context, id string, fn func(key []byte) error) error {
	s, err := c.Store(ctx)
	if err != nil {
		return err
	}
	return s.WithKey(ctx, id, fn)
}

// AlertConditions is keycustody.Store.AlertConditions for the alert pass
// (#1294). The caller only asks when some set requires encryption, so opening
// the store here, which creates the key directory the first time, never
// happens for a deployment that encrypts nothing. A store that cannot be
// opened is a look that could not be made: every required set goes on the
// unevaluated list rather than being resolved by omission.
func (c *KeyCustody) AlertConditions(ctx context.Context, policies []keycustody.SetPolicy) ([]alert.Condition, []alert.Subject) {
	s, err := c.Store(ctx)
	if err != nil {
		var unevaluated []alert.Subject
		for _, p := range policies {
			if p.Required {
				unevaluated = append(unevaluated, alert.Subject{Kind: alert.EncryptionKeyUnresolvable, Scope: p.Set})
			}
		}
		return nil, unevaluated
	}
	return s.AlertConditions(ctx, policies)
}

// ExternalKeysAtRisk is every external key that ACTIVE copies record and no
// declared reference currently yields, with those copies (#1304). A key row
// stores no reference, so this is how a dropped one is noticed. With no
// external key on record it reads the registry and opens nothing, so a
// deployment that uses none stays byte identical.
//
// It resolves references (a command reference is executed), so it is for the
// alert tick only, never for a request. The answer is kept for
// externalProbeTTL, so two asks in one tick resolve once, and
// LastExternalKeysAtRisk hands it to a health build without resolving.
func (c *KeyCustody) ExternalKeysAtRisk(ctx context.Context) ([]health.ExternalKeyAtRisk, error) {
	// The declared references are read from the running configuration, which
	// resolves nothing, so asking for them on every call is free. A probe of
	// a different list answers a different question: a reference dropped by
	// a reload is reported now, not when the TTL runs out.
	refs := fmt.Sprintf("%q", c.externalRefs())
	c.probeMu.Lock()
	if !c.probeAt.IsZero() && time.Since(c.probeAt) < externalProbeTTL && c.probedRefs == refs {
		out := c.probed
		c.probeMu.Unlock()
		return out, nil
	}
	c.probeMu.Unlock()
	out, err := c.probeExternalKeys(ctx)
	if err != nil {
		return nil, err
	}
	c.probeMu.Lock()
	c.probed, c.probeAt, c.probedRefs = out, time.Now(), refs
	c.probeMu.Unlock()
	return out, nil
}

// externalProbeTTL is how long an external key probe stands.
const externalProbeTTL = time.Minute

// LastExternalKeysAtRisk is the last ExternalKeysAtRisk answer, without
// resolving anything: nil until the alert tick has probed once.
func (c *KeyCustody) LastExternalKeysAtRisk() []health.ExternalKeyAtRisk {
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	return c.probed
}

func (c *KeyCustody) probeExternalKeys(ctx context.Context) ([]health.ExternalKeyAtRisk, error) {
	keys, err := c.Keys(ctx)
	if err != nil {
		return nil, err
	}
	external := false
	for _, k := range keys {
		external = external || (k.Provider == keycustody.ProviderExternal && k.Status != keycustody.StatusRetired)
	}
	if !external {
		return nil, nil
	}
	s, err := c.Store(ctx)
	if err != nil {
		return nil, err
	}
	unreadable, err := s.UnreadableExternalKeys(ctx)
	if err != nil {
		return nil, err
	}
	var out []health.ExternalKeyAtRisk
	for id, reason := range unreadable {
		copies, err := c.journal.ActivePlacementsRecordingKey(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(copies) > 0 {
			out = append(out, health.ExternalKeyAtRisk{KeyID: id, Reason: reason, Copies: copies})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].KeyID < out[j].KeyID })
	return out, nil
}

// Health is health.Report.KeyCustody. enabled is whether any backup set
// resolves to encryption required, which only the caller knows. When nothing
// requires encryption and no key directory exists the section is the zero
// value, which reads "this deployment encrypts nothing", and nothing is
// created to find that out.
func (c *KeyCustody) Health(ctx context.Context, enabled bool) (health.KeyCustodyHealth, error) {
	if !enabled {
		dir, err := c.dir()
		if err != nil {
			return health.KeyCustodyHealth{}, nil
		}
		if _, statErr := os.Stat(dir); errors.Is(statErr, os.ErrNotExist) {
			return health.KeyCustodyHealth{}, nil
		}
	}
	s, err := c.Store(ctx)
	if err != nil {
		return health.KeyCustodyHealth{}, fmt.Errorf("service: opening key custody: %w", err)
	}
	h, err := s.Health(ctx, enabled)
	if err != nil {
		return h, err
	}
	// Each suite something still depends on, with the lifecycle's verdict
	// (#1316). Two reads over ids and counts; no key file is opened for it.
	// The copy counts are the section's per-suite figure too (#1315), so the
	// health build does not make the same grouped read a second time.
	copies, err := c.journal.ActiveEncryptedCopiesBySuite(ctx)
	if err != nil {
		return h, err
	}
	h.EncryptedCopiesBySuite = copies
	keys, err := c.Keys(ctx)
	if err != nil {
		return h, err
	}
	h.Suites = keycustody.SuiteHealth(copycrypt.Suites(), copies, keys)
	return h, nil
}

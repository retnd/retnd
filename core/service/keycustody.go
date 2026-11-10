package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/retnd/retnd/core/internal/alert"
	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/keycustody"
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

	mu    sync.Mutex
	store *keycustody.Store
}

// NewKeyCustody returns the custodian for the deployment whose configuration
// file is path(), recording keys in journal.
func NewKeyCustody(path func() string, journal *state.Journal) *KeyCustody {
	return &KeyCustody{path: path, journal: journal, format: keycustody.AgeFormat{}}
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
		if k.Status == keycustody.StatusActive {
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
	return s.Health(ctx, enabled)
}

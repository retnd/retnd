package keycustody

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/retnd/retnd/core/internal/health"
	"github.com/retnd/retnd/core/internal/secretref"
)

// DirName is the directory managed keys live in, beside config.yaml, for
// the reason core/service's mediumCredentialsDirName gives: the backup root
// is what a NAS deployment exports over SMB, and a key under it is a key on
// the LAN. Beside config.yaml it mounts, and backs up, with the file that
// references it.
const DirName = "retained_copy_keys"

// DirBeside returns the managed key directory for a deployment whose
// configuration file is configPath.
func DirBeside(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), DirName)
}

// keyFileSuffix is the managed key file's extension. The file holds the
// format's secret as one line (for age, the identity string), so it is also
// a valid `age -i` identity file and secretref's whitespace trimming is
// harmless.
const keyFileSuffix = ".key"

var (
	// ErrUnknownKey names an id the registry never minted.
	ErrUnknownKey = errors.New("keycustody: no managed key with this id")

	// ErrNoActiveKey is Rotate's refusal when there is nothing to rotate.
	ErrNoActiveKey = errors.New("keycustody: no managed key is active")

	// ErrActiveKeyExists is Create's refusal when a key is already active:
	// a second active key is a rotation, which is a separate deliberate verb.
	ErrActiveKeyExists = errors.New("keycustody: a managed key is already active; rotate instead of creating a second one")

	// ErrNoSuchExport is Confirm's refusal when the digest matches no
	// recorded export for that key.
	ErrNoSuchExport = errors.New("keycustody: no exported bundle carries this digest for this key")

	// ErrKeyMismatch is WithKey's refusal when the file under a key id does
	// not derive the recipient the registry recorded for it: a swapped,
	// restored-from-the-wrong-backup or hand-edited key file. Using it would
	// write copies the registry says one key wrote and another can open.
	ErrKeyMismatch = errors.New("keycustody: the key file does not match the recipient recorded for its id")
)

// Store is the managed key store: one directory, one Registry, one Format.
// Every method is safe to call from a fresh process over the same
// directory, which is what the restart tests prove.
type Store struct {
	dir      string
	registry Registry
	format   Format
}

// Open opens the managed key store at dir, creating the directory 0700 if
// it does not exist, and refuses a directory whose custody has drifted.
// It also removes any .partial file a crashed Create left behind: a key
// that never reached the registry is a key nothing names.
func Open(dir string, registry Registry, format Format) (*Store, error) {
	if registry == nil || format == nil {
		return nil, errors.New("keycustody: Open needs a registry and a format")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("keycustody: creating key directory: %w", err)
	}
	if err := checkDirCustody(dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("keycustody: reading key directory: %w", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".partial") {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return &Store{dir: dir, registry: registry, format: format}, nil
}

// checkDirCustody is this package's own half of "0600 in 0700". secretref's
// ancestor walk refuses a directory another account can WRITE; a managed
// key directory is also refused when another account can READ or traverse
// it, because the directory listing names every key id this deployment
// has, and because a 0700 directory is what the docs promise. The final
// component must not be a symbolic link, for secretref's reason: the
// directories protecting the link say nothing about the ones protecting
// its target.
func checkDirCustody(dir string) error {
	li, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("keycustody: checking key directory: %w", err)
	}
	if li.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: key directory %s is a symbolic link; declare the directory it points at", secretref.ErrCustody, dir)
	}
	if !li.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", secretref.ErrCustody, dir)
	}
	if mode := li.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf(
			"%w: key directory %s has permissions %04o, which lets an account other than its owner reach the keys in it; "+
				"correct it (chmod 700 %s)",
			secretref.ErrCustody, dir, mode, dir)
	}
	return nil
}

func (s *Store) keyPath(id string) string { return filepath.Join(s.dir, id+keyFileSuffix) }

// Keys returns every managed key's record, oldest first.
func (s *Store) Keys(ctx context.Context) ([]KeyRecord, error) {
	return s.registry.Keys(ctx)
}

// Active returns the key new copies are encrypted under, if there is one.
func (s *Store) Active(ctx context.Context) (KeyRecord, bool, error) {
	keys, err := s.registry.Keys(ctx)
	if err != nil {
		return KeyRecord{}, false, err
	}
	for _, k := range keys {
		if k.Status == StatusActive {
			return k, true, nil
		}
	}
	return KeyRecord{}, false, nil
}

// Create mints the first managed key. It refuses while a key is active.
func (s *Store) Create(ctx context.Context, now time.Time) (KeyRecord, error) {
	if _, ok, err := s.Active(ctx); err != nil {
		return KeyRecord{}, err
	} else if ok {
		return KeyRecord{}, ErrActiveKeyExists
	}
	return s.mint(ctx, now, "")
}

// Rotate mints a new active key and moves the current one to superseded in
// one durable registry write. Every copy recorded against the old key keeps
// decrypting through it; nothing here re-encrypts anything (#992).
func (s *Store) Rotate(ctx context.Context, now time.Time) (KeyRecord, error) {
	active, ok, err := s.Active(ctx)
	if err != nil {
		return KeyRecord{}, err
	}
	if !ok {
		return KeyRecord{}, ErrNoActiveKey
	}
	return s.mint(ctx, now, active.ID)
}

// mint generates the material, writes the file under the commit discipline
// core/internal/lifecycle/commit.go uses for a local copy (temporary name,
// fsync, link without clobbering, unlink the temporary, fsync the
// directory), and only then records the key. A crash before the registry
// write leaves a file no record names, which Open sweeps if it was still
// .partial and which Probe reports as NotDeclared otherwise.
func (s *Store) mint(ctx context.Context, now time.Time, supersede string) (KeyRecord, error) {
	if err := checkDirCustody(s.dir); err != nil {
		return KeyRecord{}, err
	}
	id, err := newKeyID()
	if err != nil {
		return KeyRecord{}, err
	}
	secret, recipient, err := s.format.Generate()
	if err != nil {
		return KeyRecord{}, err
	}
	defer zero(secret)

	encoded := append(append([]byte(nil), secret...), '\n')
	defer zero(encoded)

	final := s.keyPath(id)
	tmp := final + ".partial"
	if err := writeFileSync(tmp, encoded, 0o600); err != nil {
		os.Remove(tmp)
		return KeyRecord{}, fmt.Errorf("keycustody: writing key file: %w", err)
	}
	if err := os.Link(tmp, final); err != nil {
		os.Remove(tmp)
		return KeyRecord{}, fmt.Errorf("keycustody: publishing key file: %w", err)
	}
	os.Remove(tmp)
	if err := fsyncDir(s.dir); err != nil {
		return KeyRecord{}, err
	}

	rec := KeyRecord{ID: id, Recipient: recipient, Created: now.UTC(), Status: StatusActive}
	if err := s.registry.AddKey(ctx, rec, supersede); err != nil {
		return KeyRecord{}, err
	}
	return rec, nil
}

// newKeyID is "k" and sixteen hex digits of CSPRNG output: nothing about
// the host, the time or the process, so the id is as stable as the file.
func newKeyID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("keycustody: generating key id: %w", err)
	}
	return "k" + hex.EncodeToString(b[:]), nil
}

// WithKey hands the material of managed key id to fn and zeroes it after.
// The file is read through secretref.Resolve, so the custody refusal is
// secretref's own, byte for byte: ErrCustody naming the path or the
// directory, never the content.
func (s *Store) WithKey(ctx context.Context, id string, fn func(key []byte) error) error {
	rec, err := s.record(ctx, id)
	if err != nil {
		return err
	}
	if err := checkDirCustody(s.dir); err != nil {
		return err
	}
	secret, err := secretref.Resolve(ctx, secretref.Ref{File: s.keyPath(id)})
	if err != nil {
		return err
	}
	key := []byte(secret.Reveal())
	defer zero(key)
	recipient, err := s.format.Recipient(key)
	if err != nil {
		return fmt.Errorf("keycustody: key file for %s: %w", id, err)
	}
	if recipient != rec.Recipient {
		return fmt.Errorf("%w: %s", ErrKeyMismatch, id)
	}
	return fn(key)
}

// record returns the registry's record for id or ErrUnknownKey.
func (s *Store) record(ctx context.Context, id string) (KeyRecord, error) {
	keys, err := s.registry.Keys(ctx)
	if err != nil {
		return KeyRecord{}, err
	}
	for _, k := range keys {
		if k.ID == id {
			return k, nil
		}
	}
	return KeyRecord{}, fmt.Errorf("%w: %s", ErrUnknownKey, id)
}

// Probe reports whether managed key id resolves right now, the way
// ProbeExternal does for a reference.
func (s *Store) Probe(ctx context.Context, id string) Probe {
	source := "managed key " + id
	if _, err := s.record(ctx, id); err != nil {
		if errors.Is(err, ErrUnknownKey) {
			return Probe{State: NotDeclared, Source: source}
		}
		return Probe{State: Unreachable, Source: source, Reason: err.Error()}
	}
	if err := s.WithKey(ctx, id, func([]byte) error { return nil }); err != nil {
		return Probe{State: Unreachable, Source: source, Reason: err.Error()}
	}
	return Probe{State: Resolves, Source: source}
}

// Resolvability probes whichever kind of provider p is.
func (s *Store) Resolvability(ctx context.Context, p Provider) Probe {
	switch {
	case p.Managed != "" && !p.External.IsZero():
		return Probe{State: Unreachable, Source: p.Describe(), Reason: "a set names both a managed key and an external reference, and there is no rule for choosing between them"}
	case p.Managed != "":
		return s.Probe(ctx, p.Managed)
	case !p.External.IsZero():
		probe := ProbeExternal(ctx, p.External)
		if probe.State != Resolves {
			return probe
		}
		// It resolves; it must also be a key of the format, or a copy
		// written under it could never be opened.
		if err := WithExternalKey(ctx, p.External, func(key []byte) error {
			_, err := s.format.Recipient(key)
			return err
		}); err != nil {
			return Probe{State: Unreachable, Source: probe.Source, Reason: err.Error()}
		}
		return probe
	default:
		return Probe{State: NotDeclared, Source: p.Describe()}
	}
}

// Health computes the FR-66 section: enabled is whether any set resolves
// to encryption required, which only the caller knows. The facts (which
// key is active, which keys have a confirmed bundle) are reported either
// way; the verdict depends on enabled, so a deployment with keys and
// nothing requiring them is not degraded.
func (s *Store) Health(ctx context.Context, enabled bool) (health.KeyCustodyHealth, error) {
	h := health.KeyCustodyHealth{Enabled: enabled}
	keys, err := s.registry.Keys(ctx)
	if err != nil {
		return h, err
	}
	confirmations, err := s.registry.Confirmations(ctx)
	if err != nil {
		return h, err
	}
	confirmedAt := map[string]time.Time{}
	for _, c := range confirmations {
		if prev, ok := confirmedAt[c.KeyID]; !ok || c.ConfirmedAt.After(prev) {
			confirmedAt[c.KeyID] = c.ConfirmedAt
		}
	}
	for _, k := range keys {
		if k.Status == StatusRetired {
			continue
		}
		at, confirmed := confirmedAt[k.ID]
		if k.Status == StatusActive {
			h.ActiveKeyID = k.ID
			h.BundleConfirmed = confirmed
			if confirmed {
				t := at
				h.BundleConfirmedAt = &t
			}
		}
		if !confirmed {
			h.UnconfirmedKeyIDs = append(h.UnconfirmedKeyIDs, k.ID)
		}
	}
	return h, nil
}

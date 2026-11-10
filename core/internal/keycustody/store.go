package keycustody

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/retnd/retnd/core/internal/copycrypt"
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

// Store is the managed key store: one directory, one Registry, and the
// Format a new key is minted under when no suite is named. A key already in
// the registry is read and checked under its own suite (KeyRecord.SuiteID),
// which may differ from that Format once a deployment has rotated across
// suites (EPIC T). Every method is safe to call from a fresh process over
// the same directory, which is what the restart tests prove.
type Store struct {
	dir      string
	registry Registry
	format   Format

	// externals reports the external references config declares right now
	// (the global provider and every per-set override). The registry records
	// an external key by id and recipient and never the reference, so this is
	// how an id becomes material again. Nil means none are declared.
	externals func() []secretref.Ref

	// lastSuites remembers, per external reference, the suite of the key it
	// yielded the last time something here resolved it (the alert tick's
	// probe, a registration, an explicit write), and forgets it the moment a
	// resolution fails. A read surface takes the suite from here and never
	// resolves a reference itself (EPIC T, #1315). The key is refKey's hash
	// of the whole reference, so two command references running one
	// executable with different arguments are two entries; the values are
	// suite ids. Nothing in it is material or is ever logged.
	suitesMu   sync.Mutex
	lastSuites map[string]string
}

// refKey identifies a reference by every field that decides what it
// yields: the file path, the variable name, and the command with all of its
// arguments. ref.String() is a description and leaves a command's arguments
// out on purpose, so it cannot be the key. The canonical form is a JSON
// array, hashed, so an argument never sits in the map in the clear.
func refKey(ref secretref.Ref) string {
	canon, err := json.Marshal([]any{ref.File, ref.Env, ref.Command})
	if err != nil {
		// Three strings and a string slice always marshal; refuse to share
		// an entry if that ever stops being true.
		return ""
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}

// noteExternalSuite records what resolving ref just showed.
func (s *Store) noteExternalSuite(ref secretref.Ref, suiteID string) {
	k := refKey(ref)
	if k == "" {
		return
	}
	s.suitesMu.Lock()
	defer s.suitesMu.Unlock()
	if s.lastSuites == nil {
		s.lastSuites = map[string]string{}
	}
	s.lastSuites[k] = suiteID
}

// forgetExternalSuite drops what an earlier resolution of ref showed,
// because a later one failed: a reference that does not resolve now has no
// suite to report.
func (s *Store) forgetExternalSuite(ref secretref.Ref) {
	s.suitesMu.Lock()
	defer s.suitesMu.Unlock()
	delete(s.lastSuites, refKey(ref))
}

// LastExternalKeySuite is the suite of the key ref yielded the last time it
// was resolved in this process, and false when it has not been. It resolves
// nothing: it is the answer for a read surface.
func (s *Store) LastExternalKeySuite(ref secretref.Ref) (string, bool) {
	s.suitesMu.Lock()
	defer s.suitesMu.Unlock()
	k := refKey(ref)
	if k == "" {
		return "", false
	}
	id, ok := s.lastSuites[k]
	return id, ok
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

// Active returns the active managed key, whatever its suite, if there is
// one. There is at most one (migration 0015's index). The engine does not
// use this to choose a key for a copy: it asks ActiveFor with the policy's
// suite, so an active key of another suite is never a substitute (FR-85).
func (s *Store) Active(ctx context.Context) (KeyRecord, bool, error) {
	keys, err := s.registry.Keys(ctx)
	if err != nil {
		return KeyRecord{}, false, err
	}
	for _, k := range keys {
		if k.Status == StatusActive && k.Provider != ProviderExternal {
			return k, true, nil
		}
	}
	return KeyRecord{}, false, nil
}

// ActiveFor returns the active managed key of suiteID, if there is one.
// This is the question the engine asks before a copy (FR-85): "the active
// key of the policy's suite", never "whatever key is active".
func (s *Store) ActiveFor(ctx context.Context, suiteID string) (KeyRecord, bool, error) {
	rec, ok, err := s.Active(ctx)
	if err != nil || !ok {
		return KeyRecord{}, false, err
	}
	if suiteOf(rec) != suiteID {
		return KeyRecord{}, false, nil
	}
	return rec, true, nil
}

// suiteOf is a record's suite id, with the empty id of a key written before
// migration 0020 read as age/v1.
func suiteOf(rec KeyRecord) string {
	if rec.SuiteID == "" {
		return AgeFormatID
	}
	return rec.SuiteID
}

// Create mints the first managed key under the store's format. It refuses
// while a key is active.
func (s *Store) Create(ctx context.Context, now time.Time) (KeyRecord, error) {
	return s.CreateIn(ctx, now, "")
}

// CreateIn mints the first managed key under suiteID, or under the store's
// format when suiteID is empty. It refuses while a managed key of any suite
// is active: a second active key is a rotation, which is a separate verb.
func (s *Store) CreateIn(ctx context.Context, now time.Time, suiteID string) (KeyRecord, error) {
	f, err := s.mintFormat(suiteID)
	if err != nil {
		return KeyRecord{}, err
	}
	if _, ok, err := s.Active(ctx); err != nil {
		return KeyRecord{}, err
	} else if ok {
		return KeyRecord{}, ErrActiveKeyExists
	}
	return s.mint(ctx, now, "", f)
}

// Rotate mints a new active key under the active key's own suite and
// moves the current one to superseded in one durable registry write, so a
// rotation with no suite named never changes the suite a deployment writes
// under (ADR 0033 D4). Every copy recorded against the old key keeps
// decrypting through it; nothing here re-encrypts anything (#992).
func (s *Store) Rotate(ctx context.Context, now time.Time) (KeyRecord, error) {
	return s.RotateTo(ctx, now, "")
}

// RotateTo is Rotate into suiteID (the active key's suite when empty). Rotating
// into another suite supersedes the current active key, whatever its suite,
// in the same durable write that adds the new one (ADR 0033 D3), so there
// is never a moment with two active managed keys or none.
func (s *Store) RotateTo(ctx context.Context, now time.Time, suiteID string) (KeyRecord, error) {
	active, ok, err := s.Active(ctx)
	if err != nil {
		return KeyRecord{}, err
	}
	if !ok {
		return KeyRecord{}, ErrNoActiveKey
	}
	if suiteID == "" {
		suiteID = suiteOf(active)
	}
	f, err := s.mintFormat(suiteID)
	if err != nil {
		return KeyRecord{}, err
	}
	return s.mint(ctx, now, active.ID, f)
}

// mintFormat is the Format a new key of suiteID is generated under. A suite
// this build does not know is refused before anything is generated or
// written; so is a retired one, which reads and never writes (ADR 0033 D6).
func (s *Store) mintFormat(suiteID string) (Format, error) {
	if suiteID == "" {
		return s.format, nil
	}
	f, err := s.formatOf(suiteID)
	if err != nil {
		return nil, err
	}
	if suite, ok := f.(copycrypt.Suite); ok && suite.Status() == copycrypt.StatusRetired {
		return nil, fmt.Errorf("%w: suite %s is retired and no new key is minted under it", ErrRetiredSuite, suiteID)
	}
	return f, nil
}

// ErrRetiredSuite is the refusal to mint a key under a retired suite.
var ErrRetiredSuite = errors.New("keycustody: the suite is retired")

// mint generates the material, writes the file under the commit discipline
// core/internal/lifecycle/commit.go uses for a local copy (temporary name,
// fsync, link without clobbering, unlink the temporary, fsync the
// directory), and only then records the key. A crash before the registry
// write leaves a file no record names, which Open sweeps if it was still
// .partial and which Probe reports as NotDeclared otherwise.
func (s *Store) mint(ctx context.Context, now time.Time, supersede string, f Format) (KeyRecord, error) {
	if err := checkDirCustody(s.dir); err != nil {
		return KeyRecord{}, err
	}
	id, err := newKeyID()
	if err != nil {
		return KeyRecord{}, err
	}
	secret, recipient, err := f.Generate()
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

	rec := KeyRecord{ID: id, Recipient: recipient, Created: now.UTC(), Status: StatusActive, SuiteID: f.ID()}
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
	if rec.Provider == ProviderExternal {
		return s.withExternalRecord(ctx, rec, fn)
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
	f, err := s.formatOf(rec.SuiteID)
	if err != nil {
		return fmt.Errorf("keycustody: key %s: %w", id, err)
	}
	recipient, err := f.Recipient(key)
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
			s.forgetExternalSuite(p.External)
			return probe
		}
		// It resolves; it must also be a key of a registered suite, or a
		// copy written under it could never be opened.
		if err := WithExternalKey(ctx, p.External, func(key []byte) error {
			_, suiteID, err := s.recipientOfAnySuite(key)
			if err == nil {
				s.noteExternalSuite(p.External, suiteID)
			}
			return err
		}); err != nil {
			s.forgetExternalSuite(p.External)
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
	var externalActive string
	for _, k := range keys {
		if k.Status == StatusRetired {
			continue
		}
		if k.Provider == ProviderExternal {
			// The operator holds an external key; there is no bundle to confirm.
			if k.Status == StatusActive && externalActive == "" {
				externalActive = k.ID
			}
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
	if h.ActiveKeyID == "" && externalActive != "" {
		// An external-only deployment: the operator holds the key, so there
		// is no managed key to be active and no bundle to confirm. Whether
		// the reference resolves is the alert condition's, not this state's.
		h.ActiveKeyID = externalActive
		h.BundleConfirmed = true
	}
	return h, nil
}

// writeFileSync writes b to path with mode, creating or truncating it, and
// fsyncs before closing so a rename after it publishes bytes that are on
// disk.
func writeFileSync(path string, b []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// fsyncDir makes a directory entry change (a link, a rename, an unlink)
// durable. Same discipline as core/internal/lifecycle/commit.go's fsyncDir.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("keycustody: fsync %s: %w", dir, err)
	}
	return nil
}

// recipientOfAnySuite derives the recipient of key under the first suite
// that accepts it: the store's own format first, then the rest of
// copycrypt's registry in table order. It is how an operator's external key
// is matched to the suite it belongs to without being told. A key no suite
// accepts is refused with the store's format's own error, which never
// quotes the key.
func (s *Store) recipientOfAnySuite(key []byte) (recipient, suiteID string, err error) {
	recipient, err = s.format.Recipient(key)
	if err == nil {
		return recipient, s.format.ID(), nil
	}
	for _, suite := range copycrypt.Suites() {
		if suite.ID() == s.format.ID() {
			continue
		}
		if r, rerr := suite.Recipient(key); rerr == nil {
			return r, suite.ID(), nil
		}
	}
	return "", "", err
}

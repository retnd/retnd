package config

import (
	"fmt"

	"github.com/retnd/retnd/core/internal/model"
	"github.com/retnd/retnd/core/internal/secretref"
)

// Retained-copy encryption configuration (EPIC N, N1.1 / FR-59): the
// deployment-wide default and the per-set override for encrypting the
// copies the artifact engine writes to a storage medium.
//
// # What is here and what is deliberately not
//
// The whole operator surface is two things: a mode (off or required at the
// deployment, inherit, off or required on a set) and a key provider (the
// product's own managed key, or an external reference in the file/env/
// command shape every other secret in this file uses). There is no cipher,
// no chunk size, no nonce length, no KDF cost and no algorithm knob, at
// either scope. ADR 0027 fixes the format as a property of the build and
// records its id and version per stored copy, so a second format can land
// later without a config field and without a one-way door. A per-set
// format would make one artifact's two copies unreadable by two different
// rules for no operator benefit, and a per-set rotation cadence is a
// property of a key, not of a backup set, so both are refused by the only
// mechanism that needs no code: Load's KnownFields(true) does not know
// them.
//
// # Why this is not key_encryption
//
// config.KeyEncryption (#298) already means "the key that protects secret
// material this manager persists to disk", and it is read by the SSH key
// store. Two key concepts under one name in a file read with
// KnownFields(true) would be unrecoverable: an operator who wrote the wrong
// one would be refused at Load with an error naming the right key, or
// worse, accepted with the wrong meaning. So this is one new top-level key,
// and key_encryption is neither extended nor moved.
//
// # The pattern
//
// Config.RetainedCopyEncryptionBlock is the operator's block, a pointer so
// that "wrote nothing" and "wrote off" stay two different statements and
// so a re-marshal of a file that never mentioned the key stays byte
// identical. Config.RetainedCopyEncryption is the resolved deployment
// policy Validate fills. BackupSet.RetainedCopyEncryptionConfig and
// BackupSet.RetainedCopyEncryption are the same pair one scope down, in
// exactly the three-field shape RetentionConfig and AssuranceConfig use.
// Every consumer reads the resolved field, and
// TestRetainedCopyEncryption_NoConsumerReadsThePointers fails a file that
// reaches for the pointer instead.

// Retained-copy encryption modes. The deployment block takes off or
// required; a backup set may also say inherit, which is the default.
const (
	RetainedCopyEncryptionInherit  = "inherit"
	RetainedCopyEncryptionOff      = "off"
	RetainedCopyEncryptionRequired = "required"
)

// Key provider kinds, as RetainedCopyKeyProvider.Kind reports them. These
// are also the values the journal's key registry stores in its provider
// column (state.KeyProviderManaged / KeyProviderExternal), so the two
// vocabularies are one.
const (
	KeyProviderManaged  = "managed"
	KeyProviderExternal = "external"
)

// GlobalRetainedCopyEncryptionModes and SetRetainedCopyEncryptionModes
// spell the closed vocabularies in a fixed order, for messages and for
// readers that render a chooser.
var (
	GlobalRetainedCopyEncryptionModes = []string{RetainedCopyEncryptionOff, RetainedCopyEncryptionRequired}
	SetRetainedCopyEncryptionModes    = []string{RetainedCopyEncryptionInherit, RetainedCopyEncryptionOff, RetainedCopyEncryptionRequired}
)

// RetainedCopyKeyProvider names WHERE the key that encrypts retained copies
// comes from, and carries no material. Exactly one of the two may be set.
//
//   - Managed: the product generates and keeps the key itself, under the
//     same file-custody rule secretref enforces, and the operator's duty is
//     the one-time export of the recovery bundle (FR-63, FR-66; #990).
//   - External: the operator holds the key and names it the way every other
//     secret in this file is named, as a file, an environment variable or a
//     command whose stdout is the secret. No KMS or Vault field: a command
//     resolver already reaches any of them, and a provider interface is a
//     separate decision this epic cut on purpose.
//
// There is no field for the key itself and there will not be. A key an
// operator could type into config.yaml is the exposure #298 exists to
// close for the SSH key, and compat fixture 62-invalid-inline-key-material
// pins that Load refuses one.
type RetainedCopyKeyProvider struct {
	Managed  bool          `yaml:"managed,omitempty"`
	External *SecretSource `yaml:"external,omitempty"`
}

// IsZero reports whether no provider is declared.
func (p RetainedCopyKeyProvider) IsZero() bool { return !p.Managed && p.External == nil }

// Kind is KeyProviderManaged, KeyProviderExternal, or "" when none is
// declared.
func (p RetainedCopyKeyProvider) Kind() string {
	switch {
	case p.Managed:
		return KeyProviderManaged
	case p.External != nil:
		return KeyProviderExternal
	default:
		return ""
	}
}

// ExternalRef is the external reference as the secretref resolver takes
// it, or the zero Ref for a managed or absent provider. It is the one place
// the schema type becomes the runtime one, so no caller rebuilds the triple
// by hand.
func (p RetainedCopyKeyProvider) ExternalRef() secretref.Ref {
	if p.External == nil {
		return secretref.Ref{}
	}
	return secretref.Ref{File: p.External.File, Env: p.External.Env, Command: p.External.Command}
}

// validate refuses a provider that names nothing or both, and an external
// reference that names no source or more than one. path is the field's
// own path for the message.
func (p RetainedCopyKeyProvider) validate(v *validator, path string) {
	named := 0
	if p.Managed {
		named++
	}
	if p.External != nil {
		named++
	}
	if named != 1 {
		v.addf("%s: name exactly one of managed: true or external: {file | env | command}", path)
		return
	}
	if p.External != nil {
		if err := p.External.validateExactlyOneSource(); err != nil {
			v.addf("%s.external: %v", path, err)
		}
	}
}

// RetainedCopyEncryptionBlock is the deployment-wide
// `retained_copy_encryption:` block. Nil on Config means "write nothing",
// which resolves to off with no provider: every configuration written
// before this key existed.
type RetainedCopyEncryptionBlock struct {
	// Mode is off or required. Empty is off.
	Mode string `yaml:"mode,omitempty"`

	// KeyProvider may be declared under mode off: that is a staged
	// provider, the way a storage medium no tier names yet is staged, and
	// a later flip to required finds it in place.
	KeyProvider *RetainedCopyKeyProvider `yaml:"key_provider,omitempty"`
}

// RetainedCopyEncryptionOverride is a backup set's own
// `retained_copy_encryption:` block: mode and key_provider, nothing else.
type RetainedCopyEncryptionOverride struct {
	// Mode is inherit, off or required. Empty is inherit.
	Mode string `yaml:"mode,omitempty"`

	// KeyProvider is only meaningful under mode required, where it replaces
	// the deployment's provider for this set's copies. Under inherit or
	// off it is refused rather than ignored.
	KeyProvider *RetainedCopyKeyProvider `yaml:"key_provider,omitempty"`
}

// RetainedCopyEncryption is the fully resolved policy one scope decides
// under: the deployment's, on Config, and each backup set's, on BackupSet.
// Mode is off or required; inherit never survives resolution.
type RetainedCopyEncryption struct {
	Mode        string
	KeyProvider RetainedCopyKeyProvider
}

// Enabled reports whether copies under this policy are to be encrypted.
func (e RetainedCopyEncryption) Enabled() bool { return e.Mode == RetainedCopyEncryptionRequired }

// EncryptsRetainedCopies reports whether a managed or external key guards
// this set's retained copies: the set resolves to required AND it is not a
// kopia-engine set. A kopia set never reaches the artifact copy path where
// retained_copy_encryption acts; its repository is always encrypted by kopia
// under the operator's own passphrase, so required on it is satisfied by the
// repository and layers nothing (ADR 0028 decision 4). The engine, health,
// metrics and the alert wiring all read this one method so they agree with
// the web UI. Enabled stays the question "what does the resolved policy say".
func (b BackupSet) EncryptsRetainedCopies() bool {
	return b.RetainedCopyEncryption.Enabled() && b.Engine != model.EngineKopia
}

// RetainedCopyEncryptionIsOverride reports whether this set declares its
// own block. It is the one sanctioned read of the pointer, and it answers
// "did the operator say something here", never "what did they say": the
// resolved field answers that.
func (b BackupSet) RetainedCopyEncryptionIsOverride() bool {
	return b.RetainedCopyEncryptionConfig != nil
}

// resolveRetainedCopyEncryption is phase 2 of Validate for the policy: the
// deployment block into c.RetainedCopyEncryption without touching the
// operator's own block, then every backup set from its override and that
// resolved global.
//
// Any mutation of c.RetainedCopyEncryptionBlock has to be followed by
// Validate or ResolveRetainedCopyEncryption. A consumer that patched the
// global and skipped it would leave every inheriting set deciding under the
// old policy, which is how the retention override flags were once a silent
// no-op.
func (c *Config) resolveRetainedCopyEncryption(v *validator) {
	global := c.resolveGlobalRetainedCopyEncryption(v)
	c.RetainedCopyEncryption = global
	for i := range c.Sources {
		for j := range c.Sources[i].BackupSets {
			bs := &c.Sources[i].BackupSets[j]
			path := fmt.Sprintf("sources[%d].backup_sets[%d]", i, j)
			bs.RetainedCopyEncryption = resolveSetRetainedCopyEncryption(v, path, bs.RetainedCopyEncryptionConfig, global)
		}
	}
}

// ResolveRetainedCopyEncryption runs the encryption phase on its own, for a
// caller that mutated the deployment block or builds a Config by hand, the
// way ResolveBackupSetRetention and ResolveAssurance do.
func (c *Config) ResolveRetainedCopyEncryption() error {
	v := &validator{}
	c.resolveRetainedCopyEncryption(v)
	return v.err()
}

func (c *Config) resolveGlobalRetainedCopyEncryption(v *validator) RetainedCopyEncryption {
	var b RetainedCopyEncryptionBlock
	if c.RetainedCopyEncryptionBlock != nil {
		b = *c.RetainedCopyEncryptionBlock
	}
	const p = "retained_copy_encryption"
	out := RetainedCopyEncryption{Mode: b.Mode}
	if out.Mode == "" {
		out.Mode = RetainedCopyEncryptionOff
	}
	if !containsString(GlobalRetainedCopyEncryptionModes, out.Mode) {
		v.addf("%s.mode: %q is not one of %v (inherit is a per-set spelling)", p, out.Mode, GlobalRetainedCopyEncryptionModes)
	}
	if b.KeyProvider != nil {
		b.KeyProvider.validate(v, p+".key_provider")
		out.KeyProvider = *b.KeyProvider
	}
	if out.Mode == RetainedCopyEncryptionRequired && out.KeyProvider.IsZero() {
		v.addf("%s.key_provider: mode required needs a key provider (managed: true, or an external file, env or command reference); with nothing to encrypt under, no copy could be written", p)
	}
	return out
}

// resolveSetRetainedCopyEncryption resolves one set's policy from its
// override and the already-resolved global. The override block itself is
// never written to.
func resolveSetRetainedCopyEncryption(v *validator, path string, o *RetainedCopyEncryptionOverride, global RetainedCopyEncryption) RetainedCopyEncryption {
	if o == nil {
		return global
	}
	path += ".retained_copy_encryption"
	if *o == (RetainedCopyEncryptionOverride{}) {
		v.addf("%s: names nothing; remove the block to inherit the deployment's policy, or name a mode", path)
		return global
	}
	if o.KeyProvider != nil {
		o.KeyProvider.validate(v, path+".key_provider")
	}
	switch o.Mode {
	case "", RetainedCopyEncryptionInherit:
		if o.KeyProvider != nil {
			v.addf("%s.key_provider: a key provider needs mode required; inherit takes the deployment's", path)
		}
		return global
	case RetainedCopyEncryptionOff:
		if o.KeyProvider != nil {
			v.addf("%s.key_provider: a key provider needs mode required; remove it, or set the mode", path)
		}
		return RetainedCopyEncryption{Mode: RetainedCopyEncryptionOff}
	case RetainedCopyEncryptionRequired:
		out := RetainedCopyEncryption{Mode: RetainedCopyEncryptionRequired, KeyProvider: global.KeyProvider}
		if o.KeyProvider != nil {
			out.KeyProvider = *o.KeyProvider
		}
		if out.KeyProvider.IsZero() {
			v.addf("%s.key_provider: mode required needs a key provider, on the set or on the deployment", path)
		}
		return out
	default:
		v.addf("%s.mode: %q is not one of %v", path, o.Mode, SetRetainedCopyEncryptionModes)
		return global
	}
}

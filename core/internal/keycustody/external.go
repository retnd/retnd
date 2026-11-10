package keycustody

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/retnd/retnd/core/internal/secretref"
)

// Resolvability is the probe's three-way answer about a key: whether a
// reference is declared, and if so whether it currently yields material.
// It is never the material.
type Resolvability int

const (
	// NotDeclared: no reference and no managed key id names a key at all.
	NotDeclared Resolvability = iota

	// Unreachable: a key is declared and, right now, cannot be resolved.
	// Probe.Reason says why in secretref's own words, which name the
	// source (a path, a variable, an executable) and never its content.
	Unreachable

	// Resolves: the declared key yielded material just now. The material
	// was discarded as soon as that was known.
	Resolves
)

// String renders the state as the word an operator reads.
func (r Resolvability) String() string {
	switch r {
	case NotDeclared:
		return "not_declared"
	case Unreachable:
		return "unreachable"
	case Resolves:
		return "resolves"
	default:
		return fmt.Sprintf("resolvability_%d", int(r))
	}
}

// MarshalText renders the state for JSON as the same word.
func (r Resolvability) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// Probe is one resolvability answer. It carries the reference DESCRIPTION
// (secretref.Ref.String, or "managed key <id>") because that is what the
// journal, the activity feed and the recovery sidecar record, and it is
// built so that it cannot carry anything else.
type Probe struct {
	State  Resolvability `json:"state"`
	Source string        `json:"source"`
	Reason string        `json:"reason,omitempty"`
}

// String renders the probe as one line.
func (p Probe) String() string {
	if p.Reason == "" {
		return p.State.String() + " (" + p.Source + ")"
	}
	return p.State.String() + " (" + p.Source + "): " + p.Reason
}

// ProbeExternal reports whether ref currently resolves, without keeping
// what it resolved to. It is the capability probe FR-64 asks for, and it
// reuses secretref's refusal vocabulary wholesale: ErrNoSource is
// NotDeclared, and ErrAmbiguousSource, ErrEmpty, ErrTooLarge, ErrCustody
// and every read failure are Unreachable with secretref's sentence as the
// reason.
func ProbeExternal(ctx context.Context, ref secretref.Ref) Probe {
	if ref.IsZero() {
		return Probe{State: NotDeclared, Source: ref.String()}
	}
	err := WithExternalKey(ctx, ref, func([]byte) error { return nil })
	if err != nil {
		return Probe{State: Unreachable, Source: ref.String(), Reason: err.Error()}
	}
	return Probe{State: Resolves, Source: ref.String()}
}

// WithExternalKey resolves ref through secretref and hands the material to
// fn, then zeroes the slice fn was given. The material is never returned as
// a value, and it is the format's job (ADR 0027) to decide what it does
// with an operator-supplied key of arbitrary length.
//
// secretref.Resolve returns the material as an obs.Secret, which is a Go
// string and cannot be zeroed; the copy made here is what this package can
// take responsibility for, exactly as secretref's fromEnv says of its own.
func WithExternalKey(ctx context.Context, ref secretref.Ref, fn func(key []byte) error) error {
	secret, err := secretref.Resolve(ctx, ref)
	if err != nil {
		return err
	}
	key := []byte(secret.Reveal())
	defer zero(key)
	return fn(key)
}

// Provider is the per-set key_provider in runtime form: a managed key by id
// or an external reference, and exactly one. The External field is
// secretref.Ref itself rather than a type shaped like it, so
// secretref's TestRefFieldSetMatchesTheConfiguredOnes keeps covering the
// only shape an external key can be declared in.
//
// #989's config type for key_provider resolves to this; the wiring is the
// service's, and this package takes the resolved value.
type Provider struct {
	Managed  string
	External secretref.Ref
}

// Describe names the provider the way every durable surface records it:
// a key id or a reference description, never material.
func (p Provider) Describe() string {
	switch {
	case p.Managed != "" && !p.External.IsZero():
		return "managed key " + p.Managed + " and external key from " + p.External.String()
	case p.Managed != "":
		return "managed key " + p.Managed
	case !p.External.IsZero():
		return "external key from " + p.External.String()
	default:
		return "no key provider"
	}
}

// String renders Describe, so a Provider in an error or a log line is the
// description. MarshalJSON does the same for the reasons secretref.Ref's
// four renderers give.
func (p Provider) String() string { return p.Describe() }

// MarshalJSON renders the description.
func (p Provider) MarshalJSON() ([]byte, error) {
	//nolint:wrapcheck // marshalling a string cannot fail for a reason a caller can act on
	return json.Marshal(p.Describe())
}

// SetPolicy is what one backup set has resolved to under
// retained_copy_encryption: whether encrypted copies are required, and
// through which provider. Required is false for both `off` and an
// `inherit` that resolved to off; the distinction is config's and does not
// reach here.
type SetPolicy struct {
	Set      string
	Required bool
	Provider Provider
}

// zero overwrites b in place, with the KeepAlive secretref's own zero uses
// so the compiler cannot elide the writes.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}

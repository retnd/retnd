// Package export is the artifact recovery domain (EPIC Q, FR-67 and FR-68):
// pick one retained copy of an artifact, pull its bytes through a bounded
// buffer into a staging file, prove they are the bytes the journal recorded,
// and only then hand them to exactly one declared target.
//
// It is a sibling of core/internal/recovery rather than part of it on
// purpose. That package is a leaf (the sidecar manifest format, imported by
// lifecycle and by catalog rebuild) and says so in its own doc; this one
// needs the placement ladder and the archive access states, and importing
// them from the leaf would turn every manifest reader into a dependent of
// both.
//
// Nothing in here executes a program. The package imports no os/exec, and a
// test holds it to that, because "recover" means "give back the retained
// bytes" and never "run something against them".
package export

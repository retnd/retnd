package packaging

import (
	"sort"
)

// This file owns canonical binary and command comparisons used by the
// packaging gates. The former deployment-name overlap is closed; only the
// current paths and entrypoints are accepted.

// LegacyBrandPath returns no alternate path because the deployment-name
// compatibility window is closed.
func LegacyBrandPath(string) string {
	return ""
}

// LegacyBrandName returns no alternate binary name because release manifests
// now use only canonical names.
func LegacyBrandName(string) string {
	return ""
}

// KnowsBinary reports whether path is a binary the canonical image
// answers to: one it contains, or a retained alias of one.
func (c Canonical) KnowsBinary(path string) bool {
	if contains(c.Binaries, path) {
		return true
	}
	_, retained := c.RetainedBinaries[path]
	return retained
}

// BinarySpellings is every path KnowsBinary accepts, sorted, for a
// refusal message that tells the reader what it would have taken.
func (c Canonical) BinarySpellings() []string {
	out := append([]string(nil), c.Binaries...)
	for alias := range c.RetainedBinaries {
		out = append(out, alias)
	}
	sort.Strings(out)
	return out
}

// CommandSpellings returns argv plus every spelling of it that names a
// retained alias of the same binary, so a caller can hold a provider to
// "runs the canonical command" without holding it to a name the image
// still answers to.
//
// Sorted by the alias so the answer is deterministic; a map iteration
// here would reorder the argv a failure message prints.
func (c Canonical) CommandSpellings(argv []string) [][]string {
	out := [][]string{argv}
	if len(argv) == 0 {
		return out
	}
	aliases := make([]string, 0, len(c.RetainedBinaries))
	for alias, real := range c.RetainedBinaries {
		if real == argv[0] {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		spelling := make([]string, 0, len(argv))
		spelling = append(spelling, alias)
		spelling = append(spelling, argv[1:]...)
		out = append(out, spelling)
	}
	return out
}

// sameEntrypoint rewrites argv[0] from a retained alias to the binary it
// links to, so two artifacts that name the same inode compare equal.
//
// CommandSpellings' inverse, and it exists because equivalence is a
// comparison of two artifacts rather than of one artifact against the
// contract: neither side is "the wanted argv" whose spellings could be
// enumerated, so the alias is normalised away instead.
func (c Canonical) sameEntrypoint(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	real, retained := c.RetainedBinaries[argv[0]]
	if !retained {
		return argv
	}
	out := make([]string, 0, len(argv))
	out = append(out, real)
	out = append(out, argv[1:]...)
	return out
}
